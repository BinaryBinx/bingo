package middleware

import (
	"context"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/BinaryBinx/bingo/internal/responsemeta"
	"github.com/valyala/fasthttp"
)

// Timeout bounds response waiting and supplies cooperative cancellation through
// core.RequestContext.Context. It cannot terminate arbitrary blocked Go code.
// The outermost Timeout owns the deadline; nested instances run synchronously.
// Bingo middleware stop response post-processing after timeout. Keep custom
// middleware with response post-processing inside Timeout. If it wraps Timeout,
// it must check LastTimeoutErrorResponse after next before touching the response;
// a worker must not read that field concurrently with the timeout timer.
// fasthttp retains the worker's real ctx (including Conn/hijack support); the
// timeout response sent to the client is a separate snapshot.
func Timeout(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		if duration <= 0 {
			return next
		}
		// An outer goroutine cannot recover a worker panic.
		protected := Recovery()(next)
		handler := fasthttp.TimeoutHandler(func(ctx *fasthttp.RequestCtx) {
			execution := ctx.UserValue(timeoutExecutionKey{}).(*timeoutExecution)
			execution.started.Store(true)
			defer execution.finish(ctx)
			protected(ctx)
		}, duration, "Request timeout")
		return func(ctx *fasthttp.RequestCtx) {
			if requestcontext.HasTimeout(ctx) {
				next(ctx)
				return
			}
			work, admitted := requestcontext.StartWork(ctx)
			if !admitted {
				resetErrorResponse(ctx, fasthttp.StatusServiceUnavailable, "Service Unavailable")
				ctx.Response.Header.Set("Retry-After", "1")
				return
			}
			if work != nil {
				// Keep one wrapper reference until timeout/cleanup ownership has
				// been decided, and another for the potentially detached worker.
				defer work.Done()
				work.Retain()
			}
			workContext, cancel := context.WithTimeout(requestcontext.From(ctx), duration)
			requestcontext.SetTimeout(ctx, workContext)
			execution := &timeoutExecution{leases: retainConcurrencyLeases(ctx), work: work}
			responsemeta.TrackTimeoutHeaders(ctx, &execution.headers)
			ctx.SetUserValue(timeoutExecutionKey{}, execution)
			defer cancel()
			execution.invokeNative(ctx, handler)
			if !execution.started.Load() && ctx.LastTimeoutErrorResponse() == nil {
				// fasthttp can reject admission without ever starting a worker.
				// Restore the error metadata even when used without an App work
				// tracker or a ConcurrencyLimit lease. No worker can publish now.
				execution.mu.Lock()
				execution.finished = true
				execution.mu.Unlock()
				execution.releaseWork()
				execution.headers.Apply(&ctx.Response.Header)
				ctx.Response.Header.Set("Retry-After", "1")
			}
			// fasthttp and context have separate timers. If the deadline is due,
			// let the context timer publish DeadlineExceeded before cancel would
			// otherwise publish Canceled. This never waits for business work.
			if deadline, ok := workContext.Deadline(); ok && !time.Now().Before(deadline) {
				<-workContext.Done()
			}
			// A cooperating handler may finish as the context timer fires, just
			// before fasthttp's timer is selected. Still report a deadline expiry.
			if workContext.Err() == context.DeadlineExceeded && ctx.LastTimeoutErrorResponse() == nil {
				ctx.TimeoutError("Request timeout")
			}
			if response := ctx.LastTimeoutErrorResponse(); response != nil {
				execution.headers.Apply(&response.Header)
				execution.abandon(ctx)
			}
		}
	}
}

type timeoutExecutionKey struct{}
type timeoutExecution struct {
	mu                           sync.Mutex
	started                      atomic.Bool
	finished, abandoned, cleaned bool
	leases                       *concurrencyLease
	work                         *requestcontext.WorkTracker
	headers                      responsemeta.TimeoutHeaders
}

func (execution *timeoutExecution) invokeNative(ctx *fasthttp.RequestCtx, handler fasthttp.RequestHandler) {
	returned := false
	defer func() {
		// Native admission can panic while closing a preexisting user stream in
		// its 429 response, before it ever starts our worker. Release the reserved
		// references on that exceptional path; keep propagating the original panic.
		if !returned && !execution.started.Load() {
			execution.releaseWork()
		}
	}()
	handler(ctx)
	returned = true
}

func (execution *timeoutExecution) releaseWork() {
	releaseConcurrencyLeases(execution.leases)
	if execution.work != nil {
		execution.work.Done()
	}
}

func (execution *timeoutExecution) finish(ctx *fasthttp.RequestCtx) {
	defer execution.releaseWork()
	execution.mu.Lock()
	execution.finished = true
	clean := execution.abandoned && !execution.cleaned
	execution.cleaned = execution.cleaned || clean
	execution.mu.Unlock()
	if clean {
		cleanupTimedOutRequest(ctx)
	}
}

func (execution *timeoutExecution) abandon(ctx *fasthttp.RequestCtx) {
	execution.mu.Lock()
	execution.abandoned = true
	clean := execution.finished && !execution.cleaned
	execution.cleaned = execution.cleaned || clean
	execution.mu.Unlock()
	if clean {
		// This is the response goroutine. User Close methods may block; cleanup
		// must not delay sending the timeout snapshot. Usually the still-running
		// worker performs cleanup itself through finish instead.
		// The outer request is still admitted. Retain cleanup before it returns,
		// even if the worker has already dropped its own tracking reference.
		if execution.work != nil {
			execution.work.Retain()
		}
		go func() {
			if execution.work != nil {
				defer execution.work.Done()
			}
			cleanupTimedOutRequest(ctx)
		}()
	}
}

// fasthttp detaches timed-out contexts instead of returning them to its pool.
// Once the worker has finished, close late streams and user io.Closers and remove
// multipart temporary files explicitly; GC cannot release arbitrary resources.
// No other goroutine accesses these fields now; the timeout snapshot is untouched.
func cleanupTimedOutRequest(ctx *fasthttp.RequestCtx) {
	// Remove user closers first so one panicking Close neither crashes an
	// unprotected cleanup goroutine nor prevents other resources being closed.
	type resource struct {
		key    any
		closer io.Closer
	}
	var resources []resource
	ctx.VisitUserValuesAll(func(key, value any) {
		if closer, ok := value.(io.Closer); ok {
			resources = append(resources, resource{key, closer})
		}
	})
	for _, resource := range resources {
		ctx.RemoveUserValue(resource.key)
		safeTimeoutCleanup(func() { _ = resource.closer.Close() })
	}
	safeTimeoutCleanup(ctx.Response.Reset)
	safeTimeoutCleanup(ctx.Request.Reset)
}

func safeTimeoutCleanup(cleanup func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("Panic recovered during timeout cleanup: %v", recovered)
		}
	}()
	cleanup()
}

// Capture ownership before next. Inside the worker, even reading fasthttp's
// timeoutResponse field races with the timer goroutine writing it. Outside the
// worker, next has already returned after that write and the snapshot is safe.
type responseGuard struct{ worker context.Context }

func guardResponse(ctx *fasthttp.RequestCtx) responseGuard {
	if requestcontext.HasTimeout(ctx) {
		return responseGuard{worker: requestcontext.From(ctx)}
	}
	return responseGuard{}
}

func (guard responseGuard) abandoned(ctx *fasthttp.RequestCtx) bool {
	if guard.worker != nil {
		return guard.worker.Err() != nil
	}
	return ctx.LastTimeoutErrorResponse() != nil
}
