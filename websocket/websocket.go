package websocket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type Config struct {
	EnableCompression  bool
	MaxMessageSize     int64
	ReadTimeout        int // Seconds; zero disables the operation timeout.
	WriteTimeout       int
	InsecureSkipVerify bool
	OriginPatterns     []string
}

func DefaultConfig() *Config {
	return &Config{EnableCompression: true, MaxMessageSize: 1 << 20, ReadTimeout: 30, WriteTimeout: 30}
}

type WebSocketUpgrader struct {
	config  *Config
	manager *ConnectionManager
}

func NewWebSocketUpgrader(config *Config) *WebSocketUpgrader {
	if config == nil {
		config = DefaultConfig()
	}
	copyConfig := *config
	copyConfig.OriginPatterns = append([]string(nil), config.OriginPatterns...)
	if copyConfig.MaxMessageSize <= 0 {
		copyConfig.MaxMessageSize = 1 << 20
	}
	return &WebSocketUpgrader{config: &copyConfig, manager: NewConnectionManager()}
}

// Capture the transport at the HTTP hijack boundary. coder.Conn.CloseNow waits
// for an existing Close handshake, so force-close must interrupt transport I/O
// first rather than relying only on the high-level WebSocket closing flag.
type captureWriter struct {
	http.ResponseWriter
	transport net.Conn
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *captureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.transport = c
	}
	return c, rw, err
}
func (w *captureWriter) WriteHeaderNow() {
	if writer, ok := w.ResponseWriter.(interface{ WriteHeaderNow() }); ok {
		writer.WriteHeaderNow()
	}
}

func (w *WebSocketUpgrader) Upgrade(writer http.ResponseWriter, request *http.Request) (*Connection, error) {
	if w.manager.closed.Load() {
		http.Error(writer, "WebSocket server closed", 503)
		return nil, ErrConnectionClosed
	}
	options := &websocket.AcceptOptions{InsecureSkipVerify: w.config.InsecureSkipVerify, OriginPatterns: w.config.OriginPatterns, CompressionMode: websocket.CompressionDisabled}
	if w.config.EnableCompression {
		options.CompressionMode = websocket.CompressionNoContextTakeover
	}
	capture := &captureWriter{ResponseWriter: writer}
	conn, err := websocket.Accept(capture, request, options)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(w.config.MaxMessageSize)
	c := &Connection{conn: conn, transport: capture.transport, id: generateID(), readTimeout: secondsToDuration(w.config.ReadTimeout), writeTimeout: secondsToDuration(w.config.WriteTimeout)}
	if !w.manager.Add(c) {
		_ = capture.transport.Close()
		_ = conn.CloseNow()
		return nil, errors.New("websocket: max connections reached or manager closed")
	}
	return c, nil
}

func (w *WebSocketUpgrader) GetManager() *ConnectionManager { return w.manager }

// Connection owns one transport. closed rejects new operations as soon as closing
// starts; closeDone means transport cleanup has actually completed. The manager
// retains closing connections until completion so Shutdown can still force them.
type Connection struct {
	conn         *websocket.Conn
	transport    net.Conn
	manager      *ConnectionManager
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	readGate     chan struct{}
	writeGate    chan struct{}
	closed       atomic.Bool
	lastActivity atomic.Int64
	readTimeout  time.Duration
	writeTimeout time.Duration
	closeDone    chan struct{}
	finishOnce   sync.Once
	forceOnce    sync.Once
}

func (c *Connection) ID() string     { return c.id }
func (c *Connection) IsClosed() bool { return c.closed.Load() }

func (c *Connection) operationContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return c.ctx, func() {}
	}
	return context.WithTimeout(c.ctx, timeout)
}

func (c *Connection) acquire(ctx context.Context, gate chan struct{}) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		if c.closed.Load() {
			<-gate
			return ErrConnectionClosed
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Encode before transport I/O: an unsupported Go value does not close a healthy
// connection. Once I/O starts, any coder.Conn error is terminal.
func (c *Connection) Send(v interface{}) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(websocket.MessageText, data)
}
func (c *Connection) SendText(text string) error   { return c.write(websocket.MessageText, []byte(text)) }
func (c *Connection) SendBinary(data []byte) error { return c.write(websocket.MessageBinary, data) }

func (c *Connection) write(kind websocket.MessageType, data []byte) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	if err := c.acquire(ctx, c.writeGate); err != nil {
		return err
	}
	defer func() { <-c.writeGate }()
	err := c.conn.Write(ctx, kind, data)
	if err != nil {
		c.CloseNow()
	} else {
		c.lastActivity.Store(time.Now().UnixNano())
	}
	return err
}

func (c *Connection) Read(v interface{}) error {
	data, err := c.ReadBinary()
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, v); err != nil {
		c.CloseNow()
	}
	return err
}
func (c *Connection) ReadText() (string, error) {
	data, err := c.ReadBinary()
	return string(data), err
}
func (c *Connection) ReadBinary() ([]byte, error) {
	if c.closed.Load() {
		return nil, ErrConnectionClosed
	}
	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	if err := c.acquire(ctx, c.readGate); err != nil {
		return nil, err
	}
	defer func() { <-c.readGate }()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		c.CloseNow()
	} else {
		c.lastActivity.Store(time.Now().UnixNano())
	}
	return data, err
}

func (c *Connection) finishClose() {
	c.finishOnce.Do(func() {
		c.manager.Remove(c.id)
		close(c.closeDone)
	})
}

