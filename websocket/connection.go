package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
)

const defaultOperationTimeout = 30 * time.Second

// Connection owns its transport until Close finishes. Read calls are serialized;
// concurrent writes honor an operation deadline that includes waiting for a lock.
type Connection struct {
	conn          *coderws.Conn
	transport     net.Conn
	transportMu   sync.Mutex
	transportDone bool
	broadcastMu   sync.Mutex
	broadcastStop context.CancelFunc // at most one admitted broadcast per manager
	manager       *ConnectionManager
	id            string
	ctx           context.Context
	cancel        context.CancelFunc
	readGate      chan struct{}
	writeGate     chan struct{}
	closed        atomic.Bool
	lastActivity  atomic.Int64
	readTimeout   time.Duration
	writeTimeout  time.Duration
	closeDone     chan struct{}
	closeErr      error // published by closing closeDone
}

func newConnection(manager *ConnectionManager, transport net.Conn, config Config) *Connection {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Connection{
		manager: manager, transport: transport, id: generateID(), ctx: ctx, cancel: cancel,
		readGate: make(chan struct{}, 1), writeGate: make(chan struct{}, 1),
		readTimeout: secondsToDuration(config.ReadTimeout), writeTimeout: secondsToDuration(config.WriteTimeout),
		closeDone: make(chan struct{}),
	}
	c.touch()
	return c
}

func (c *Connection) ID() string     { return c.id }
func (c *Connection) IsClosed() bool { return c.closed.Load() }
func (c *Connection) touch()         { c.lastActivity.Store(time.Now().UnixNano()) }

func (c *Connection) Send(value interface{}) error {
	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	if c.IsClosed() {
		return ErrConnectionClosed
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	} // Encoding errors do not close a healthy socket.
	return c.write(ctx, coderws.MessageText, data)
}

func (c *Connection) SendText(text string) error {
	return c.send(coderws.MessageText, []byte(text))
}

func (c *Connection) SendBinary(data []byte) error {
	return c.send(coderws.MessageBinary, data)
}

func (c *Connection) send(kind coderws.MessageType, data []byte) error {
	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	return c.write(ctx, kind, data)
}

func (c *Connection) write(ctx context.Context, kind coderws.MessageType, data []byte) error {
	if err := c.acquire(ctx, c.writeGate); err != nil {
		return err
	}
	defer func() { <-c.writeGate }()
	if err := c.conn.Write(ctx, kind, data); err != nil {
		c.CloseNow()
		return err
	}
	c.touch()
	return nil
}

func (c *Connection) Read(value interface{}) error {
	data, err := c.read()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		// Preserve wsjson's terminal malformed-message policy without waiting
		// for a hostile peer to complete an error-close handshake.
		c.CloseNow()
		return fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	return nil
}

func (c *Connection) ReadText() (string, error) {
	data, err := c.read()
	return string(data), err
}

func (c *Connection) ReadBinary() ([]byte, error) { return c.read() }

func (c *Connection) read() ([]byte, error) {
	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	if err := c.acquire(ctx, c.readGate); err != nil {
		return nil, err
	}
	defer func() { <-c.readGate }()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	c.touch()
	return data, nil
}

func (c *Connection) acquire(ctx context.Context, lock chan struct{}) error {
	if c.IsClosed() {
		return ErrConnectionClosed
	}
	select {
	case lock <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock
			return err
		}
		if c.IsClosed() {
			<-lock
			return ErrConnectionClosed
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Connection) operationContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(c.ctx, timeout)
	}
	return c.ctx, noOperationCancel
}

// The manager's broadcast gate serializes batches, and each connection appears
// once in a batch. Register its cancel function before waiting for the write
// gate so Close can interrupt either a queued or an in-progress write.
func (c *Connection) beginBroadcast(parent context.Context) context.Context {
	var ctx context.Context
	var cancel context.CancelFunc
	if c.writeTimeout > 0 {
		ctx, cancel = context.WithTimeout(parent, c.writeTimeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	c.broadcastMu.Lock()
	c.broadcastStop = cancel
	if c.ctx.Err() != nil {
		cancel()
	}
	c.broadcastMu.Unlock()
	return ctx
}

func (c *Connection) endBroadcast() {
	c.broadcastMu.Lock()
	stop := c.broadcastStop
	c.broadcastStop = nil
	c.broadcastMu.Unlock()
	stop()
}

func (c *Connection) cancelOperations() {
	c.cancel()
	c.broadcastMu.Lock()
	if c.broadcastStop != nil {
		c.broadcastStop()
	}
	c.broadcastMu.Unlock()
}

func noOperationCancel() {}

func (c *Connection) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultOperationTimeout)
	defer cancel()
	return c.CloseWithContext(ctx)
}

// CloseWithContext forces the transport closed when ctx expires and waits until
// cleanup finishes. Concurrent Close calls wait for the same completed close.
func (c *Connection) CloseWithContext(ctx context.Context) error {
	stop := context.AfterFunc(ctx, c.forceTransportClose)
	defer stop()
	if c.closed.CompareAndSwap(false, true) {
		c.cancelOperations()
		if ctx.Err() != nil {
			c.forceTransportClose()
		}
		var err error
		if c.conn != nil {
			err = c.conn.Close(coderws.StatusNormalClosure, "")
		}
		c.finishClose(err)
	}
	select {
	case <-c.closeDone:
		if err := ctx.Err(); err != nil {
			return err
		}
		return c.closeErr
	case <-ctx.Done():
		c.forceTransportClose()
		<-c.closeDone
		return ctx.Err()
	}
}

// CloseNow closes the raw transport as well: coder/websocket.CloseNow alone waits
// for an already running Close handshake instead of interrupting it.
func (c *Connection) CloseNow() { _ = c.closeNow() }

func (c *Connection) closeNow() error {
	c.forceTransportClose()
	if c.closed.CompareAndSwap(false, true) {
		c.cancelOperations()
		var err error
		if c.conn != nil {
			err = c.conn.CloseNow()
		}
		c.finishClose(err)
	}
	<-c.closeDone
	return c.closeErr
}

func (c *Connection) forceTransportClose() {
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	if !c.transportDone && c.transport != nil {
		_ = c.transport.Close()
	}
}

func (c *Connection) finishClose(err error) {
	// fasthttp may recycle its connection wrapper after the handler returns.
	// Late cancellation and repeated Close must not touch the recycled wrapper.
	c.transportMu.Lock()
	c.transportDone = true
	c.transportMu.Unlock()
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	c.closeErr = err
	c.manager.removeClosed(c)
}
