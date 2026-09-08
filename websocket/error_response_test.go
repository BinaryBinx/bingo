package websocket

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func oldHandshakeHeaders() http.Header {
	return http.Header{
		"content-encoding": {"gzip"}, "Content-Digest": {"old"}, "signature": {"old"},
		"ETag": {`"old"`}, "Cache-Control": {"public, max-age=3600"}, "Set-Cookie": {"old"},
		"Trailer": {"X-Checksum"}, "X-Checksum": {"old"}, "X-Request-Id": {"handshake-id"},
		"Access-Control-Allow-Origin": {"https://client.test"}, "X-Content-Type-Options": {"nosniff"},
	}
}

func checkHandshakeError(t *testing.T, response *http.Response, status int) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || len(body) == 0 || response.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("bad handshake error: status=%d body=%q headers=%v", response.StatusCode, body, response.Header)
	}
	for name := range response.Header {
		for _, stale := range []string{"Content-Encoding", "Content-Digest", "Signature", "ETag", "Set-Cookie", "Trailer", "X-Checksum"} {
			if strings.EqualFold(name, stale) {
				t.Errorf("rejection retained %s", name)
			}
		}
	}
	for name, want := range map[string]string{
		"Cache-Control": "no-store", "X-Request-ID": "handshake-id",
		"Access-Control-Allow-Origin": "https://client.test", "X-Content-Type-Options": "nosniff",
	} {
		if response.Header.Get(name) != want {
			t.Errorf("lost %s: %v", name, response.Header)
		}
	}
}

func TestHandshakeRejectionReplacesMetadataOnBothServers(t *testing.T) {
	for _, adapter := range []string{"net/http", "fasthttp"} {
		for _, rejection := range []string{"origin", "upgrade-required", "closed"} {
			t.Run(adapter+"/"+rejection, func(t *testing.T) {
				u := testUpgrader(t)
				if rejection == "closed" {
					u.manager.Shutdown(context.Background())
				}
				var address string
				if adapter == "net/http" {
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						for name, values := range oldHandshakeHeaders() {
							w.Header()[name] = values
						}
						conn, err := u.Upgrade(w, r)
						if err == nil {
							conn.CloseNow()
							t.Error("invalid request upgraded")
						}
					}))
					defer s.Close()
					address = s.URL
				} else {
					ln, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					s := &fasthttp.Server{Handler: func(c *fasthttp.RequestCtx) {
						for name, values := range oldHandshakeHeaders() {
							for _, value := range values {
								c.Response.Header.Add(name, value)
							}
						}
						_ = u.UpgradeFastHTTP(c, func(*Connection) { t.Error("invalid request upgraded") })
					}}
					done := make(chan error, 1)
					go func() { done <- s.Serve(ln) }()
					defer func() {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						if err := s.ShutdownWithContext(ctx); err != nil {
							t.Error(err)
						}
						if err := <-done; err != nil {
							t.Error(err)
						}
					}()
					address = "http://" + ln.Addr().String()
				}
				r, _ := http.NewRequest("GET", address+"/ws", nil)
				r.Header.Set("Upgrade", "websocket")
				r.Header.Set("Connection", "Upgrade")
				r.Header.Set("Sec-WebSocket-Version", "13")
				r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
				r.Header.Set("Origin", "https://untrusted.invalid")
				status := http.StatusForbidden
				if rejection == "upgrade-required" {
					r.Header.Del("Upgrade")
					status = http.StatusUpgradeRequired
				}
				if rejection == "closed" {
					status = http.StatusServiceUnavailable
				}
				client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableCompression: true}}
				defer client.CloseIdleConnections()
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				checkHandshakeError(t, response, status)
			})
		}
	}
}

func TestUpgradeErrorsBeforeHijackKeepPublicHeaders(t *testing.T) {
	for _, mode := range []string{"nil-request", "no-hijacker"} {
		t.Run(mode, func(t *testing.T) {
			u := testUpgrader(t)
			w := httptest.NewRecorder()
			for name, values := range oldHandshakeHeaders() {
				w.Header()[name] = values
			}
			request, _ := http.NewRequest("GET", "http://example.test/ws", nil)
			status := http.StatusNotImplemented
			if mode == "nil-request" {
				request = nil
				status = http.StatusBadRequest
			}
			if _, err := u.Upgrade(w, request); err == nil {
				t.Fatal("upgrade succeeded")
			}
			checkHandshakeError(t, w.Result(), status)
		})
	}
	for _, mode := range []string{"nil-handler", "invalid-target"} {
		t.Run(mode, func(t *testing.T) {
			u := testUpgrader(t)
			var c fasthttp.RequestCtx
			defer c.Response.Reset()
			for name, values := range oldHandshakeHeaders() {
				for _, value := range values {
					c.Response.Header.Add(name, value)
				}
			}
			c.Request.SetRequestURI("/ws")
			var handler func(*Connection)
			status := http.StatusInternalServerError
			if mode == "invalid-target" {
				c.Request.SetRequestURI("/%zz")
				handler = func(*Connection) {}
				status = http.StatusBadRequest
			}
			if err := u.UpgradeFastHTTP(&c, handler); err == nil {
				t.Fatal("upgrade succeeded")
			}
			if c.Response.StatusCode() != status || c.Hijacked() || string(c.Response.Header.Peek("X-Request-ID")) != "handshake-id" || string(c.Response.Header.Peek("Access-Control-Allow-Origin")) != "https://client.test" || string(c.Response.Header.Peek("Cache-Control")) != "no-store" {
				t.Fatalf("incorrect early error: %s", c.Response.Header.Header())
			}
			for _, name := range []string{"Content-Encoding", "Content-Digest", "Signature", "ETag", "Trailer", "X-Checksum"} {
				if len(c.Response.Header.Peek(name)) != 0 {
					t.Errorf("retained %s", name)
				}
			}
		})
	}
}
