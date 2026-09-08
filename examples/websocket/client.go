package main

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fasthttp/websocket"
)

const maxPendingChatBytes = 256 << 10

// All messages, including welcome/commands/broadcasts, use one writer per peer.
// Both queue length and bytes are bounded. Slow peers are disconnected instead
// of blocking the room or retaining an arbitrary number of messages.
type chatClient struct {
	*websocket.Conn
	transport net.Conn
	queue     chan []byte
	done      chan struct{}
	finished  chan struct{}
	once      sync.Once
	pending   atomic.Int64
}

func newChatClient(conn *websocket.Conn, transport net.Conn) *chatClient {
	c := &chatClient{Conn: conn, transport: transport, queue: make(chan []byte, 64), done: make(chan struct{}), finished: make(chan struct{})}
	go c.writeLoop()
	return c
}

func (c *chatClient) Close() error {
	c.once.Do(func() { close(c.done); _ = c.transport.Close() })
	return nil
}

// data must remain immutable after enqueue; all callers use fresh JSON buffers.
func (c *chatClient) WriteMessage(_ int, data []byte) error {
	select {
	case <-c.done:
		return errors.New("chat connection closed")
	default:
	}
	if c.pending.Add(int64(len(data))) > maxPendingChatBytes {
		c.pending.Add(-int64(len(data)))
		c.Close()
		return errors.New("chat byte queue full")
	}
	select {
	case <-c.done:
		c.pending.Add(-int64(len(data)))
		return errors.New("chat connection closed")
	case c.queue <- data:
		return nil
	default:
		c.pending.Add(-int64(len(data)))
		c.Close()
		return errors.New("chat message queue full")
	}
}

func (c *chatClient) writeLoop() {
	defer close(c.finished)
	defer c.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WebSocket writer panic: %v", r)
		}
	}()
	for {
		select {
		case <-c.done:
			return
		case data := <-c.queue:
			c.pending.Add(-int64(len(data)))
			if err := c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

// Shutdown rejects late hijack callbacks, closes sockets, then waits for their
// readers/writers to finish. Registered with App.OnShutdown in NewChatRoom.
func (cr *ChatRoom) Shutdown(ctx context.Context) error {
	cr.mu.Lock()
	cr.closed = true
	conns := make([]*chatClient, 0, len(cr.users))
	for _, c := range cr.users {
		conns = append(conns, c)
	}
	cr.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	done := make(chan struct{})
	go func() { cr.connections.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
