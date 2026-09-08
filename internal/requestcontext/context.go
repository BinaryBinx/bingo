package requestcontext

import (
	"context"
	"github.com/valyala/fasthttp"
)

type contextKey struct{}

// Snapshot only the shutdown channel. Passing RequestCtx itself as a parent
// would let context's timer goroutine call Value while the handler mutates its
// non-thread-safe user values (and would retain a pooled request after return).
type serverContext struct {
	context.Context
	done <-chan struct{}
}

func (c serverContext) Done() <-chan struct{} { return c.done }
func (c serverContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

// From provides a cooperative business context. fasthttp.RequestCtx itself only
// signals server shutdown; timeout middleware attaches a request deadline here.
func From(ctx *fasthttp.RequestCtx) context.Context {
	if value, ok := ctx.UserValue(contextKey{}).(context.Context); ok {
		return value
	}
	return serverContext{Context: context.Background(), done: ctx.Done()}
}

func Set(ctx *fasthttp.RequestCtx, value context.Context) { ctx.SetUserValue(contextKey{}, value) }

// Timeout ownership is installed before the worker starts.
type timeoutKey struct{}

func HasTimeout(ctx *fasthttp.RequestCtx) bool { return ctx.UserValue(timeoutKey{}) != nil }
func SetTimeout(ctx *fasthttp.RequestCtx, value context.Context) {
	Set(ctx, value)
	ctx.SetUserValue(timeoutKey{}, true)
}
