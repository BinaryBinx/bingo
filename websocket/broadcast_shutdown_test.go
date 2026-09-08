package websocket

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingBroadcastJSON struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (value blockingBroadcastJSON) MarshalJSON() ([]byte, error) {
	value.entered <- struct{}{}
	<-value.release
	return []byte(`"finished"`), nil
}

func TestShutdownDoesNotWaitForBroadcastEncoder(t *testing.T) {
	u, connection, _ := reviewPair(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	result := make(chan error, 1)
	go func() {
		result <- u.manager.BroadcastContext(context.Background(), blockingBroadcastJSON{entered, release})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("encoder did not start")
	}

	// Waiting broadcasts must keep applying backpressure without starting more
	// application encoders. Shutdown releases them even while the first is stuck.
	var calls atomic.Int32
	queued := make(chan error, 32)
	for i := 0; i < cap(queued); i++ {
		go func() { queued <- u.manager.BroadcastContext(context.Background(), countedJSON{&calls}) }()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- u.manager.ShutdownWithContext(shutdownCtx) }()
	select {
	case err := <-shutdown:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unresponsive peer shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for application JSON encoder")
	}
	if u.manager.Count() != 0 || !connection.IsClosed() {
		t.Fatal("shutdown returned with an owned connection")
	}
	select {
	case <-connection.closeDone:
	default:
		t.Fatal("shutdown returned before transport cleanup completed")
	}
	for i := 0; i < cap(queued); i++ {
		select {
		case err := <-queued:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrManagerClosed) {
				t.Fatalf("queued broadcast: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown left a queued broadcast waiting on the encoder")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("queued broadcasts encoded %d values", calls.Load())
	}
	u.manager.broadcastWorkers.mu.Lock()
	stopped := u.manager.broadcastWorkers.closed && u.manager.broadcastWorkers.jobs == nil
	u.manager.broadcastWorkers.mu.Unlock()
	if !stopped {
		t.Fatal("shutdown retained its delivery executor")
	}
	// A late encoder may finish in its original caller, but cannot restart the
	// executor or send to a transport which shutdown has already released.
	unblock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("late encoder result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("released encoder did not return")
	}
	if err := u.manager.ShutdownWithContext(context.Background()); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated shutdown: %v", err)
	}
}

func TestEmptyBroadcastSkipsApplicationEncoding(t *testing.T) {
	manager := NewConnectionManager()
	defer manager.Shutdown(context.Background())
	var calls atomic.Int32
	if err := manager.BroadcastContext(context.Background(), countedJSON{&calls}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("empty recipient set invoked the JSON encoder")
	}
	// An unencodable value is harmless when no recipient requires a payload.
	if err := manager.BroadcastContext(context.Background(), make(chan int)); err != nil {
		t.Fatalf("empty broadcast encoded unsupported value: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.BroadcastContext(canceled, countedJSON{&calls}); !errors.Is(err, context.Canceled) {
		t.Fatalf("empty canceled broadcast: %v", err)
	}
}

func TestBroadcastEncodingErrorReleasesAdmission(t *testing.T) {
	u, connection, client := reviewPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := u.manager.BroadcastContext(ctx, make(chan int)); err == nil {
		t.Fatal("expected JSON encoding error")
	}
	if err := u.manager.BroadcastTextContext(ctx, "healthy"); err != nil {
		t.Fatal(err)
	}
	_, data, err := client.Read(ctx)
	if err != nil || string(data) != "healthy" || connection.IsClosed() {
		t.Fatalf("encoding error damaged broadcast delivery: %q, %v", data, err)
	}
}
