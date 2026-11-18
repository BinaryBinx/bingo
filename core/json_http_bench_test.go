package core

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// Run each benchmark in a separate process when comparing pool behavior.
func BenchmarkJSONLargeHTTP(b *testing.B) {
	benchmarkJSONHTTP(b, false, false)
}

func BenchmarkJSONLargeAfterSmallWarmupHTTP(b *testing.B) {
	benchmarkJSONHTTP(b, true, false)
}

func BenchmarkJSONMixedHTTP(b *testing.B) {
	benchmarkJSONHTTP(b, true, true)
}

func benchmarkJSONHTTP(b *testing.B, warmSmall, mixed bool) {
	b.Helper()
	type payload struct {
		Text string `json:"text"`
	}
	small, large := &payload{strings.Repeat("a", 128)}, &payload{strings.Repeat("a", 64<<10)}
	const jsonOverhead = len(`{"text":""}`)
	if warmSmall {
		// Cross bytebufferpool's recalibration threshold with small responses
		// before timing. Use the same fasthttp response pool as the server.
		var response fasthttp.Response
		body := `{"text":"` + small.Text + `"}`
		for range 85000 {
			response.SetBodyString(body)
			response.Reset()
		}
	}

	cfg := DefaultConfig()
	cfg.RunMode = RunModeRelease
	app := NewApp(cfg)
	app.GET("/small", func(c *RequestContext) {
		if err := c.JSON(200, small); err != nil {
			panic(err)
		}
	})
	app.GET("/large", func(c *RequestContext) {
		if err := c.JSON(200, large); err != nil {
			panic(err)
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- app.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := app.ShutdownWithContext(ctx)
		if shutdownErr != nil {
			b.Error(shutdownErr)
			_ = listener.Close()
		}
		if err := <-stopped; err != nil {
			b.Error(err)
		}
	}()

	// Read with net/http to avoid the client also training fasthttp's pool.
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	request := &http.Request{Method: http.MethodGet}
	send := func(path string, want int) error {
		if _, err := io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: bingo.test\r\n\r\n"); err != nil {
			return err
		}
		response, err := http.ReadResponse(reader, request)
		if err != nil {
			return err
		}
		n, err := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK || n != int64(want) {
			return fmt.Errorf("%s: status %d, body %d bytes, want %d", path, response.StatusCode, n, want)
		}
		return nil
	}
	if err := send("/large", len(large.Text)+jsonOverhead); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		path, size := "/large", len(large.Text)
		if mixed && i%20 != 0 {
			path, size = "/small", len(small.Text)
		}
		if err := send(path, size+jsonOverhead); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
}
