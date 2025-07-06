package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/zjxubinbin/bingo/core"
	"github.com/zjxubinbin/bingo/middleware"
	"github.com/zjxubinbin/bingo/websocket"

	"github.com/valyala/fasthttp"
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
	app    *core.App
	users  map[string]*websocket.Connection
	colors map[string]string // 用户颜色映射
	mu     sync.RWMutex
}

// NewChatRoom 创建新的聊天室
func NewChatRoom(app *core.App) *ChatRoom {
	return &ChatRoom{
		app:    app,
		users:  make(map[string]*websocket.Connection),
		colors: make(map[string]string),
	}
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
	"#85C1E9", // 浅蓝色
	"#F7DC6F", // 金黄色
}

// assignUserColor 为用户分配颜色
func (cr *ChatRoom) assignUserColor(username string) string {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	// 如果用户已有颜色，返回现有颜色
	if color, exists := cr.colors[username]; exists {
		return color
	}

	// 根据用户数量分配颜色
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

// HandleWebSocket 处理WebSocket连接
func (cr *ChatRoom) HandleWebSocket(ctx *core.RequestContext) {
	// 创建fasthttp到标准http的适配器
	adapter := &fasthttpAdapter{
		ctx: ctx.RequestCtx,
	}

	// 升级到WebSocket连接
	conn, err := cr.app.GetWebSocketUpgrader().Upgrade(adapter, adapter.Request())
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		return
	}

	// 生成用户名
	username := generateUsername()

	// 为用户分配颜色
	userColor := cr.assignUserColor(username)

	// 添加到聊天室
	cr.mu.Lock()
	cr.users[username] = conn
	userCount := len(cr.users)
	cr.mu.Unlock()

	// 发送欢迎消息
	welcomeMsg := ChatMessage{
		Type:      "join",
		Username:  username,
		Message:   "加入了聊天室",
		Timestamp: time.Now(),
		UserCount: userCount,
		UserColor: userColor,
	}
	cr.broadcastMessage(welcomeMsg)

	// 发送系统消息
	systemMsg := ChatMessage{
		Type:      "system",
		Username:  "系统",
		Message:   "欢迎来到Bingo聊天室！输入 /help 查看帮助信息。",
		Timestamp: time.Now(),
		UserCount: userCount,
		UserColor: "#666666",
	}
	conn.Send(systemMsg)

	log.Printf("用户 %s 加入聊天室，当前用户数: %d", username, userCount)

	// 处理消息
	for {
		var msg ChatMessage
		if err := conn.Read(&msg); err != nil {
			break
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

		// 广播消息
		cr.broadcastMessage(msg)
		log.Printf("[%s] %s: %s", msg.Timestamp.Format("15:04:05"), username, msg.Message)
	}

	// 用户离开
	cr.mu.Lock()
	delete(cr.users, username)
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
	cr.broadcastMessage(leaveMsg)

	log.Printf("用户 %s 离开聊天室，当前用户数: %d", username, userCount)
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
		conn.Send(helpMsg)
	case "/users":
		cr.mu.RLock()
		userList := "在线用户: "
		for user := range cr.users {
			userList += user + ", "
		}
		cr.mu.RUnlock()
		if len(userList) > 10 {
			userList = userList[:len(userList)-2]
		}
		userMsg := ChatMessage{
			Type:      "system",
			Username:  "系统",
			Message:   userList,
			Timestamp: time.Now(),
			UserCount: userCount,
			UserColor: "#666666",
		}
		conn.Send(userMsg)
	case "/time":
		timeMsg := ChatMessage{
			Type:      "system",
			Username:  "系统",
			Message:   "当前时间: " + time.Now().Format("2006-01-02 15:04:05"),
			Timestamp: time.Now(),
			UserCount: userCount,
			UserColor: "#666666",
		}
		conn.Send(timeMsg)
	}
}

// broadcastMessage 广播消息
func (cr *ChatRoom) broadcastMessage(msg ChatMessage) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	for _, conn := range cr.users {
		conn.Send(msg)
	}
}

// fasthttpAdapter fasthttp到标准http的适配器
type fasthttpAdapter struct {
	ctx *fasthttp.RequestCtx
}

