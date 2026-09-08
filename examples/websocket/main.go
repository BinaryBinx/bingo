package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/BinaryBinx/bingo/core"
	"github.com/BinaryBinx/bingo/middleware"

	"github.com/fasthttp/websocket"
)

// ChatMessage 聊天消息结构
type ChatMessage struct {
	Type      string    `json:"type"`      // 消息类型: message, join, leave, system
	Username  string    `json:"username"`  // 用户名
	Message   string    `json:"message"`   // 消息内容
	Timestamp time.Time `json:"timestamp"` // 时间戳
	UserCount int       `json:"userCount"` // 当前用户数
	UserColor string    `json:"userColor"` // 用户颜色
}

// ChatRoom 聊天室管理器
type ChatRoom struct {
	app      *core.App
	upgrader websocket.FastHTTPUpgrader
	// users 以唯一用户名（连接计数）为键，避免同秒生成相同用户名互相覆盖
	users       map[string]*chatClient
	colors      map[string]string // 用户颜色映射
	mu          sync.RWMutex
	userSeq     atomic.Uint64
	closed      bool
	connections sync.WaitGroup
}

// NewChatRoom 创建新的聊天室
func NewChatRoom(app *core.App) *ChatRoom {
	room := &ChatRoom{
		app:    app,
		users:  make(map[string]*chatClient),
		colors: make(map[string]string),
		// fasthttp 原生升级器：在回调中交付升级完成的连接，全程可运行
		upgrader: websocket.FastHTTPUpgrader{
			EnableCompression: true,
			// nil CheckOrigin uses the upgrader's same-origin policy.
		},
	}
	if err := app.OnStopping(room.Shutdown); err != nil {
		room.closed = true
	}
	return room
}

// 预定义的用户颜色
var userColors = []string{
	"#FF6B6B", // 红色
	"#4ECDC4", // 青色
	"#45B7D1", // 蓝色
	"#96CEB4", // 绿色
	"#FFEAA7", // 黄色
	"#DDA0DD", // 紫色
	"#98D8C8", // 薄荷绿
	"#F7DC6F", // 金黄色
	"#BB8FCE", // 淡紫色
	"#85C1E9", // 天蓝色
	"#F8C471", // 橙色
	"#82E0AA", // 浅绿色
	"#F1948A", // 粉红色
}

// assignUserColor 为用户分配颜色
func (cr *ChatRoom) assignUserColor(username string) string {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	if color, exists := cr.colors[username]; exists {
		return color
	}

	colorIndex := len(cr.colors) % len(userColors)
	color := userColors[colorIndex]
	cr.colors[username] = color

	return color
}

// getUserColor 获取用户颜色
func (cr *ChatRoom) getUserColor(username string) string {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	if color, exists := cr.colors[username]; exists {
		return color
	}
	return "#666666" // 默认颜色
}

// HandleWebSocket 处理WebSocket连接的升级与消息循环
func (cr *ChatRoom) HandleWebSocket(ctx *core.RequestContext) {
	// fasthttp's hijack wrapper ignores Close until its callback returns. Keep
	// the actual socket so shutdown can unblock the callback's pending Read.
	transport := ctx.Conn()
	err := cr.upgrader.Upgrade(ctx.RequestCtx, func(conn *websocket.Conn) { cr.handleConn(conn, transport) })
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
	}
}

