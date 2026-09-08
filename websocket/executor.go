package websocket

import (
	"sync"
	"sync/atomic"
	"time"
)

const broadcastWorkerIdle = 30 * time.Second

// A manager reuses at most managerWorkers goroutines for consecutive broadcasts.
// Workers start lazily and retire after idle time or shutdown. There is no message
// backlog: run waits for its whole batch, retaining existing order/backpressure.
type connectionExecutor struct {
	mu        sync.Mutex
	jobs      chan *connectionBatch
	workers   sync.WaitGroup
	count     int
	timer     *time.Timer
	idleUntil time.Time
	closed    bool
}

type connectionBatch struct {
	conns []*Connection
	fn    func(*Connection) error
	next  atomic.Int64
	done  sync.WaitGroup
	first sync.Once
	err   error
}

func (b *connectionBatch) run() {
	defer b.done.Done()
	conns, fn := b.conns, b.fn
	// Claim one peer at a time. Reserving a chunk behind a slow socket strands
	// healthy peers in that chunk even when the other workers are idle.
	for {
		index := int(b.next.Add(1) - 1)
		if index >= len(conns) {
			return
		}
		if err := fn(conns[index]); err != nil {
			b.first.Do(func() { b.err = err })
		}
	}
}

func (e *connectionExecutor) run(conns []*Connection, fn func(*Connection) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrManagerClosed
	}
	if len(conns) == 0 {
		return nil
	}
	if len(conns) == 1 {
		return fn(conns[0])
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	if e.jobs == nil {
		e.jobs = make(chan *connectionBatch)
	}
	wanted := min(managerWorkers, len(conns))
	for e.count < wanted {
		jobs := e.jobs
		e.workers.Go(func() {
			for batch := range jobs {
				batch.run()
			}
		})
		e.count++
	}
	batch := &connectionBatch{conns: conns, fn: fn}
	batch.done.Add(wanted)
	for i := 0; i < wanted; i++ {
		e.jobs <- batch
	}
	batch.done.Wait()
	// Idle workers must not retain the most recent payload or connection slice.
	batch.conns, batch.fn = nil, nil
	e.idleUntil = time.Now().Add(broadcastWorkerIdle)
	if e.timer == nil {
		e.timer = time.AfterFunc(broadcastWorkerIdle, e.expire)
	} else {
		e.timer.Reset(broadcastWorkerIdle)
	}
	return batch.err
}

func (e *connectionExecutor) expire() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.jobs == nil {
		return
	}
	// A timer callback may already be queued when the next broadcast resets it.
	if remaining := time.Until(e.idleUntil); remaining > 0 {
		e.timer.Reset(remaining)
		return
	}
	e.stopWorkers()
	e.timer = nil
}

func (e *connectionExecutor) stopWorkers() {
	if e.jobs != nil {
		close(e.jobs)
		e.workers.Wait()
		e.jobs, e.count = nil, 0
	}
}

func (e *connectionExecutor) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.stopWorkers()
}
