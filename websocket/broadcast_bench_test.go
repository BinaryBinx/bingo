package websocket

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
)

// Measure manager admission, real loopback writes and receipt by all eight
// peers. Clients drain concurrently as they would in a chat room.
func BenchmarkBroadcastLoopback(b *testing.B) {
	const peers = 8
	u := testUpgrader(b)
	ctx, cancel := context.WithTimeout(b.Context(), time.Minute)
	defer cancel()
	payload := []byte("broadcast benchmark")
	done := make(chan error, peers)
	for range peers {
		_, peer := testPair(b, u)
		go func() {
			for range b.N {
				kind, body, err := peer.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if kind != coderws.MessageBinary || !bytes.Equal(body, payload) {
					done <- errors.New("wrong broadcast payload")
					return
				}
			}
			done <- nil
		}()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := u.manager.BroadcastBinaryContext(ctx, payload); err != nil {
			b.Fatal(err)
		}
	}
	for range peers {
		if err := <-done; err != nil {
			b.Fatal(err)
		}
	}
}