func (a *fasthttpAdapter) Header() http.Header {
	// 这里需要实现fasthttp到标准http的header转换
	// 简化实现，返回空header
	return make(http.Header)
}

func (a *fasthttpAdapter) Write(data []byte) (int, error) {
	a.ctx.SetBody(data)
	return len(data), nil
}

func (a *fasthttpAdapter) WriteHeader(statusCode int) {
	a.ctx.SetStatusCode(statusCode)
}

func (a *fasthttpAdapter) Request() *http.Request {
	// 这里需要实现fasthttp.Request到标准http.Request的转换
	// 简化实现，返回nil
	return nil
}

// generateUsername 生成用户名
func generateUsername() string {
	return "用户" + time.Now().Format("150405")
}

func main() {
	// 创建Bingo应用
	app := core.NewApp(nil)

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

        .typing-indicator {
            padding: 10px 20px;
            font-style: italic;
            color: #666;
            font-size: 12px;
        }

        .emoji-picker {
            position: absolute;
            bottom: 100%;
            right: 0;
            background: white;
            border: 1px solid #e9ecef;
            border-radius: 10px;
            padding: 10px;
            display: none;
            grid-template-columns: repeat(6, 1fr);
            gap: 5px;
            box-shadow: 0 5px 15px rgba(0,0,0,0.1);
        }

        .emoji {
            cursor: pointer;
            padding: 5px;
            border-radius: 5px;
            transition: background 0.2s;
        }

        .emoji:hover {
            background: #f8f9fa;
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
            };
            
            ws.onmessage = function(event) {
                const message = JSON.parse(event.data);
                displayMessage(message);
            };
            
            ws.onclose = function() {
                isConnected = false;
                statusIndicator.classList.remove('connected');
                addSystemMessage('连接断开，正在重连...');
                
                if (reconnectAttempts < maxReconnectAttempts) {
                    reconnectAttempts++;
                    setTimeout(connectWebSocket, 2000);
                } else {
                    addSystemMessage('重连失败，请刷新页面重试。');
                }
            };
            
            ws.onerror = function(error) {
                console.error('WebSocket错误:', error);
                addSystemMessage('连接错误，请检查网络。');
            };
        }

        // 显示消息
        function displayMessage(message) {
            const messageDiv = document.createElement('div');
            messageDiv.className = 'message ' + message.type;
            
            const contentDiv = document.createElement('div');
            contentDiv.className = 'message-content';
            
            let displayText = '';
            if (message.type === 'message') {
                const usernameSpan = '<span class="username" style="background-color: ' + message.userColor + '">' + message.username + '</span>';
                displayText = usernameSpan + message.message;
            } else if (message.type === 'join') {
                displayText = '👋 ' + message.username + ' ' + message.message;
            } else if (message.type === 'leave') {
                displayText = '👋 ' + message.username + ' ' + message.message;
            } else {
                displayText = message.message;
            }
            
            contentDiv.innerHTML = displayText;
            
            const infoDiv = document.createElement('div');
            infoDiv.className = 'message-info';
            infoDiv.textContent = formatTime(message.timestamp);
            
            messageDiv.appendChild(contentDiv);
            messageDiv.appendChild(infoDiv);
            
            chatMessages.appendChild(messageDiv);
            scrollToBottom();
            
            // 更新用户数
            if (message.userCount !== undefined) {
                userCount.textContent = message.userCount;
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
            scrollToBottom();
        }

        // 发送消息
        function sendMessage() {
            const message = messageInput.value.trim();
            if (!message || !isConnected) return;
            
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

        // 自动滚动
        chatMessages.addEventListener('scroll', function() {
            const isAtBottom = chatMessages.scrollTop + chatMessages.clientHeight >= chatMessages.scrollHeight - 10;
            if (isAtBottom) {
                // 用户正在查看最新消息，保持自动滚动
            }
        });

        // 连接WebSocket
        connectWebSocket();

        // 页面卸载时关闭连接
        window.addEventListener('beforeunload', function() {
            if (ws) {
                ws.close();
            }
        });
    </script>
</body>
</html>
`
