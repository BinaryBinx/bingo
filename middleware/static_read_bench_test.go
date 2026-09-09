package middleware

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkStaticBodyRead measures opening and buffering a file after Stat,
// excluding response-cache hits and network transfer. The OS may cache the file.
func BenchmarkStaticBodyRead(b *testing.B) {
	for _, size := range []int{4 << 10, 128 << 10, 512 << 10} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "asset")
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0600); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				f, err := os.Open(path)
				if err != nil {
					b.Fatal(err)
				}
				var c fasthttp.RequestCtx
				data, streamed, err := staticBody(&c, f, info, 1<<20)
				if err != nil || streamed || len(data) != size {
					b.Fatal("invalid buffered file", err)
				}
			}
		})
	}
}
