package middleware

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestTimeoutKeepsPublicMetadataInBothOrders(t *testing.T) {
	for _, outside := range []bool{true, false} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			release, finished := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			leaf := func(c *fasthttp.RequestCtx) {
				defer close(finished)
				c.Response.Header.Set("Set-Cookie", "private")
				c.Response.Header.Set("Content-Digest", "old")
				c.Response.Header.Set("Signature", "old")
				<-release
				c.SetBodyString("late response")
			}
			headers := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
				return CORS([]string{"https://client.test"}, nil, nil)(RequestID()(Security()(next)))
			}
			h := headers(Timeout(20 * time.Millisecond)(leaf))
			if !outside {
				h = Timeout(20 * time.Millisecond)(headers(leaf))
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &fasthttp.Server{Handler: h}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ln) }()
			defer func() { ln.Close(); <-done }()
			req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
			req.Header.Set("Origin", "https://client.test")
			req.Close = true
			client := &http.Client{Timeout: time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			once.Do(func() { close(release) })
			<-finished
			shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := server.ShutdownWithContext(shutdown); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 408 {
				t.Fatal(resp.StatusCode)
			}
			for _, field := range []string{"Access-Control-Allow-Origin", "X-Request-ID", "X-Content-Type-Options", "Content-Security-Policy"} {
				if resp.Header.Get(field) == "" {
					t.Errorf("missing %s", field)
				}
			}
			if resp.Header.Get("Cache-Control") != "no-store" {
				t.Error("timeout is not no-store")
			}
			for _, field := range []string{"Set-Cookie", "Content-Digest", "Signature"} {
				if resp.Header.Get(field) != "" {
					t.Errorf("copied unsafe %s", field)
				}
			}
		})
	}
}
