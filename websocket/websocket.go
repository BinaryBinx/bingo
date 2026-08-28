package websocket

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// WebSocketUpgrader WebSocket升级器
type WebSocketUpgrader struct {
	// WebSocket配置
	config *Config
	// 连接管理器
	manager *ConnectionManager
}

// Config WebSocket配置
type Config struct {
	// 是否启用压缩
	EnableCompression bool
	// 最大消息大小
	MaxMessageSize int64
	// 读取超时时间
	ReadTimeout int
	// 写入超时时间
	WriteTimeout int
	// 是否跳过Origin校验。仅在明确需要允许任意跨域WebSocket时开启
	InsecureSkipVerify bool
	// 允许的Origin主机或 scheme://host 模式
	OriginPatterns []string
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		EnableCompression: true,
		MaxMessageSize:    1024 * 1024, // 1MB
		ReadTimeout:       30,
		WriteTimeout:      30,
	}
}

// NewWebSocketUpgrader 创建新的WebSocket升级器
func NewWebSocketUpgrader(config *Config) *WebSocketUpgrader {
	if config == nil {
		config = DefaultConfig()
	}

	return &WebSocketUpgrader{
		config:  config,
		manager: NewConnectionManager(),
	}
}

// Upgrade 升级HTTP连接到WebSocket
func (w *WebSocketUpgrader) Upgrade(writer http.ResponseWriter, request *http.Request) (*Connection, error) {
	options := &websocket.AcceptOptions{
		InsecureSkipVerify: w.config.InsecureSkipVerify,
		OriginPatterns:     w.config.OriginPatterns,
		CompressionMode:    websocket.CompressionDisabled,
	}

	if w.config.EnableCompression {
		options.CompressionMode = websocket.CompressionNoContextTakeover
	}

	conn, err := websocket.Accept(writer, request, options)
	if err != nil {
		return nil, err
	}

	conn.SetReadLimit(w.config.MaxMessageSize)

	connCtx, cancel := context.WithCancel(context.Background())
	connection := &Connection{
		conn:         conn,
		manager:      w.manager,
		id:           generateID(),
		ctx:          connCtx,
		cancel:       cancel,
		readTimeout:  secondsToDuration(w.config.ReadTimeout),
		writeTimeout: secondsToDuration(w.config.WriteTimeout),
	}
	connection.lastActivity.Store(time.Now().UnixNano())

	if !w.manager.Add(connection) {
		conn.Close(websocket.StatusNormalClosure, "max connections reached")
		return nil, errors.New("websocket: max connections reached")
	}

	return connection, nil
}

// GetManager 获取连接管理器
func (w *WebSocketUpgrader) GetManager() *ConnectionManager {
	return w.manager
}

// Connection WebSocket连接包装器
type Connection struct {
	conn         *websocket.Conn
	manager      *ConnectionManager
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	readMu       sync.Mutex
	writeMu      sync.Mutex
	closed       atomic.Bool   // 连接是否已关闭，原子操作避免每消息加锁
	lastActivity atomic.Int64  // 最近活动时间戳（UnixNano），原子更新避免每消息加锁
	readTimeout  time.Duration
	writeTimeout time.Duration
}

// ID 获取连接ID
func (c *Connection) ID() string {
	return c.id
}

// Send 发送消息
func (c *Connection) Send(v interface{}) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	c.lastActivity.Store(time.Now().UnixNano())

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, c.conn, v)
}

// SendText 发送文本消息
func (c *Connection) SendText(text string) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	c.lastActivity.Store(time.Now().UnixNano())

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, []byte(text))
}

// SendBinary 发送二进制消息
func (c *Connection) SendBinary(data []byte) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	c.lastActivity.Store(time.Now().UnixNano())

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageBinary, data)
}

// Read 读取消息
func (c *Connection) Read(v interface{}) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	c.lastActivity.Store(time.Now().UnixNano())

	c.readMu.Lock()
	defer c.readMu.Unlock()

	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	return wsjson.Read(ctx, c.conn, v)
}

