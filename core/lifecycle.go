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
	<-app.shutdownCh
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

// ShutdownWithContext starts shutdown once. Concurrent callers wait on the same
// completion event, or their own context. The first caller sets the drain budget.
func (app *App) ShutdownWithContext(ctx context.Context) error {
	app.lifecycleMu.Lock()
	if app.shuttingDown {
		app.lifecycleMu.Unlock()
		select {
		case <-app.shutdownCh:
			return app.shutdownErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	app.shuttingDown = true
	upgrader := app.wsUpgrader
	hooks := append([]func(context.Context) error(nil), app.shutdownHooks...)
	app.lifecycleMu.Unlock()
	app.cancel()
	// Stop accepting HTTP immediately, while upgraded connections also drain.
	httpDone := make(chan error, 1)
	go func() { httpDone <- app.server.ShutdownWithContext(ctx) }()
	var errs []error
	if upgrader != nil {
		upgrader.GetManager().Shutdown(ctx)
	}
	for _, hook := range hooks {
		if err := callShutdownHook(ctx, hook); err != nil {
			errs = append(errs, err)
		}
	}
	if err := <-httpDone; err != nil {
		errs = append(errs, err)
	}
	app.shutdownErr = errors.Join(errs...)
	close(app.shutdownCh)
	return app.shutdownErr
}

func callShutdownHook(ctx context.Context, hook func(context.Context) error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("shutdown hook panic: %v", value)
		}
	}()
	return hook(ctx)
}

// OnShutdown registers cleanup for resources owned by the application, including
// hijacked sockets. Hooks must honor ctx and must not call Shutdown recursively.
func (app *App) OnShutdown(hook func(context.Context) error) error {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	if app.shuttingDown {
		return ErrAppClosed
	}
	if hook == nil {
		return errors.New("bingo: nil shutdown hook")
	}
	app.shutdownHooks = append(app.shutdownHooks, hook)
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
