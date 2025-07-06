package websocket

import (
	"context"
	"log"
	"net/http"
	"sync"

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
	// 设置WebSocket选项
	options := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	}

	if w.config.EnableCompression {
		options.CompressionMode = websocket.CompressionContextTakeover
	}

	// 升级连接
	conn, err := websocket.Accept(writer, request, options)
	if err != nil {
		return nil, err
	}

	// 设置连接选项
	conn.SetReadLimit(w.config.MaxMessageSize)

	// 创建连接包装器
	connection := &Connection{
		conn:    conn,
		manager: w.manager,
		id:      generateID(),
		ctx:     request.Context(),
	}

	// 添加到连接管理器
	w.manager.Add(connection)

	return connection, nil
}

// GetManager 获取连接管理器
func (w *WebSocketUpgrader) GetManager() *ConnectionManager {
	return w.manager
}

// Connection WebSocket连接包装器
type Connection struct {
	conn    *websocket.Conn
	manager *ConnectionManager
	id      string
	ctx     context.Context
	mu      sync.RWMutex
	closed  bool
}

// ID 获取连接ID
func (c *Connection) ID() string {
	return c.id
}

// Send 发送消息
func (c *Connection) Send(v interface{}) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return ErrConnectionClosed
	}

	return wsjson.Write(c.ctx, c.conn, v)
}

// SendText 发送文本消息
func (c *Connection) SendText(text string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return ErrConnectionClosed
	}

	return c.conn.Write(c.ctx, websocket.MessageText, []byte(text))
}

// SendBinary 发送二进制消息
func (c *Connection) SendBinary(data []byte) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return ErrConnectionClosed
	}

	return c.conn.Write(c.ctx, websocket.MessageBinary, data)
}

// Read 读取消息
func (c *Connection) Read(v interface{}) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return ErrConnectionClosed
	}

	return wsjson.Read(c.ctx, c.conn, v)
}

// ReadText 读取文本消息
func (c *Connection) ReadText() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return "", ErrConnectionClosed
	}

	_, data, err := c.conn.Read(c.ctx)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// ReadBinary 读取二进制消息
func (c *Connection) ReadBinary() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return nil, ErrConnectionClosed
	}

	_, data, err := c.conn.Read(c.ctx)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// Close 关闭连接
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	c.manager.Remove(c.id)
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// IsClosed 检查连接是否已关闭
func (c *Connection) IsClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closed
}

// ConnectionManager 连接管理器
type ConnectionManager struct {
	connections map[string]*Connection
	mu          sync.RWMutex
}

// NewConnectionManager 创建新的连接管理器
func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		connections: make(map[string]*Connection),
	}
}

// Add 添加连接
func (m *ConnectionManager) Add(conn *Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections[conn.id] = conn
	log.Printf("WebSocket连接已添加: %s", conn.id)
}

// Remove 移除连接
func (m *ConnectionManager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.connections, id)
	log.Printf("WebSocket连接已移除: %s", id)
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
	connections := m.GetAll()
	for _, conn := range connections {
		if !conn.IsClosed() {
			conn.Send(v)
		}
	}
}

// BroadcastText 广播文本消息给所有连接
func (m *ConnectionManager) BroadcastText(text string) {
	connections := m.GetAll()
	for _, conn := range connections {
		if !conn.IsClosed() {
			conn.SendText(text)
		}
	}
}

// BroadcastBinary 广播二进制消息给所有连接
func (m *ConnectionManager) BroadcastBinary(data []byte) {
	connections := m.GetAll()
	for _, conn := range connections {
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