// handleConn 在 hijacked 连接的 goroutine 中运行单个连接的消息循环
func (cr *ChatRoom) handleConn(raw *websocket.Conn, transport net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WebSocket handler panic: %v", r)
			raw.Close()
		}
	}()
	// 防止超限消息占用内存
	raw.SetReadLimit(64 * 1024)

	// 使用连接计数生成唯一用户名，避免同秒重复
	username := fmt.Sprintf("用户%d", cr.userSeq.Add(1))
	cr.mu.Lock()
	if cr.closed || len(cr.users) >= 1000 {
		cr.mu.Unlock()
		raw.Close()
		return
	}
	conn := newChatClient(raw, transport)
	userColor := userColors[len(cr.colors)%len(userColors)]
	cr.colors[username] = userColor
	cr.connections.Add(1)
	cr.users[username] = conn
	userCount := len(cr.users)
	cr.mu.Unlock()

	defer func() {
		defer cr.connections.Done()
		// 成功升级后必须显式关闭连接，释放资源
		conn.Close()
		<-conn.finished

		// 移除用户并清理其颜色记录
		cr.mu.Lock()
		if _, ok := cr.users[username]; ok {
			delete(cr.users, username)
			delete(cr.colors, username)
		}
		userCount = len(cr.users)
		cr.mu.Unlock()

		leaveMsg := ChatMessage{
			Type:      "leave",
			Username:  username,
			Message:   "离开了聊天室",
			Timestamp: time.Now(),
			UserCount: userCount,
			UserColor: userColor,
		}
		cr.broadcastMessage(leaveMsg, conn)
		log.Printf("用户 %s 离开聊天室，当前用户数: %d", username, userCount)
	}()

	// 发送欢迎消息
	welcomeMsg := ChatMessage{
		Type:      "join",
		Username:  username,
		Message:   "加入了聊天室",
		Timestamp: time.Now(),
		UserCount: userCount,
		UserColor: userColor,
	}
	cr.broadcastMessage(welcomeMsg, nil)

	// 发送系统消息
	systemMsg := ChatMessage{
		Type:      "system",
		Username:  "系统",
		Message:   "欢迎来到Bingo聊天室！输入 /help 查看帮助信息。",
		Timestamp: time.Now(),
		UserCount: userCount,
		UserColor: "#666666",
	}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshalJS(systemMsg))

	log.Printf("用户 %s 加入聊天室，当前用户数: %d", username, userCount)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// 连接断开（对端关闭、网络错误），结束消息循环
			return
		}

		var msg ChatMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		if msg.Type == "ping" {
			if err := conn.WriteMessage(websocket.TextMessage, mustMarshalJS(ChatMessage{Type: "pong", Timestamp: time.Now()})); err != nil {
				return
			}
			continue
		}
		if msg.Type != "message" {
			continue
		}
		msg.Message = strings.TrimSpace(msg.Message)
		if msg.Message == "" || utf8.RuneCountInString(msg.Message) > 500 {
			continue
		}
		// 设置消息属性
		msg.Username = username
		msg.Timestamp = time.Now()
		msg.UserColor = userColor

		cr.mu.RLock()
		msg.UserCount = len(cr.users)
		cr.mu.RUnlock()

		// 处理特殊命令
		if msg.Type == "message" && len(msg.Message) > 0 && msg.Message[0] == '/' {
			cr.handleCommand(username, msg.Message)
			continue
		}

		// 广播消息（不回送给发送者自己）
		cr.broadcastMessage(msg, conn)
		log.Printf("[%s] %s: %s", msg.Timestamp.Format("15:04:05"), username, msg.Message)
	}
}

// handleCommand 处理特殊命令
func (cr *ChatRoom) handleCommand(username, command string) {
	cr.mu.RLock()
	conn, exists := cr.users[username]
	userCount := len(cr.users)
	cr.mu.RUnlock()

	if !exists {
		return
	}

	switch command {
	case "/help":
		helpMsg := ChatMessage{
			Type:      "system",
			Username:  "系统",
			Message:   "可用命令: /help - 显示帮助, /users - 显示在线用户, /time - 显示当前时间",
			Timestamp: time.Now(),
			UserCount: userCount,
			UserColor: "#666666",
		}
		_ = conn.WriteMessage(websocket.TextMessage, mustMarshalJS(helpMsg))
	case "/users":
		userList, userCount := cr.userList()
		userMsg := ChatMessage{
			Type:      "system",
			Username:  "系统",
			Message:   userList,
			Timestamp: time.Now(),
			UserCount: userCount,
			UserColor: "#666666",
		}
		_ = conn.WriteMessage(websocket.TextMessage, mustMarshalJS(userMsg))
	case "/time":
		timeMsg := ChatMessage{
			Type:      "system",
			Username:  "系统",
			Message:   "当前时间: " + time.Now().Format("2006-01-02 15:04:05"),
			Timestamp: time.Now(),
			UserCount: userCount,
			UserColor: "#666666",
		}
		_ = conn.WriteMessage(websocket.TextMessage, mustMarshalJS(timeMsg))
	}
}

