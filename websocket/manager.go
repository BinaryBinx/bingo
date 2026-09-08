package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
)

const managerWorkers = 32

type ConnectionManager struct {
	connections      map[string]*Connection
	mu               sync.RWMutex
	timeout          int
	maxConns         int
	closed           atomic.Bool
	ctx              context.Context
	cancel           context.CancelFunc
	pending          map[*upgradeReservation]struct{}
	upgrades         sync.WaitGroup
	cleanup          sync.WaitGroup
	cleanupStop      context.CancelFunc
	broadcasts       sync.WaitGroup
	broadcastGate    chan struct{}
	broadcastWorkers connectionExecutor
	shutdownDone     chan struct{}
	shutdownErr      error
}

// NewConnectionManager allocates no background goroutine. The idle scanner starts
// on first use and stops again when the last connection is removed.
func NewConnectionManager() *ConnectionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConnectionManager{
		connections: make(map[string]*Connection), timeout: 3600, maxConns: 1000,
		ctx: ctx, cancel: cancel, pending: make(map[*upgradeReservation]struct{}),
		broadcastGate: make(chan struct{}, 1), shutdownDone: make(chan struct{}),
	}
}

func (m *ConnectionManager) Add(conn *Connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if conn == nil || (conn.manager != nil && conn.manager != m) || conn.IsClosed() || m.closed.Load() ||
		(m.maxConns > 0 && len(m.connections)+len(m.pending) >= m.maxConns) {
		return false
	}
	if conn.ctx == nil {
		conn.manager = m
		conn.ctx, conn.cancel = context.WithCancel(context.Background())
		conn.readGate, conn.writeGate = make(chan struct{}, 1), make(chan struct{}, 1)
		conn.closeDone = make(chan struct{})
		conn.touch()
	}
	return m.addLocked(conn)
}

func (m *ConnectionManager) addReserved(conn *Connection, reservation *upgradeReservation) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return false
	}
	if !m.addLocked(conn) {
		return false
	}
	delete(m.pending, reservation) // transfer the reserved capacity to the connection
	return true
}

func (m *ConnectionManager) addLocked(conn *Connection) bool {
	if _, exists := m.connections[conn.id]; exists {
		return false
	}
	m.connections[conn.id] = conn
	m.startCleanupLocked()
	return true
}

func (m *ConnectionManager) Remove(id string) {
	m.mu.Lock()
	m.removeLocked(id)
	m.mu.Unlock()
}

func (m *ConnectionManager) removeLocked(id string) {
	delete(m.connections, id)
	if len(m.connections) == 0 && m.cleanupStop != nil {
		m.cleanupStop()
		m.cleanupStop = nil
	}
}

func (m *ConnectionManager) removeClosed(conn *Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connections[conn.id] == conn {
		m.removeLocked(conn.id)
	}
	// A shutdown snapshot can only omit this connection after close completion
	// has been published. This also covers concurrent user-initiated Close calls.
	close(conn.closeDone)
}

func (m *ConnectionManager) Get(id string) (*Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, ok := m.connections[id]
	return conn, ok
}

func (m *ConnectionManager) GetAll() []*Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conns := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		conns = append(conns, conn)
	}
	return conns
}

func (m *ConnectionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}

// Broadcast retains the original API. Use BroadcastContext for delivery errors
// or a different total deadline.
func (m *ConnectionManager) Broadcast(value interface{}) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultOperationTimeout)
	defer cancel()
	_ = m.BroadcastContext(ctx, value)
}

func (m *ConnectionManager) BroadcastText(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultOperationTimeout)
	defer cancel()
	_ = m.BroadcastTextContext(ctx, text)
}

func (m *ConnectionManager) BroadcastBinary(data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultOperationTimeout)
	defer cancel()
	_ = m.BroadcastBinaryContext(ctx, data)
}

func (m *ConnectionManager) BroadcastContext(ctx context.Context, value interface{}) error {
	return m.broadcast(ctx, coderws.MessageText, func() ([]byte, error) { return json.Marshal(value) })
}

func (m *ConnectionManager) BroadcastTextContext(ctx context.Context, text string) error {
	return m.broadcast(ctx, coderws.MessageText, func() ([]byte, error) { return []byte(text), nil })
}

// BroadcastBinaryContext borrows data until it returns; do not mutate it during
// the call. No per-client copy or unbounded background queue is created.
func (m *ConnectionManager) BroadcastBinaryContext(ctx context.Context, data []byte) error {
	return m.broadcast(ctx, coderws.MessageBinary, func() ([]byte, error) { return data, nil })
}

