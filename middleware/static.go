package middleware

import (
	"bytes"
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
// 1 MiB per file and one minute retention. TTL < 0 disables snapshots.
type StaticConfig struct {
	MaxEntries    int
	MaxBytes      int64
	MaxEntryBytes int64
	TTL           time.Duration
	// Immutable skips metadata checks on snapshot hits until TTL expiry/Reload.
	// Enable only for versioned assets whose contents cannot change at that URL.
	// Large immutable files use their known size for fixed-length streaming,
	// enabling sendfile on supported plain TCP connections. Never modify an
	// immutable file in place while responses might still be reading it.
	// It does not set a browser Cache-Control policy automatically.
	Immutable bool
	// DisableRange opts out of single byte-range responses. By default GET
	// supports closed, open-ended and suffix ranges; HEAD ignores Range.
	DisableRange bool
	// ETag optionally supplies a valid quoted strong or weak entity tag from
	// metadata. Nil omits ETag. WeakStaticETag avoids reading the file body.
	// Custom strong tags must identify the exact bytes; callbacks must be safe
	// for concurrent calls and must not call this handler's Reload or Close.
	ETag func(os.FileInfo) string
}

type staticSnapshot struct {
	body                      []byte
	modTime                   time.Time
	size                      int64
	contentType, lastModified string
	etag                      string
}

func normalizeStaticConfig(cfg StaticConfig) StaticConfig {
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
	if cfg.MaxEntryBytes > cfg.MaxBytes {
		cfg.MaxEntryBytes = cfg.MaxBytes
	}
	if cfg.MaxEntryBytes == 1<<63-1 {
		// Reserve the sentinel byte used by LimitReader without overflowing.
		cfg.MaxEntryBytes--
	}
	return cfg
}

func Static(root string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return StaticWithConfig(root, StaticConfig{})
}

// StaticWithConfig keeps the original per-request root lifetime and follows
// directory replacements automatically. Use NewStaticHandler for a reusable
// root with explicit Close/Reload lifecycle and faster cache-hit processing.
func StaticWithConfig(root string, cfg StaticConfig) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	cfg = normalizeStaticConfig(cfg)
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
			handled := func() bool { defer r.Close(); return serveStatic(ctx, r, cfg, cache) }()
			if !handled {
				next(ctx)
			}
		}
	}
}

func serveStaticSnapshot(ctx *fasthttp.RequestCtx, item staticSnapshot, cfg StaticConfig) {
	if staticResponseMetadata(ctx, item.modTime, item.size, item.contentType, item.lastModified, item.etag, cfg.DisableRange) {
		return
	}
	if selected, ranged := selectStaticRange(ctx, cfg, item.modTime, item.size, item.etag); ranged {
		if selected.length > 0 {
			ctx.SetBody(item.body[selected.start : selected.start+selected.length])
		}
		return
	}
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(item.body)
}

