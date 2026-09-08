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
	ctx, cancel := c.operationContext(context.Background(), c.writeTimeout)
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
	return c.send(context.Background(), coderws.MessageText, []byte(text))
}

func (c *Connection) SendBinary(data []byte) error {
	return c.send(context.Background(), coderws.MessageBinary, data)
}

func (c *Connection) send(parent context.Context, kind coderws.MessageType, data []byte) error {
	ctx, cancel := c.operationContext(parent, c.writeTimeout)
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
	ctx, cancel := c.operationContext(context.Background(), c.readTimeout)
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

func (c *Connection) operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := c.ctx
	cancel := func() {}
	stop := func() bool { return false }
	if parent.Done() != nil {
		ctx, cancel = context.WithCancel(parent)
		stop = context.AfterFunc(c.ctx, cancel)
		if c.ctx.Err() != nil {
			cancel()
		}
	}
	if timeout > 0 {
		limited, stopTimer := context.WithTimeout(ctx, timeout)
		return limited, func() { stop(); stopTimer(); cancel() }
	}
	return ctx, func() { stop(); cancel() }
}

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
		c.cancel()
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
		c.cancel()
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
