package middleware

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

// StaticConfig controls file snapshots. Defaults: 1000 entries, 64 MiB total,
// 1 MiB per file and one minute retention. File metadata is rechecked on each hit.
// TTL < 0 disables snapshot caching. GET supports If-Modified-Since; HEAD avoids reads.
type StaticConfig struct {
	MaxEntries    int
	MaxBytes      int64
	MaxEntryBytes int64
	TTL           time.Duration
}
type staticSnapshot struct {
	body    []byte
	modTime time.Time
	size    int64
}

func Static(root string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return StaticWithConfig(root, StaticConfig{})
}

func StaticWithConfig(root string, cfg StaticConfig) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 1000
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 64 << 20
	}
	if cfg.MaxEntryBytes <= 0 {
		cfg.MaxEntryBytes = 1 << 20
	}
	if cfg.TTL == 0 {
		cfg.TTL = time.Minute
	}
	// Leave room for the sentinel byte and avoid overflowing LimitReader's limit.
	if cfg.MaxEntryBytes > cfg.MaxBytes {
		cfg.MaxEntryBytes = cfg.MaxBytes
	}
	if cfg.MaxEntryBytes == 1<<63-1 {
		cfg.MaxEntryBytes--
	}
	rootAbs, rootErr := filepath.Abs(root)
	cache := newBoundedCache[string, staticSnapshot](cfg.MaxEntries, cfg.MaxBytes)
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if rootErr != nil || (!ctx.IsGet() && !ctx.IsHead()) {
				next(ctx)
				return
			}
			r, err := os.OpenRoot(rootAbs)
			if err != nil {
				next(ctx)
				return
			}
			defer r.Close()
			name := path.Clean("/" + strings.ReplaceAll(string(ctx.Path()), "\\", "/"))
			if name == "/" {
				name = "/index.html"
			}
			name = filepath.FromSlash(strings.TrimPrefix(name, "/"))
			info, err := r.Stat(name)
			if err != nil || !info.Mode().IsRegular() {
				cache.delete(name)
				next(ctx)
				return
			}
			contentType := mime.TypeByExtension(filepath.Ext(name))
			if staticMetadata(ctx, info, contentType) {
				return
			}
			if item, ok := cache.get(name, time.Now()); ok && item.size == info.Size() && item.modTime.Equal(info.ModTime()) {
				if contentType != "" {
					ctx.SetContentType(contentType)
				}
				ctx.SetStatusCode(fasthttp.StatusOK)
				ctx.SetBody(item.body)
				return
			}
			cache.delete(name)
			f, err := openStaticFile(r, name)
			if err != nil {
				next(ctx)
				return
			}
			// Revalidate the opened object: it may differ from the preceding Stat.
			info, err = f.Stat()
			if err != nil || !info.Mode().IsRegular() {
				f.Close()
				next(ctx)
				return
			}
			if staticMetadata(ctx, info, contentType) {
				f.Close()
				return
			}
			data, streamed, err := staticBody(ctx, f, info, cfg.MaxEntryBytes)
			if err != nil {
				ctx.Error("Internal Server Error", 500)
				return
			}
			if streamed {
				return
			} // fasthttp now owns f and closes it after sending.
			cost := int64(len(data) + len(name) + 128)
			cache.put(name, staticSnapshot{data, info.ModTime(), info.Size()}, cost, time.Now().Add(cfg.TTL))
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetBody(data)
		}
	}
}

// staticBody keeps ownership of f until either closing it or transferring it to
// fasthttp. A file that grows past the cache limit is rewound and streamed whole.
func staticBody(ctx *fasthttp.RequestCtx, f *os.File, info os.FileInfo, limit int64) ([]byte, bool, error) {
	if info.Size() > limit {
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyStream(f, -1)
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		f.Close()
		return nil, false, err
	}
	if int64(len(data)) > limit {
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, false, err
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyStream(f, -1)
		return nil, true, nil
	}
	err = f.Close()
	return data, false, err
}

// HTTP dates have one-second resolution. If-None-Match takes precedence over
// If-Modified-Since; this handler does not synthesize an ETag.
func staticMetadata(ctx *fasthttp.RequestCtx, info os.FileInfo, contentType string) bool {
	if contentType != "" {
		ctx.SetContentType(contentType)
	}
	ctx.Response.Header.Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	if values := ctx.Request.Header.PeekAll("If-Modified-Since"); len(values) == 1 && len(ctx.Request.Header.Peek("If-None-Match")) == 0 {
		if since, err := http.ParseTime(string(values[0])); err == nil && !info.ModTime().Truncate(time.Second).After(since) {
			ctx.Response.ResetBody()
			ctx.SetStatusCode(fasthttp.StatusNotModified)
			return true
		}
	}
	if ctx.IsHead() {
		ctx.Response.ResetBody()
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.Response.Header.SetContentLength(int(info.Size()))
		return true
	}
	return false
}
