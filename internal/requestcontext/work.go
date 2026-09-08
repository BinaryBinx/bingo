package requestcontext

import (
	"context"
	"sync/atomic"

	"github.com/valyala/fasthttp"
)

// WorkTracker accounts for timeout wrappers and their detached business work.
// The low bit seals admission; the remaining bits count live references. Stop
// and TryStart are atomic with respect to each other, so drain cannot miss a
// worker racing shutdown. Synchronous HTTP requests are drained by fasthttp.
// There is no per-request allocation or waiter goroutine.
type WorkTracker struct {
	state atomic.Int64
	done  chan struct{}
}

func NewWorkTracker() *WorkTracker { return &WorkTracker{done: make(chan struct{})} }

func (t *WorkTracker) TryStart() bool {
	for {
		state := t.state.Load()
		if state&1 != 0 {
			return false
		}
		if t.state.CompareAndSwap(state, state+2) {
			return true
		}
	}
}

func (t *WorkTracker) Stopping() bool { return t.state.Load()&1 != 0 }

// Retain must be called while holding an existing reference, before handing
// work to another goroutine. It may extend already admitted work after Stop.
func (t *WorkTracker) Retain() { t.state.Add(2) }

func (t *WorkTracker) Done() {
	if t.state.Add(-2) == 1 {
		close(t.done)
	}
}

func (t *WorkTracker) Stop() <-chan struct{} {
	if t.state.Or(1) == 0 {
		close(t.done)
	}
	return t.done
}

type workKey struct{}

// WithWorkTracker is used once per App. This standard value context preserves
// the underlying cancel context's efficient propagation, unlike a custom Done.
func WithWorkTracker(parent context.Context, tracker *WorkTracker) context.Context {
	return context.WithValue(parent, workKey{}, tracker)
}

// StartWork seals admission against shutdown. Callers outside an App have no
// tracker; their own server is responsible for the middleware lifecycle.
func StartWork(ctx *fasthttp.RequestCtx) (*WorkTracker, bool) {
	tracker, _ := From(ctx).Value(workKey{}).(*WorkTracker)
	return tracker, tracker == nil || tracker.TryStart()
}
