package websocket

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBroadcastSlowPeerDoesNotReserveHealthyPeers(t *testing.T) {
	var executor connectionExecutor
	defer executor.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peers := make([]*Connection, 256)
	for i := range peers {
		peers[i] = &Connection{}
	}
	var delivered atomic.Int32
	healthyDone, result := make(chan struct{}), make(chan error, 1)
	go func() {
		result <- executor.run(peers, func(peer *Connection) error {
			if peer == peers[0] {
				<-ctx.Done()
				return ctx.Err()
			}
			if delivered.Add(1) == 255 {
				close(healthyDone)
			}
			return nil
		})
	}()
	select {
	case <-healthyDone:
	case <-time.After(2 * time.Second):
		cancel()
		<-result
		t.Fatalf("healthy peers stranded behind a slow peer: delivered %d/255", delivered.Load())
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("slow-peer error lost: %v", err)
	}
}