// userList 只在锁内复制用户名，拼接在锁外完成，避免大聊天室的列表查询
// 长时间阻塞用户加入和退出。返回的人数与列表来自同一次快照。
func (cr *ChatRoom) userList() (string, int) {
	cr.mu.RLock()
	users := make([]string, 0, len(cr.users))
	for user := range cr.users {
		users = append(users, user)
	}
	cr.mu.RUnlock()

	const prefix = "在线用户: "
	size := len(prefix) + max(0, len(users)-1)*len(", ")
	for _, user := range users {
		size += len(user)
	}
	var list strings.Builder
	list.Grow(size)
	list.WriteString(prefix)
	for index, user := range users {
		if index > 0 {
			list.WriteString(", ")
		}
		list.WriteString(user)
	}
	return list.String(), len(users)
}

// broadcastMessage 广播消息：锁内只取连接快照，锁外执行网络发送，
// 避免慢连接持锁阻塞加入/退出；JSON 预编码一次，重复使用只读字节
func (cr *ChatRoom) broadcastMessage(msg ChatMessage, skip *chatClient) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	cr.mu.RLock()
	conns := make([]*chatClient, 0, len(cr.users))
	for _, c := range cr.users {
		conns = append(conns, c)
	}
	cr.mu.RUnlock()

	for _, c := range conns {
		if c == skip {
			continue
		}
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			// 发送失败说明连接已失效，幂等移除并关闭
			cr.removeConn(c)
		}
	}
}

// removeConn 幂等移除连接并关闭
func (cr *ChatRoom) removeConn(conn *chatClient) {
	cr.mu.Lock()
	for name, c := range cr.users {
		if c == conn {
			delete(cr.users, name)
			delete(cr.colors, name)
			break
		}
	}
	cr.mu.Unlock()
	_ = conn.Close()
}

// mustMarshalJS 序列化聊天消息（配置固定，失败视为编程错误直接 panic）
func mustMarshalJS(msg ChatMessage) []byte {
	data, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	return data
}

func main() {
	// 基于 DefaultConfig 覆盖，避免手工字面量缺失超时等默认值
	app := core.NewApp(nil)
	app.SetRunMode(core.RunModeRelease)

	// 创建聊天室
	chatRoom := NewChatRoom(app)

	// 添加中间件
	app.Use(middleware.CORS([]string{"*"}, []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}, []string{"Content-Type", "Authorization"}))
	app.Use(middleware.Recovery())
	app.Use(middleware.RequestID())

	// 注册路由
	app.GET("/", func(ctx *core.RequestContext) {
		ctx.HTML(200, chatRoomHTML)
	})

	app.GET("/ws", func(ctx *core.RequestContext) {
		chatRoom.HandleWebSocket(ctx)
	})

	// 启动服务器
	log.Println("🚀 Bingo聊天室应用启动在 http://localhost:8080")
	log.Println("💬 打开浏览器访问 http://localhost:8080 开始聊天")
	log.Println("🎨 使用Bingo框架实现，支持用户颜色系统")

	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}