// Root-scoped operations enforce the same path/symlink boundary in both APIs.
// Readers of a managed handler hold its generation lock only through this call;
// streamed files have their own descriptor and may outlive a root reload/close.
func serveStatic(ctx *fasthttp.RequestCtx, r *os.Root, cfg StaticConfig, cache *boundedCache[string, staticSnapshot]) bool {
	name := path.Clean("/" + strings.ReplaceAll(string(ctx.Path()), "\\", "/"))
	if name == "/" {
		name = "/index.html"
	}
	name = filepath.FromSlash(strings.TrimPrefix(name, "/"))
	item, cached := cache.get(name, time.Now())
	if cached && cfg.Immutable {
		serveStaticSnapshot(ctx, item, cfg)
		return true
	}
	info, err := r.Stat(name)
	if err != nil || !info.Mode().IsRegular() {
		cache.delete(name)
		return false
	}
	if cached && item.size == info.Size() && item.modTime.Equal(info.ModTime()) {
		serveStaticSnapshot(ctx, item, cfg)
		return true
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	etag := staticETag(cfg, info)
	if staticMetadata(ctx, info, contentType, etag, cfg.DisableRange) {
		return true
	}
	cache.delete(name)
	f, err := openStaticFile(r, name)
	if err != nil {
		return false
	}
	transferred := false
	defer func() {
		// A user ETag callback or an old response stream's Close can panic.
		// Keep ownership until handing a new stream to fasthttp succeeds.
		if !transferred {
			_ = f.Close()
		}
	}()
	// A replacement between Stat and Open must still be a regular file in Root.
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return false
	}
	etag = staticETag(cfg, info)
	if staticMetadata(ctx, info, contentType, etag, cfg.DisableRange) {
		f.Close()
		return true
	}
	if selected, ranged := selectStaticRange(ctx, cfg, info.ModTime(), info.Size(), etag); ranged {
		if selected.length > 0 {
			streamStaticRange(ctx, f, selected)
			transferred = true
		} else {
			f.Close()
		}
		return true
	}
	if cfg.Immutable && info.Size() > cfg.MaxEntryBytes && uint64(info.Size()) <= uint64(^uint(0)>>1) {
		// Preserve *os.File itself: fasthttp recognizes it and flushes headers
		// before taking the OS file-to-socket path. Mutable/growing files still
		// use an unknown length below so the complete file can be streamed.
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyStream(f, int(info.Size()))
		transferred = true
		return true
	}
	data, streamed, err := staticBody(ctx, f, info, cfg.MaxEntryBytes)
	if err != nil {
		resetErrorResponse(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
		return true
	}
	if streamed {
		transferred = true
		return true
	}
	item = staticSnapshot{body: data, modTime: info.ModTime(), size: int64(len(data)), contentType: contentType, lastModified: info.ModTime().UTC().Format(http.TimeFormat), etag: etag}
	cost := int64(len(data) + len(name) + len(item.contentType) + len(item.lastModified) + len(item.etag) + 224)
	cache.put(name, item, cost, time.Now().Add(cfg.TTL))
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(data)
	return true
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

// Preconditions run in RFC 9110 section 13.2.2 order on every cache path.
func staticMetadata(ctx *fasthttp.RequestCtx, info os.FileInfo, contentType, etag string, disableRange bool) bool {
	return staticResponseMetadata(ctx, info.ModTime(), info.Size(), contentType, info.ModTime().UTC().Format(http.TimeFormat), etag, disableRange)
}

func staticResponseMetadata(ctx *fasthttp.RequestCtx, modified time.Time, size int64, contentType, lastModified, etag string, disableRange bool) bool {
	if contentType != "" {
		ctx.SetContentType(contentType)
	}
	ctx.Response.Header.Set("Last-Modified", lastModified)
	ctx.Response.Header.Del("Content-Range")
	if etag != "" {
		ctx.Response.Header.Set("ETag", etag)
	} else {
		ctx.Response.Header.Del("ETag")
	}
	if !disableRange {
		ctx.Response.Header.Set("Accept-Ranges", "bytes")
	} else {
		ctx.Response.Header.Del("Accept-Ranges")
	}
	if values := ctx.Request.Header.PeekAll("If-Match"); len(values) > 0 {
		if !staticETagListMatches(values, etag, true) {
			staticPreconditionResponse(ctx, fasthttp.StatusPreconditionFailed)
			return true
		}
	} else if values := ctx.Request.Header.PeekAll("If-Unmodified-Since"); len(values) == 1 {
		if since, err := http.ParseTime(string(values[0])); err == nil && modified.Truncate(time.Second).After(since) {
			staticPreconditionResponse(ctx, fasthttp.StatusPreconditionFailed)
			return true
		}
	}
	if values := ctx.Request.Header.PeekAll("If-None-Match"); len(values) > 0 {
		if staticETagListMatches(values, etag, false) {
			staticPreconditionResponse(ctx, fasthttp.StatusNotModified)
			return true
		}
	} else if values := ctx.Request.Header.PeekAll("If-Modified-Since"); len(values) == 1 {
		if since, err := http.ParseTime(string(values[0])); err == nil && !modified.Truncate(time.Second).After(since) {
			staticPreconditionResponse(ctx, fasthttp.StatusNotModified)
			return true
		}
	}
	if ctx.IsHead() {
		ctx.Response.ResetBody()
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.Response.Header.SetContentLength(int(size))
		return true
	}
	return false
}

func staticWildcard(values [][]byte) bool {
	return len(values) == 1 && bytes.Equal(bytes.TrimSpace(values[0]), []byte("*"))
}

func staticPreconditionResponse(ctx *fasthttp.RequestCtx, status int) {
	ctx.Response.ResetBody()
	ctx.Response.Header.Del("Content-Length")
	ctx.SetStatusCode(status)
}
