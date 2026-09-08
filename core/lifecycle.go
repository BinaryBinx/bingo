package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/BinaryBinx/bingo/websocket"
	"github.com/valyala/fasthttp"
)

var ErrAppClosed = errors.New("bingo: application is shutting down or closed")

// readyListener acknowledges registration inside fasthttp.Serve, before Accept
// blocks. Shutdown cannot overtake Serve before its listener has been registered.
type readyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *readyListener) signalReady()              { l.once.Do(func() { close(l.ready) }) }
func (l *readyListener) Accept() (net.Conn, error) { l.signalReady(); return l.Listener.Accept() }

func (app *App) startServing(listen func() (net.Listener, error)) error {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	if app.shuttingDown {
		return ErrAppClosed
	}
	if app.started {
		return fasthttp.ErrAlreadyServing
	}
	app.started = true
	ln, err := listen()
	if err != nil {
		app.serveErr = err
		close(app.serveDone)
		return err
	}
	listener := &readyListener{Listener: ln, ready: make(chan struct{})}
	go func() {
		app.serveErr = app.server.Serve(listener)
		close(app.serveDone)
		listener.signalReady() // Also release startup if Serve fails before Accept.
	}()
	<-listener.ready
	return nil
}

// Run blocks until both serving and graceful shutdown have finished.
func (app *App) Run() error {
	addr := net.JoinHostPort(app.config.Host, strconv.Itoa(app.config.Port))
	err := app.startServing(func() (net.Listener, error) { return net.Listen("tcp", addr) })
	if err != nil {
		if !errors.Is(err, fasthttp.ErrAlreadyServing) && !errors.Is(err, ErrAppClosed) {
			_ = app.Shutdown()
		}
		return fmt.Errorf("%w: %w", ErrServerStart, err)
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	select {
	case <-app.serveDone:
	case <-quit:
	case <-app.shutdownCh:
	}
	// Serve returning after its listener closes is not a drain-completion event.
	shutdownErr := app.Shutdown()
	if shutdownErr != nil {
		// A deadline must also bound Run. The coordinator can finish safe cleanup
		// later if the caller keeps the process alive; never report a clean exit.
		select {
		case <-app.serveDone:
			return errors.Join(app.serveErr, shutdownErr)
		default:
			return shutdownErr
		}
	}
	<-app.serveDone
	return errors.Join(app.serveErr, shutdownErr)
}

// Serve takes ownership of ln, including closing it when startup is rejected.
// It returns when the listener stops; use Run or await Shutdown to await draining.
func (app *App) Serve(ln net.Listener) error {
	err := app.startServing(func() (net.Listener, error) { return ln, nil })
	if err != nil {
		_ = ln.Close()
		return err
	}
	<-app.serveDone
	return app.serveErr
}

func (app *App) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return app.ShutdownWithContext(ctx)
}

// ShutdownWithContext seals admission, cancels business contexts and starts one
// shutdown coordinator. Callers wait for completion or the earlier of their own
// deadline and the first caller's budget. Timeout workers and their cleanup are
// included in draining. Exceeding the budget returns an error, never success.
// If work ignores cancellation, the coordinator defers resource cleanup until
// it actually exits; it cannot forcibly terminate application Go code.
func (app *App) ShutdownWithContext(ctx context.Context) error {
	app.lifecycleMu.Lock()
	if !app.shuttingDown {
		app.shuttingDown = true
		app.shutdownContext = ctx
		stopping := append([]func(context.Context) error(nil), app.stoppingHooks...)
		cleanup := append([]func(context.Context) error(nil), app.shutdownHooks...)
		drained := app.work.Stop()
		app.cancel()
		go app.shutdown(ctx, app.wsUpgrader, stopping, cleanup, drained)
	}
	budget := app.shutdownContext
	app.lifecycleMu.Unlock()
	// Prefer an already completed result even if the caller canceled afterwards.
	select {
	case <-app.shutdownCh:
		return app.shutdownErr
	default:
	}
	select {
	case <-app.shutdownCh:
		return app.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	case <-budget.Done():
		return budget.Err()
	}
}

func (app *App) shutdown(ctx context.Context, upgrader *websocket.WebSocketUpgrader, stopping, cleanup []func(context.Context) error, drained <-chan struct{}) {
	// Keep fasthttp's drain alive after a caller's deadline: otherwise its return
	// no longer tells us whether an active response stream is using resources.
	// This is one goroutine per App, not a goroutine per request or per hook.
	httpDone := make(chan error, 1)
	go func() { httpDone <- app.server.Shutdown() }()
	wsDone := make(chan error, 1)
	if upgrader != nil {
		go func() { wsDone <- upgrader.GetManager().ShutdownWithContext(ctx) }()
	} else {
		wsDone <- nil
	}
	var errs []error
	for _, hook := range stopping {
		if err := callShutdownHook(ctx, hook); err != nil {
			errs = append(errs, err)
		}
	}
	if err := <-httpDone; err != nil {
		errs = append(errs, err)
	}
	if err := <-wsDone; err != nil {
		errs = append(errs, err)
	}
	<-drained
	// This phase is safe for database pools and managed static roots, including
	// when an uncooperative timeout worker finished after the original budget.
	for _, hook := range cleanup {
		if err := callShutdownHook(ctx, hook); err != nil {
			errs = append(errs, err)
		}
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	app.shutdownErr = errors.Join(errs...)
	close(app.shutdownCh)
}

func callShutdownHook(ctx context.Context, hook func(context.Context) error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("shutdown hook panic: %v", value)
		}
	}()
	return hook(ctx)
}

// OnStopping registers notifications run while HTTP and WebSocket connections
// drain. Use it to stop producers or close custom hijacked connections; resource
// destruction needed by active handlers belongs in OnShutdown instead.
// Hooks run sequentially, must honor ctx and must not recursively call Shutdown.
func (app *App) OnStopping(hook func(context.Context) error) error {
	return app.addShutdownHook(hook, true)
}

// OnShutdown registers resource cleanup after HTTP, WebSocket and tracked
// business work have drained. On a missed deadline it runs only when late work
// exits, with the original (possibly expired) context. Hooks must honor ctx and
// must not recursively call Shutdown. Use OnStopping for pre-drain notifications.
func (app *App) OnShutdown(hook func(context.Context) error) error {
	return app.addShutdownHook(hook, false)
}

func (app *App) addShutdownHook(hook func(context.Context) error, stopping bool) error {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	if app.shuttingDown {
		return ErrAppClosed
	}
	if hook == nil {
		return errors.New("bingo: nil shutdown hook")
	}
	if stopping {
		app.stoppingHooks = append(app.stoppingHooks, hook)
	} else {
		app.shutdownHooks = append(app.shutdownHooks, hook)
	}
	return nil
}

// GetWebSocketUpgrader lazily creates the upgrader; obtaining it after shutdown
// returns a closed manager that rejects new connections.
func (app *App) GetWebSocketUpgrader() *websocket.WebSocketUpgrader {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	if app.wsUpgrader == nil {
		app.wsUpgrader = websocket.NewWebSocketUpgrader(nil)
		if app.shuttingDown {
			app.wsUpgrader.GetManager().Shutdown(context.Background())
		}
	}
	return app.wsUpgrader
}
