// Package websocket integrates coder/websocket with net/http and fasthttp.
package websocket

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/BinaryBinx/bingo/internal/responsemeta"
	coderws "github.com/coder/websocket"
)

// Config WebSocket 配置。超时单位为秒；0 表示不设置该操作的期限。
type Config struct {
	EnableCompression  bool
	MaxMessageSize     int64
	ReadTimeout        int
	WriteTimeout       int
	InsecureSkipVerify bool
	OriginPatterns     []string
}

func DefaultConfig() *Config {
	return &Config{
		EnableCompression: true,
		MaxMessageSize:    1 << 20,

		ReadTimeout:  30,
		WriteTimeout: 30,
	}
}

type WebSocketUpgrader struct {
	config  Config
	manager *ConnectionManager
}

func NewWebSocketUpgrader(config *Config) *WebSocketUpgrader {
	if config == nil {
		config = DefaultConfig()
	}
	copyConfig := *config
	copyConfig.OriginPatterns = slices.Clone(config.OriginPatterns)
	if copyConfig.MaxMessageSize <= 0 {
		copyConfig.MaxMessageSize = DefaultConfig().MaxMessageSize
	}
	return &WebSocketUpgrader{config: copyConfig, manager: NewConnectionManager()}
}

func (w *WebSocketUpgrader) GetManager() *ConnectionManager { return w.manager }

// Upgrade upgrades a net/http connection. The caller owns the returned connection
// and must Close it. After a successful HTTP hijack, even a rejected handshake is
// written directly to the socket; do not write another HTTP response on error.
func (w *WebSocketUpgrader) Upgrade(writer http.ResponseWriter, request *http.Request) (*Connection, error) {
	if request == nil {
		writeUpgradeError(writer, "invalid WebSocket request", http.StatusBadRequest)
		return nil, ErrUpgradeFailed
	}
	reservation, err := w.manager.reserve()
	if err != nil {
		writeUpgradeError(writer, err.Error(), http.StatusServiceUnavailable)
		return nil, err
	}
	defer reservation.release()
	headers := writer.Header().Clone()
	raw, buffered, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		writeUpgradeError(writer, "WebSocket requires HTTP hijacking", http.StatusNotImplemented)
		return nil, fmt.Errorf("%w: %v", ErrUpgradeFailed, err)
	}
	return w.accept(raw, buffered, headers, request, reservation)
}

func writeUpgradeError(writer http.ResponseWriter, message string, status int) {
	responsemeta.ResetHTTPErrorHeaders(writer.Header())
	http.Error(writer, message, status)
}

func (w *WebSocketUpgrader) accept(raw net.Conn, buffered *bufio.ReadWriter, headers http.Header, request *http.Request, reservation *upgradeReservation) (*Connection, error) {
	reservation.attach(raw)
	// Reserved handshakes are tracked and interrupted by manager shutdown.
	if err := raw.SetDeadline(time.Now().Add(defaultOperationTimeout)); err != nil {
		raw.Close()
		return nil, err
	}
	connection := newConnection(w.manager, raw, w.config)
	options := &coderws.AcceptOptions{
		InsecureSkipVerify: w.config.InsecureSkipVerify,
		OriginPatterns:     w.config.OriginPatterns,
		OnPingReceived:     func(context.Context, []byte) bool { connection.touch(); return true },
		OnPongReceived:     func(context.Context, []byte) { connection.touch() },
	}
	if w.config.EnableCompression {
		options.CompressionMode = coderws.CompressionNoContextTakeover
	}
	response := &socketResponseWriter{conn: raw, buffered: buffered, headers: headers}
	conn, err := coderws.Accept(response, request, options)
	if err != nil {
		connection.cancel()
		raw.Close()
		return nil, err
	}
	connection.conn = conn
	conn.SetReadLimit(w.config.MaxMessageSize)
	if err := raw.SetDeadline(time.Time{}); err != nil {
		connection.CloseNow()
		return nil, err
	}
	if !w.manager.addReserved(connection, reservation) {
		connection.CloseNow()
		return nil, ErrManagerClosed
	}
	return connection, nil
}

func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if uint64(seconds) > uint64(maxDuration/time.Second) {
		return maxDuration
	}
	return time.Duration(seconds) * time.Second
}