func (c *Connection) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		<-c.closeDone
		return nil
	}
	c.cancel()
	defer c.finishClose()
	if c.conn == nil {
		return nil
	}
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// CloseNow can interrupt Close even after graceful closing has begun.
func (c *Connection) CloseNow() {
	c.closed.Store(true)
	c.cancel()
	c.forceOnce.Do(func() {
		if c.transport != nil {
			_ = c.transport.Close()
		}
		if c.conn != nil {
			_ = c.conn.CloseNow()
		}
		c.finishClose()
	})
}

func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if uint64(seconds) > uint64((1<<63-1)/time.Second) {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(seconds) * time.Second
}

type ConnectionManager struct {
	connections    map[string]*Connection
	mu             sync.RWMutex
	timeout        int
	maxConns       int
	ctx            context.Context
	cancel         context.CancelFunc
	closed         atomic.Bool
	shutdownDone   chan struct{}
	cleanupDone    chan struct{}
	cleanupStarted bool
}

func NewConnectionManager() *ConnectionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConnectionManager{connections: make(map[string]*Connection), timeout: 3600, maxConns: 1000, ctx: ctx, cancel: cancel, shutdownDone: make(chan struct{}), cleanupDone: make(chan struct{})}
}

func (m *ConnectionManager) Add(c *Connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c == nil || m.closed.Load() || c.closed.Load() || (m.maxConns > 0 && len(m.connections) >= m.maxConns) {
		return false
	}
	if _, exists := m.connections[c.id]; exists {
		return false
	}
	c.manager = m
	c.ctx, c.cancel = context.WithCancel(m.ctx)
	c.readGate = make(chan struct{}, 1)
	c.writeGate = make(chan struct{}, 1)
	c.closeDone = make(chan struct{})
	c.lastActivity.Store(time.Now().UnixNano())
	m.connections[c.id] = c
	if !m.cleanupStarted {
		m.cleanupStarted = true
		go m.cleanupConnections()
	}
	return true
}

func (m *ConnectionManager) Remove(id string) { m.mu.Lock(); delete(m.connections, id); m.mu.Unlock() }
func (m *ConnectionManager) Get(id string) (*Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.connections[id]
	return c, ok
}
func (m *ConnectionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}
func (m *ConnectionManager) GetAll() []*Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conns := make([]*Connection, 0, len(m.connections))
	for _, c := range m.connections {
		conns = append(conns, c)
	}
	return conns
}

// Broadcast encodes once. Bounded workers prevent a single slow peer from
// blocking every other peer, without starting one goroutine per connection.
func (m *ConnectionManager) Broadcast(v interface{}) {
	if data, err := json.Marshal(v); err == nil {
		m.broadcast(websocket.MessageText, data)
	}
}
func (m *ConnectionManager) BroadcastText(text string) {
	m.broadcast(websocket.MessageText, []byte(text))
}
func (m *ConnectionManager) BroadcastBinary(data []byte) { m.broadcast(websocket.MessageBinary, data) }
func (m *ConnectionManager) broadcast(kind websocket.MessageType, data []byte) {
	conns := m.GetAll()
	var next atomic.Uint64
	var wg sync.WaitGroup
	for i := 0; i < min(64, len(conns)); i++ {
		wg.Go(func() {
			for {
				n := int(next.Add(1)) - 1
				if n >= len(conns) {
					return
				}
				_ = conns[n].write(kind, data)
			}
		})
	}
	wg.Wait()
}

func (m *ConnectionManager) CloseAll() { m.Shutdown(context.Background()) }

func (m *ConnectionManager) cleanupConnections() {
	defer close(m.cleanupDone)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			timeout := secondsToDuration(m.timeout)
			now := time.Now().UnixNano()
			var expired []*Connection
			for _, c := range m.connections {
				if timeout > 0 && now-c.lastActivity.Load() > int64(timeout) {
					expired = append(expired, c)
				}
			}
			m.mu.RUnlock()
			for _, c := range expired {
				c.CloseNow()
			}
		}
	}
}

// Shutdown includes dispatch and handshakes in one budget. Connections remain
// tracked until transport completion, including Close calls started elsewhere.
func (m *ConnectionManager) Shutdown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		select {
		case <-m.shutdownDone:
			return
		case <-ctx.Done():
		}
		for _, c := range m.GetAll() {
			c.CloseNow()
		}
		<-m.shutdownDone
		return
	}
	m.closed.Store(true)
	m.cancel()
	if !m.cleanupStarted {
		close(m.cleanupDone)
	}
	conns := make([]*Connection, 0, len(m.connections))
	for _, c := range m.connections {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	defer close(m.shutdownDone)
	var wg sync.WaitGroup
	var next atomic.Uint64
	for i := 0; i < min(64, len(conns)); i++ {
		wg.Go(func() {
			for {
				n := int(next.Add(1)) - 1
				if n >= len(conns) {
					return
				}
				if ctx.Err() != nil {
					conns[n].CloseNow()
				} else {
					_ = conns[n].Close()
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		for _, c := range conns {
			c.CloseNow()
		}
		<-done
	}
	<-m.cleanupDone
}

func (m *ConnectionManager) SetTimeout(timeout int) { m.mu.Lock(); m.timeout = timeout; m.mu.Unlock() }
func (m *ConnectionManager) SetMaxConns(max int)    { m.mu.Lock(); m.maxConns = max; m.mu.Unlock() }
func (m *ConnectionManager) GetTimeout() int        { m.mu.RLock(); defer m.mu.RUnlock(); return m.timeout }
func (m *ConnectionManager) GetMaxConns() int       { m.mu.RLock(); defer m.mu.RUnlock(); return m.maxConns }