// ReadText 读取文本消息
func (c *Connection) ReadText() (string, error) {
	if c.closed.Load() {
		return "", ErrConnectionClosed
	}
	c.lastActivity.Store(time.Now().UnixNano())

	c.readMu.Lock()
	defer c.readMu.Unlock()

	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// ReadBinary 读取二进制消息
func (c *Connection) ReadBinary() ([]byte, error) {
	if c.closed.Load() {
		return nil, ErrConnectionClosed
	}
	c.lastActivity.Store(time.Now().UnixNano())

	c.readMu.Lock()
	defer c.readMu.Unlock()

	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// Close 关闭连接
func (c *Connection) Close() error {
	if c.closed.Swap(true) {
		return nil
	}

	c.cancel()
	// 尝试从管理器中移除连接，但不返回错误
	// 因为连接可能已经被清理协程移除
	c.manager.Remove(c.id)
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// IsClosed 检查连接是否已关闭
func (c *Connection) IsClosed() bool {
	return c.closed.Load()
}

func (c *Connection) operationContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return c.ctx, func() {}
	}
	return context.WithTimeout(c.ctx, timeout)
}

func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// ConnectionManager 连接管理器
type ConnectionManager struct {
	connections map[string]*Connection
	mu          sync.RWMutex
	timeout     int
	maxConns    int
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewConnectionManager 创建新的连接管理器
func NewConnectionManager() *ConnectionManager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &ConnectionManager{
		connections: make(map[string]*Connection),
		timeout:     3600,
		maxConns:    1000,
		ctx:         ctx,
		cancel:      cancel,
	}

	go manager.cleanupConnections()

	return manager
}

// Add 添加连接
func (m *ConnectionManager) Add(conn *Connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.maxConns > 0 && len(m.connections) >= m.maxConns {
		return false
	}

	m.connections[conn.id] = conn
	return true
}

// Remove 移除连接
func (m *ConnectionManager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.connections, id)
}

// Get 获取连接
func (m *ConnectionManager) Get(id string) (*Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, exists := m.connections[id]
	return conn, exists
}

// GetAll 获取所有连接
func (m *ConnectionManager) GetAll() []*Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	connections := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		connections = append(connections, conn)
	}
	return connections
}

// Count 获取连接数量
func (m *ConnectionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}

// Broadcast 广播消息给所有连接
func (m *ConnectionManager) Broadcast(v interface{}) {
	m.mu.RLock()
	conns := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		conns = append(conns, conn)
	}
	m.mu.RUnlock()

	for _, conn := range conns {
		if !conn.IsClosed() {
			conn.Send(v)
		}
	}
}

// BroadcastText 广播文本消息给所有连接
func (m *ConnectionManager) BroadcastText(text string) {
	m.mu.RLock()
	conns := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		conns = append(conns, conn)
	}
	m.mu.RUnlock()

	for _, conn := range conns {
		if !conn.IsClosed() {
			conn.SendText(text)
		}
	}
}

// BroadcastBinary 广播二进制消息给所有连接
func (m *ConnectionManager) BroadcastBinary(data []byte) {
	m.mu.RLock()
	conns := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		conns = append(conns, conn)
	}
	m.mu.RUnlock()

	for _, conn := range conns {
		if !conn.IsClosed() {
			conn.SendBinary(data)
		}
	}
}

// CloseAll 关闭所有连接
func (m *ConnectionManager) CloseAll() {
	connections := m.GetAll()
	for _, conn := range connections {
		conn.Close()
	}
}

// cleanupConnections 定期清理超时的连接
func (m *ConnectionManager) cleanupConnections() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.mu.Lock()
			now := time.Now().UnixNano()
			timeoutNano := int64(time.Duration(m.timeout) * time.Second)

			var toClose []*Connection
			for id, conn := range m.connections {
				if conn.closed.Load() || now-conn.lastActivity.Load() > timeoutNano {
					delete(m.connections, id)
					toClose = append(toClose, conn)
				}
			}
			m.mu.Unlock()

			// 解锁后再同步关闭，避免大量连接超时时开启大量 goroutine；
			// Close 内部的 Remove 是幂等的，已从 map 删除的连接再次删除无害
			for _, conn := range toClose {
				conn.Close()
			}
		case <-m.ctx.Done():
			return
		}
	}
}

// Shutdown 关闭连接管理器
func (m *ConnectionManager) Shutdown() {
	m.cancel()
	m.CloseAll()
}

// SetTimeout 设置连接超时时间（秒）
func (m *ConnectionManager) SetTimeout(timeout int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeout = timeout
}

// SetMaxConns 设置最大连接数
func (m *ConnectionManager) SetMaxConns(maxConns int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxConns = maxConns
}

// GetTimeout 获取连接超时时间（秒）
func (m *ConnectionManager) GetTimeout() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.timeout
}

// GetMaxConns 获取最大连接数
func (m *ConnectionManager) GetMaxConns() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxConns
}