// 美观的HTML界面
const chatRoomHTML = `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Bingo 聊天室</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }

        .chat-container {
            width: 90%;
            max-width: 800px;
            height: 80vh;
            background: white;
            border-radius: 20px;
            box-shadow: 0 20px 40px rgba(0,0,0,0.1);
            display: flex;
            flex-direction: column;
            overflow: hidden;
        }

        .chat-header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 20px;
            text-align: center;
            position: relative;
        }

        .chat-header h1 {
            font-size: 24px;
            margin-bottom: 5px;
        }

        .user-count {
            font-size: 14px;
            opacity: 0.9;
        }

        .status-indicator {
            position: absolute;
            top: 20px;
            right: 20px;
            width: 12px;
            height: 12px;
            border-radius: 50%;
            background: #ff4757;
            animation: pulse 2s infinite;
        }

        .status-indicator.connected {
            background: #2ed573;
        }

        @keyframes pulse {
            0% { opacity: 1; }
            50% { opacity: 0.5; }
            100% { opacity: 1; }
        }

        .chat-messages {
            flex: 1;
            padding: 20px;
            overflow-y: auto;
            background: #f8f9fa;
        }

        .message {
            margin-bottom: 15px;
            animation: fadeIn 0.3s ease-in;
        }

        @keyframes fadeIn {
            from { opacity: 0; transform: translateY(10px); }
            to { opacity: 1; transform: translateY(0); }
        }

        .message-content {
            display: inline-block;
            max-width: 70%;
            padding: 12px 16px;
            border-radius: 18px;
            word-wrap: break-word;
            position: relative;
        }

        .message.own .message-content {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            margin-left: auto;
            display: block;
        }

        .message.other .message-content {
            background: white;
            color: #333;
            border: 1px solid #e9ecef;
        }

        .username {
            display: inline-block;
            padding: 2px 8px;
            border-radius: 12px;
            color: white;
            font-weight: bold;
            margin-right: 8px;
            font-size: 12px;
        }

        .message.system .message-content {
            background: #ffa502;
            color: white;
            text-align: center;
            max-width: 100%;
            border-radius: 10px;
        }

        .message.join .message-content {
            background: #2ed573;
            color: white;
            text-align: center;
            max-width: 100%;
            border-radius: 10px;
        }

        .message.leave .message-content {
            background: #ff4757;
            color: white;
            text-align: center;
            max-width: 100%;
            border-radius: 10px;
        }

        .message-info {
            font-size: 12px;
            margin-top: 5px;
            opacity: 0.7;
        }

        .message.own .message-info {
            text-align: right;
        }

        .chat-input {
            padding: 20px;
            background: white;
            border-top: 1px solid #e9ecef;
            display: flex;
            gap: 10px;
        }

        .message-input {
            flex: 1;
            padding: 12px 16px;
            border: 2px solid #e9ecef;
            border-radius: 25px;
            font-size: 14px;
            outline: none;
            transition: border-color 0.3s;
        }

        .message-input:focus {
            border-color: #667eea;
        }

        .send-button {
            padding: 12px 24px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            border: none;
            border-radius: 25px;
            cursor: pointer;
            font-size: 14px;
            font-weight: 600;
            transition: transform 0.2s;
        }

        .send-button:hover {
            transform: translateY(-2px);
        }

        .send-button:disabled {
            opacity: 0.6;
            cursor: not-allowed;
            transform: none;
        }

        @media (max-width: 768px) {
            .chat-container {
                width: 95%;
                height: 90vh;
            }

            .message-content {
                max-width: 85%;
            }
        }
    </style>
</head>
<body>
    <div class="chat-container">
        <div class="chat-header">
            <div class="status-indicator" id="statusIndicator"></div>
            <h1>💬 Bingo 聊天室</h1>
            <div class="user-count">在线用户: <span id="userCount">0</span></div>
        </div>

        <div class="chat-messages" id="chatMessages">
            <div class="message system">
                <div class="message-content">
                    欢迎来到Bingo聊天室！正在连接...
                </div>
            </div>
        </div>

        <div class="chat-input">
            <input type="text" class="message-input" id="messageInput" placeholder="输入消息..." maxlength="500">
            <button class="send-button" id="sendButton">发送</button>
        </div>
    </div>

    <script>
        let ws = null;
        let isConnected = false;
        let reconnectAttempts = 0;
        const maxReconnectAttempts = 5;
        // 限制聊天记录条数，避免长时间使用 DOM 无限增长
        const maxMessages = 200;
        let heartbeatTimer = null;
        let reconnectTimer = null;
        let leavingPage = false;

        // DOM元素
        const chatMessages = document.getElementById('chatMessages');
        const messageInput = document.getElementById('messageInput');
        const sendButton = document.getElementById('sendButton');
        const statusIndicator = document.getElementById('statusIndicator');
        const userCount = document.getElementById('userCount');

        // 连接WebSocket
        function connectWebSocket() {
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const wsUrl = protocol + '//' + window.location.host + '/ws';

            ws = new WebSocket(wsUrl);

            ws.onopen = function() {
                isConnected = true;
                reconnectAttempts = 0;
                statusIndicator.classList.add('connected');
                addSystemMessage('连接成功！');
                clearInterval(heartbeatTimer);
                heartbeatTimer = setInterval(function() {
                    if (ws && ws.readyState === WebSocket.OPEN && ws.bufferedAmount <= 65536) ws.send(JSON.stringify({type: 'ping'}));
                }, 20000);
            };

            ws.onmessage = function(event) {
                let message;
                try { message = JSON.parse(event.data); } catch { return; }
                if (!message || message.type === 'pong') return;
                displayMessage(message);
            };

            ws.onclose = function() {
                clearInterval(heartbeatTimer);
                isConnected = false;
                statusIndicator.classList.remove('connected');
                if (leavingPage) return;
                addSystemMessage('连接断开，正在重连...');

                if (reconnectAttempts < maxReconnectAttempts) {
                    reconnectAttempts++;
                    reconnectTimer = setTimeout(connectWebSocket, 2000);
                } else {
                    addSystemMessage('重连失败，请刷新页面重试。');
                }
            };

            ws.onerror = function(error) {
                console.error('WebSocket错误:', error);
                addSystemMessage('连接错误，请检查网络。');
            };
        }

        // 创建消息元素（全部使用 DOM API / textContent，杜绝 innerHTML 注入 XSS）
        function createMessageDom(message) {
            const messageDiv = document.createElement('div');
            messageDiv.className = 'message ' + (message.type || 'message');

            const contentDiv = document.createElement('div');
            contentDiv.className = 'message-content';

            if (message.type === 'message') {
                if (message.username) {
                    const usernameSpan = document.createElement('span');
                    usernameSpan.className = 'username';
                    usernameSpan.style.backgroundColor = message.userColor || '#666666';
                    usernameSpan.textContent = message.username;
                    contentDiv.appendChild(usernameSpan);
                }
                contentDiv.appendChild(document.createTextNode(message.message || ''));
            } else if (message.type === 'join' || message.type === 'leave') {
                contentDiv.textContent = (message.type === 'join' ? '👋 ' : '👋 ') + (message.username || '') + ' ' + (message.message || '');
            } else {
                contentDiv.textContent = message.message || '';
            }

            messageDiv.appendChild(contentDiv);

            const infoDiv = document.createElement('div');
            infoDiv.className = 'message-info';
            infoDiv.textContent = formatTime(message.timestamp);
            messageDiv.appendChild(infoDiv);

            return messageDiv;
        }

        // 显示消息
        function displayMessage(message) {
            chatMessages.appendChild(createMessageDom(message));
            trimMessages();
            scrollToBottom();

            // 更新用户数
            if (message.userCount !== undefined) {
                userCount.textContent = message.userCount;
            }
        }

        // 限制消息条数
        function trimMessages() {
            while (chatMessages.childElementCount > maxMessages) {
                chatMessages.removeChild(chatMessages.firstElementChild);
            }
        }

        // 添加系统消息
        function addSystemMessage(text) {
            const messageDiv = document.createElement('div');
            messageDiv.className = 'message system';

            const contentDiv = document.createElement('div');
            contentDiv.className = 'message-content';
            contentDiv.textContent = text;

            messageDiv.appendChild(contentDiv);
            chatMessages.appendChild(messageDiv);
            trimMessages();
            scrollToBottom();
        }

        // 发送消息
        function sendMessage() {
            const message = messageInput.value.trim();
            if (!message || !isConnected || !ws || ws.readyState !== WebSocket.OPEN) return;
            if (ws.bufferedAmount > 65536) { addSystemMessage('网络繁忙，请稍后再发送。'); return; }

            const messageData = {
                type: 'message',
                message: message
            };

            ws.send(JSON.stringify(messageData));
            messageInput.value = '';
        }

        // 滚动到底部
        function scrollToBottom() {
            chatMessages.scrollTop = chatMessages.scrollHeight;
        }

        // 格式化时间
        function formatTime(timestamp) {
            const date = new Date(timestamp);
            return date.toLocaleTimeString('zh-CN', {
                hour: '2-digit',
                minute: '2-digit'
            });
        }

        // 事件监听
        sendButton.addEventListener('click', sendMessage);

        messageInput.addEventListener('keypress', function(e) {
            if (e.key === 'Enter') {
                sendMessage();
            }
        });

        // 连接WebSocket
        connectWebSocket();

        // 页面卸载时关闭连接
        window.addEventListener('beforeunload', function() {
            leavingPage = true;
            clearInterval(heartbeatTimer);
            clearTimeout(reconnectTimer);
            if (ws) {
                ws.close();
            }
        });
    </script>
</body>
</html>
`
