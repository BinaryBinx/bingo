package websocket

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"

	"github.com/BinaryBinx/bingo/internal/responsemeta"
	"github.com/valyala/fasthttp"
)

// UpgradeFastHTTP schedules a session on fasthttp's hijack callback. handler runs
// after the HTTP handler returns; its connection is closed automatically when it
// returns. Never capture ctx: fasthttp owns its pooled request/response storage.
// Origin and protocol checks use the same coder/websocket path as Upgrade.
func (w *WebSocketUpgrader) UpgradeFastHTTP(ctx *fasthttp.RequestCtx, handler func(*Connection)) error {
	if handler == nil {
		responsemeta.ResetError(ctx, http.StatusInternalServerError, "WebSocket handler is required")
		return fmt.Errorf("%w: nil handler", ErrUpgradeFailed)
	}
	request, err := copyFastHTTPRequest(ctx)
	if err != nil {
		responsemeta.ResetError(ctx, http.StatusBadRequest, "invalid WebSocket request")
		return fmt.Errorf("%w: %v", ErrUpgradeFailed, err)
	}
	headers := make(http.Header)
	ctx.Response.Header.VisitAll(func(key, value []byte) { headers.Add(string(key), string(value)) })
	// The normal response is suppressed, but middleware must not mistake a
	// scheduled upgrade for a cacheable HTTP 200 response.
	ctx.SetStatusCode(http.StatusSwitchingProtocols)
	ctx.HijackSetNoResponse(true)
	transport := ctx.Conn()
	ctx.Hijack(func(bufferedConn net.Conn) {
		raw := &ownedHijackConn{Conn: bufferedConn, transport: transport}
		defer raw.Close()
		if err := raw.SetDeadline(time.Now().Add(defaultOperationTimeout)); err != nil {
			return
		}
		// Hijack callbacks are outside core's request recovery boundary.
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("WebSocket handler panic: %v\n%s", recovered, debug.Stack())
			}
		}()
		buffered := bufio.NewReadWriter(bufio.NewReader(raw), bufio.NewWriter(raw))
		response := &socketResponseWriter{conn: raw, buffered: buffered, headers: headers}
		conn, err := w.Upgrade(response, request)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	})
	return nil
}

// All strings are copied before returning. fasthttpadaptor.ConvertRequest uses
// unsafe aliases and cannot be retained by an asynchronous hijack callback.
func copyFastHTTPRequest(ctx *fasthttp.RequestCtx) (*http.Request, error) {
	requestURI := string(ctx.RequestURI())
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil {
		return nil, err
	}
	proto := string(ctx.Request.Header.Protocol())
	major, minor, ok := http.ParseHTTPVersion(proto)
	if !ok {
		return nil, fmt.Errorf("invalid HTTP version %q", proto)
	}
	headers := make(http.Header)
	ctx.Request.Header.VisitAll(func(key, value []byte) { headers.Add(string(key), string(value)) })
	return &http.Request{
		Method: string(ctx.Method()), URL: parsed, Proto: proto, ProtoMajor: major, ProtoMinor: minor,
		Header: headers, Host: string(ctx.Host()), RequestURI: requestURI,
		RemoteAddr: ctx.RemoteAddr().String(), TLS: ctx.TLSConnectionState(), Body: http.NoBody,
	}, nil
}

// fasthttp's pooled hijack wrapper normally makes Close a no-op. Preserve its
// buffered Read while closing the real socket; never keep RequestCtx in a callback.
type ownedHijackConn struct {
	net.Conn
	transport net.Conn
}

func (c *ownedHijackConn) Close() error { return c.transport.Close() }

// socketResponseWriter preserves buffered client frames when handing the stream
// to coder/websocket and writes only one complete handshake response.
type socketResponseWriter struct {
	conn     net.Conn
	buffered *bufio.ReadWriter
	headers  http.Header
	status   int
	err      error
}

func (w *socketResponseWriter) Header() http.Header { return w.headers }

func (w *socketResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.headers.Del("Content-Length")
	w.headers.Del("Transfer-Encoding")
	if status == http.StatusSwitchingProtocols {
		w.headers.Del("Content-Encoding")
	} else {
		// coder/websocket writes rejection bodies directly after hijacking.
		// No outer compression or error middleware can repair their headers.
		responsemeta.ResetHTTPErrorHeaders(w.headers)
		w.headers.Set("Content-Type", "text/plain; charset=utf-8")
		w.headers.Set("Connection", "close")
	}
	_, w.err = fmt.Fprintf(w.buffered, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	if w.err == nil {
		w.err = w.headers.Write(w.buffered)
	}
	if w.err == nil {
		_, w.err = w.buffered.WriteString("\r\n")
	}
	if w.err == nil {
		w.err = w.buffered.Flush()
	}
}

func (w *socketResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.buffered.Write(data)
	if err == nil {
		err = w.buffered.Flush()
	}
	return n, err
}

func (w *socketResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.err != nil {
		return nil, nil, w.err
	}
	return w.conn, w.buffered, nil
}
