package websocket

import (
	"context"
	"errors"
	"io"
	"net"
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

	// 连接 context 继承 manager 的 context：manager Shutdown 时，
	// 正在阻塞的读写操作会随之取消，而不是拖住退出流程
	connCtx, cancel := context.WithCancel(w.manager.ctx)
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

	// Add 在同一把锁下检查 manager 关闭状态，杜绝 Shutdown 完成后仍接受新连接
	if !w.manager.Add(connection) {
		conn.CloseNow()
		return nil, errors.New("websocket: max connections reached or manager closed")
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
	closed       atomic.Bool  // 连接是否已关闭，原子操作避免每消息加锁
	lastActivity atomic.Int64 // 最近活动时间戳（UnixNano），原子更新避免每消息加锁
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

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	err := wsjson.Write(ctx, c.conn, v)
	if isTerminalConnError(err) {
		c.closeAfterIOError()
	} else if err == nil {
		// 只在成功通信后记录活动时间，失败的广播不再刷新清理阈值
		c.lastActivity.Store(time.Now().UnixNano())
	}
	return err
}

// SendText 发送文本消息
func (c *Connection) SendText(text string) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	err := c.conn.Write(ctx, websocket.MessageText, []byte(text))
	if isTerminalConnError(err) {
		c.closeAfterIOError()
	} else if err == nil {
		c.lastActivity.Store(time.Now().UnixNano())
	}
	return err
}

// SendBinary 发送二进制消息
func (c *Connection) SendBinary(data []byte) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := c.operationContext(c.writeTimeout)
	defer cancel()
	err := c.conn.Write(ctx, websocket.MessageBinary, data)
	if isTerminalConnError(err) {
		c.closeAfterIOError()
	} else if err == nil {
		c.lastActivity.Store(time.Now().UnixNano())
	}
	return err
}

// Read 读取消息
func (c *Connection) Read(v interface{}) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	err := wsjson.Read(ctx, c.conn, v)
	if isTerminalConnError(err) {
		c.closeAfterIOError()
	} else if err == nil {
		c.lastActivity.Store(time.Now().UnixNano())
	}
	return err
}

// ReadText 读取文本消息
func (c *Connection) ReadText() (string, error) {
	if c.closed.Load() {
		return "", ErrConnectionClosed
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if isTerminalConnError(err) {
		c.closeAfterIOError()
		return "", err
	}
	if err != nil {
		return "", err
	}
	c.lastActivity.Store(time.Now().UnixNano())
	return string(data), nil
}

// ReadBinary 读取二进制消息
func (c *Connection) ReadBinary() ([]byte, error) {
	if c.closed.Load() {
		return nil, ErrConnectionClosed
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	ctx, cancel := c.operationContext(c.readTimeout)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if isTerminalConnError(err) {
		c.closeAfterIOError()
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	c.lastActivity.Store(time.Now().UnixNano())
	return data, nil
}

// Close 关闭连接（执行关闭握手）
func (c *Connection) Close() error {
	if c.closed.Swap(true) {
		return nil
	}

	c.cancel()
	// 尝试从管理器中移除连接，但不返回错误
	// 因为连接可能已经被清理协程移除
	c.manager.Remove(c.id)
	if c.conn == nil {
		return nil
	}
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// CloseNow 立即强制关闭连接，不执行关闭握手。
// 用于退出期限内强制回收：随后对同一连接调用 Close 是幂等的
func (c *Connection) CloseNow() {
	if c.closed.Swap(true) {
		return
	}
	c.cancel()
	c.manager.Remove(c.id)
	if c.conn != nil {
		c.conn.CloseNow()
	}
}

// closeAfterIOError 在读写遇到终止性错误时幂等清理连接：
// 标记关闭、取消操作 context、移出管理器并强制回收底层连接。
// 若调用方没有显式调用 Close，断开的连接不再占用连接名额
func (c *Connection) closeAfterIOError() {
	if c.closed.Swap(true) {
		return
	}
	c.cancel()
	c.manager.Remove(c.id)
	if c.conn != nil {
		c.conn.CloseNow()
	}
}

// isTerminalConnError 判断错误是否表示连接已失效（对端关闭、网络错误等）。
// 读写超时与 JSON 编解码错误不视为连接失效，避免误杀可用连接
func isTerminalConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return false
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
	closed      atomic.Bool // 是否已关闭（Shutdown 后拒绝新连接）
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

// Add 添加连接。
// 在同一把锁下检查关闭状态与容量上限，与 Shutdown 协调：
// 关闭状态一旦设置，后续 Add 全部失败，杜绝关闭后仍接受新连接
func (m *ConnectionManager) Add(conn *Connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed.Load() {
		return false
	}
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

// CloseAll 关闭所有连接。
// 为兼容旧的调用方式：无外部期限时使用统一的内部关闭预算，
// 到期强制回收底层连接，避免串行握手拖住退出
func (m *ConnectionManager) CloseAll() {
	m.Shutdown(context.Background())
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

// Shutdown 关闭连接管理器。
//
// 在同一把锁下设置关闭状态、取消清理协程并取得待关闭快照，之后拒绝
// 新的 Add；以有限并发执行关闭握手，并在统一期限内（5 秒或调用方
// context 更早的截止时间）用 CloseNow 强制回收剩余连接，确保退出不被
// 不回应关闭握手的对端拖住
func (m *ConnectionManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		return
	}
	m.closed.Store(true)
	m.cancel()
	conns := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		conns = append(conns, conn)
	}
	m.mu.Unlock()

	const closeHandshakeBudget = 5 * time.Second
	const closeWorkers = 64

	var wg sync.WaitGroup
	tasks := make(chan *Connection)
	for i := 0; i < closeWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for conn := range tasks {
				conn.Close()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	for _, conn := range conns {
		select {
		case tasks <- conn:
		case <-ctx.Done():
			// 外部取消：放弃派发，直接进入强制回收
			close(tasks)
			goto force
		}
	}
	close(tasks)

	select {
	case <-done:
		return
	case <-time.After(closeHandshakeBudget):
	case <-ctx.Done():
	}

force:
	// 期限内未完成的连接强制回收；已关闭的连接幂等跳过
	for _, conn := range conns {
		conn.CloseNow()
	}
	<-done
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
