package websocket

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationContextPreservesBothCancellationSources(t *testing.T) {
	for _, source := range []string{"parent", "connection", "operation"} {
		t.Run(source, func(t *testing.T) {
			lifetime, closeConnection := context.WithCancel(context.Background())
			defer closeConnection()
			c := &Connection{ctx: lifetime}
			parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
			defer cancelParent()
			operation, cancel := c.operationContext(parent, time.Minute)
			defer cancel()
			switch source {
			case "parent":
				cancelParent()
			case "connection":
				closeConnection()
			case "operation":
				cancel()
			}
			select {
			case <-operation.Done():
			case <-time.After(time.Second):
				t.Fatal("operation did not cancel")
			}
			if source != "parent" && parent.Err() != nil {
				t.Fatal("operation canceled its caller")
			}
			if source != "connection" && lifetime.Err() != nil {
				t.Fatal("operation canceled its connection")
			}
		})
	}
	lifetime, cancelConnection := context.WithCancel(context.Background())
	defer cancelConnection()
	c := &Connection{ctx: lifetime}
	parent, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	operation, cancel := c.operationContext(parent, time.Hour)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	operationDeadline, _ := operation.Deadline()
	if !parentDeadline.Equal(operationDeadline) {
		t.Fatal("earlier parent deadline was replaced")
	}
	<-operation.Done()
	if !errors.Is(operation.Err(), context.DeadlineExceeded) {
		t.Fatal("deadline cause lost")
	}
}

func TestConnectionExecutorBoundsReuseAndRetirement(t *testing.T) {
	e := &connectionExecutor{}
	defer e.close()
	conns := make([]*Connection, 1000)
	entered, release := make(chan struct{}, managerWorkers), make(chan struct{})
	var active, peak, calls atomic.Int32
	sentinel := errors.New("recipient error")
	result := make(chan error, 1)
	go func() {
		result <- e.run(conns, func(*Connection) error {
			current := active.Add(1)
			for previous := peak.Load(); current > previous; previous = peak.Load() {
				if peak.CompareAndSwap(previous, current) {
					break
				}
			}
			if calls.Add(1) <= managerWorkers {
				entered <- struct{}{}
			}
			<-release
			active.Add(-1)
			return sentinel
		})
	}()
	for i := 0; i < managerWorkers; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("broadcast workers did not run in parallel")
		}
	}
	close(release)
	if err := <-result; !errors.Is(err, sentinel) || calls.Load() != 1000 || peak.Load() > managerWorkers {
		t.Fatalf("batch err=%v calls=%d peak=%d", err, calls.Load(), peak.Load())
	}
	for i := 0; i < 20; i++ {
		if err := e.run(conns, func(*Connection) error { calls.Add(1); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 21000 {
		t.Fatal("reused executor dropped or repeated recipients")
	}
	// Trigger idle expiry without making the test sleep for the production interval.
	e.mu.Lock()
	e.timer.Stop()
	e.idleUntil = time.Now().Add(-time.Second)
	e.mu.Unlock()
	e.expire()
	e.mu.Lock()
	stopped := e.jobs == nil && e.count == 0
	e.mu.Unlock()
	if !stopped {
		t.Fatal("idle workers retained")
	}
	if err := e.run(conns[:2], func(*Connection) error { return nil }); err != nil {
		t.Fatal("retired executor could not restart")
	}
	e.close()
	if err := e.run(conns, func(*Connection) error { return nil }); !errors.Is(err, ErrManagerClosed) {
		t.Fatal("closed executor admitted work")
	}
}

func TestRepeatedBroadcastPreservesPerPeerOrder(t *testing.T) {
	u := testUpgrader(t)
	_, first := testPair(t, u)
	_, second := testPair(t, u)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 20; i++ {
		text := fmt.Sprint(i)
		if err := u.manager.BroadcastTextContext(ctx, text); err != nil {
			t.Fatal(err)
		}
		_, a, errA := first.Read(ctx)
		_, b, errB := second.Read(ctx)
		if errA != nil || errB != nil || string(a) != text || string(b) != text {
			t.Fatalf("broadcast order/data: %q %q / %v %v", a, b, errA, errB)
		}
	}
	first.CloseNow()
	second.CloseNow()
	u.manager.Shutdown(context.Background())
	u.manager.broadcastWorkers.mu.Lock()
	defer u.manager.broadcastWorkers.mu.Unlock()
	if u.manager.broadcastWorkers.jobs != nil {
		t.Fatal("shutdown retained broadcast workers")
	}
}
