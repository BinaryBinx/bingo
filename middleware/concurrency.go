package middleware

import (
	"sync/atomic"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
)

// ConcurrencyLimit caps admitted business executions, including work continuing
// after Timeout returns 408. A full limiter rejects immediately with 503; there
// is no waiting queue. <= 0 disables the limit. The returned middleware shares
// one budget across every handler wrapped by that same middleware instance.
// Streaming/hijacked connection lifetimes are not counted after next returns.
func ConcurrencyLimit(maxConcurrent int) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	var active atomic.Int64
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		if maxConcurrent <= 0 {
			return next
		}
		return func(ctx *fasthttp.RequestCtx) {
			for {
				current := active.Load()
				if current >= int64(maxConcurrent) {
					resetErrorResponse(ctx, fasthttp.StatusServiceUnavailable, "Service Unavailable")
					ctx.Response.Header.Set("Retry-After", "1")
					return
				}
				if active.CompareAndSwap(current, current+1) {
					break
				}
			}
			if requestcontext.HasTimeout(ctx) {
				// Already executing in the worker: its stack owns the whole lease.
				defer active.Add(-1)
				next(ctx)
				return
			}
			// An inner Timeout can outlive this call. It retains the immutable
			// lease chain before starting its worker and releases it on completion.
			previous, _ := ctx.UserValue(concurrencyLeaseKey{}).(*concurrencyLease)
			lease := &concurrencyLease{active: &active, previous: previous}
			lease.refs.Store(1)
			ctx.SetUserValue(concurrencyLeaseKey{}, lease)
			defer func() {
				lease.release()
				// A timed-out worker still owns the user-value collection.
				if ctx.LastTimeoutErrorResponse() == nil {
					if previous == nil {
						ctx.RemoveUserValue(concurrencyLeaseKey{})
					} else {
						ctx.SetUserValue(concurrencyLeaseKey{}, previous)
					}
				}
			}()
			next(ctx)
		}
	}
}

type concurrencyLeaseKey struct{}
type concurrencyLease struct {
	active   *atomic.Int64
	previous *concurrencyLease
	refs     atomic.Int32
}

func (lease *concurrencyLease) release() {
	if lease.refs.Add(-1) == 0 {
		lease.active.Add(-1)
	}
}

func retainConcurrencyLeases(ctx *fasthttp.RequestCtx) *concurrencyLease {
	head, _ := ctx.UserValue(concurrencyLeaseKey{}).(*concurrencyLease)
	for lease := head; lease != nil; lease = lease.previous {
		lease.refs.Add(1)
	}
	return head
}

func releaseConcurrencyLeases(head *concurrencyLease) {
	for lease := head; lease != nil; lease = lease.previous {
		lease.release()
	}
}
