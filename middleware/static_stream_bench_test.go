package middleware

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func staticTCPServer(t testing.TB, handler fasthttp.RequestHandler) (*http.Client, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fasthttp.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	transport := &http.Transport{}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.ShutdownWithContext(ctx); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return client, "http://" + ln.Addr().String()
}

func TestStaticImmutableTCPFraming(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("fixed length body\n", 8192)
	writeStaticFixture(t, root, body)
	s, err := NewStaticHandler(root, StaticConfig{Immutable: true, MaxEntryBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	client, address := staticTCPServer(t, s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }))
	// Read twice over a reusable HTTP transport; an incorrect stream length
	// would corrupt the boundary between consecutive keep-alive responses.
	for range 2 {
		resp, err := client.Get(address + "/asset.txt")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(data) != body || resp.ContentLength != int64(len(body)) || len(resp.TransferEncoding) != 0 {
			t.Fatalf("invalid fixed-length response: length=%d transfer=%v err=%v", resp.ContentLength, resp.TransferEncoding, err)
		}
	}
}

// This uses a real plain TCP connection so Linux can exercise sendfile. It is
// an end-to-end local transfer comparison, not a TLS or remote-network estimate.
func BenchmarkStaticFileTCP(b *testing.B) {
	root := b.TempDir()
	const size = 16 << 20
	if err := os.WriteFile(filepath.Join(root, "asset.bin"), make([]byte, size), 0600); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"mutable", "immutable"} {
		b.Run(mode, func(b *testing.B) {
			s, err := NewStaticHandler(root, StaticConfig{Immutable: mode == "immutable", MaxEntryBytes: 1024})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { s.Close() })
			client, address := staticTCPServer(b, s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }))
			get := func() {
				resp, err := client.Get(address + "/asset.bin")
				if err != nil {
					b.Fatal(err)
				}
				n, err := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil || n != size || resp.StatusCode != 200 {
					b.Fatalf("transfer: bytes=%d status=%d err=%v", n, resp.StatusCode, err)
				}
			}
			get()
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				get()
			}
		})
	}
}