func (m *ConnectionManager) broadcast(parent context.Context, kind coderws.MessageType, payload func() ([]byte, error)) error {
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	m.broadcasts.Add(1)
	m.mu.Unlock()
	defer m.broadcasts.Done()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(m.ctx, cancel)
	defer func() { stop(); cancel() }()
	// Serialize batches to bound the global worker count and preserve ordering.
	// Waiting callers apply backpressure and their total deadline still applies.
	select {
	case m.broadcastGate <- struct{}{}:
		defer func() { <-m.broadcastGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Encode only after admission: queued batches do not retain encoded copies.
	data, err := payload()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.broadcastWorkers.run(m.GetAll(), func(conn *Connection) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return conn.send(ctx, kind, data)
	})
}

func parallelConnections(conns []*Connection, fn func(*Connection) error) error {
	if len(conns) == 1 {
		return fn(conns[0])
	}
	var next atomic.Int64
	var workers sync.WaitGroup
	var first sync.Once
	var result error
	for i := 0; i < min(managerWorkers, len(conns)); i++ {
		workers.Go(func() {
			for {
				index := int(next.Add(1) - 1)
				if index >= len(conns) {
					return
				}
				if err := fn(conns[index]); err != nil {
					first.Do(func() { result = err })
				}
			}
		})
	}
	workers.Wait()
	return result
}

// CloseAll keeps the existing permanent-shutdown behavior.
func (m *ConnectionManager) CloseAll() { m.Shutdown(context.Background()) }

func (m *ConnectionManager) startCleanupLocked() {
	if m.closed.Load() || m.cleanupStop != nil || len(m.connections) == 0 || m.timeout <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cleanupStop = cancel
	timeout := secondsToDuration(m.timeout)
	m.cleanup.Go(func() { m.cleanupConnections(ctx, timeout) })
}

func (m *ConnectionManager) cleanupConnections(ctx context.Context, timeout time.Duration) {
	interval := min(time.Minute, max(100*time.Millisecond, timeout/2))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			var expired []*Connection
			for _, conn := range m.GetAll() {
				if now.Sub(time.Unix(0, conn.lastActivity.Load())) >= timeout {
					expired = append(expired, conn)
				}
			}
			closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = parallelConnections(expired, func(conn *Connection) error {
				if ctx.Err() != nil || time.Since(time.Unix(0, conn.lastActivity.Load())) < timeout {
					return nil
				}
				return conn.CloseWithContext(closeCtx)
			})
			cancel()
		}
	}
}

// ShutdownWithContext permanently stops admission, aborts in-flight handshakes,
// and waits for connections, broadcasts and idle scanners. At the deadline all
// owned transports are forcibly closed before returning.
func (m *ConnectionManager) ShutdownWithContext(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed.Load() {
		m.closed.Store(true)
		m.cancel()
		go m.shutdown(ctx)
	}
	m.mu.Unlock()
	select {
	case <-m.shutdownDone:
		if err := ctx.Err(); err != nil {
			return err
		}
		return m.shutdownErr
	case <-ctx.Done():
		m.forceCloseAll()
		<-m.shutdownDone
		return ctx.Err()
	}
}

func (m *ConnectionManager) shutdown(ctx context.Context) {
	m.shutdownErr = parallelConnections(m.GetAll(), func(conn *Connection) error { return conn.CloseWithContext(ctx) })
	m.upgrades.Wait()
	m.broadcasts.Wait()
	m.broadcastWorkers.close()
	m.cleanup.Wait()
	close(m.shutdownDone)
}

func (m *ConnectionManager) forceCloseAll() {
	for _, conn := range m.GetAll() {
		conn.forceTransportClose()
	}
}

// Shutdown retains the original five-second upper bound and caller context API.
func (m *ConnectionManager) Shutdown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = m.ShutdownWithContext(ctx)
}

// SetTimeout sets the idle timeout in seconds; <= 0 disables idle eviction.
// Successfully received ping/pong frames also count as activity.
func (m *ConnectionManager) SetTimeout(timeout int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeout = timeout
	if m.cleanupStop != nil {
		m.cleanupStop()
		m.cleanupStop = nil
	}
	m.startCleanupLocked()
}

func (m *ConnectionManager) SetMaxConns(maxConns int) {
	m.mu.Lock()
	m.maxConns = maxConns
	m.mu.Unlock()
}

func (m *ConnectionManager) GetTimeout() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.timeout
}

func (m *ConnectionManager) GetMaxConns() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxConns
}

type upgradeReservation struct {
	manager *ConnectionManager
	mu      sync.Mutex
	conn    net.Conn
	stop    func() bool
}

func (m *ConnectionManager) reserve() (*upgradeReservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return nil, ErrManagerClosed
	}
	if m.maxConns > 0 && len(m.connections)+len(m.pending) >= m.maxConns {
		return nil, ErrMaxConnections
	}
	r := &upgradeReservation{manager: m}
	r.stop = context.AfterFunc(m.ctx, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.conn != nil {
			r.conn.Close()
		}
	})
	m.pending[r] = struct{}{}
	m.upgrades.Add(1)
	return r, nil
}

func (r *upgradeReservation) attach(conn net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conn = conn
	if r.manager.ctx.Err() != nil {
		conn.Close()
	}
}

func (r *upgradeReservation) release() {
	r.stop()
	r.mu.Lock()
	r.conn = nil
	r.mu.Unlock()
	r.manager.mu.Lock()
	delete(r.manager.pending, r)
	r.manager.mu.Unlock()
	r.manager.upgrades.Done()
}

var (
	ErrManagerClosed  = errors.New("websocket: connection manager is shut down")
	ErrMaxConnections = errors.New("websocket: max connections reached")
)
