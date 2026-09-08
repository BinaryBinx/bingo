package middleware

import (
	"bytes"
	"github.com/klauspost/compress/gzip"
	"github.com/valyala/fasthttp"
	"io"
	"strconv"
	"strings"
	"sync"
)

// compressBufPool 复用压缩中间件的输出 buffer，减少 GC 压力
var compressBufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// gzipPreference scans negotiation tokens without allocating slices. Repeated
// gzip fields retain the most restrictive quality, including an explicit q=0.
type gzipPreference struct{ gzip, wildcard float64 }

func (p *gzipPreference) add(value []byte) {
	for coding := range bytes.SplitSeq(value, []byte(",")) {
		name, parameters, hasParameters := bytes.Cut(coding, []byte(";"))
		name = bytes.TrimSpace(name)
		quality := 1.0
		for hasParameters {
			var parameter []byte
			parameter, parameters, hasParameters = bytes.Cut(parameters, []byte(";"))
			key, value, ok := bytes.Cut(bytes.TrimSpace(parameter), []byte("="))
			if !ok || !bytes.EqualFold(key, []byte("q")) {
				quality = 0
				continue
			}
			parsed, err := strconv.ParseFloat(string(bytes.TrimSpace(value)), 64)
			if err != nil || !(parsed >= 0 && parsed <= 1) {
				quality = 0
			} else {
				quality = parsed
			}
		}
		if bytes.EqualFold(name, []byte("gzip")) {
			if p.gzip < 0 {
				p.gzip = quality
			} else {
				p.gzip = min(p.gzip, quality)
			}
		}
		if bytes.Equal(name, []byte("*")) {
			if p.wildcard < 0 {
				p.wildcard = quality
			} else {
				p.wildcard = min(p.wildcard, quality)
			}
		}
	}
}
func (p gzipPreference) accepts() bool {
	if p.gzip >= 0 {
		return p.gzip > 0
	}
	return p.wildcard > 0
}
func acceptsGzip(value string) bool {
	p := gzipPreference{-1, -1}
	p.add([]byte(value))
	return p.accepts()
}
func requestAcceptsGzip(h *fasthttp.RequestHeader) bool {
	p := gzipPreference{-1, -1}
	for _, line := range h.PeekAll("Accept-Encoding") {
		p.add(line)
	}
	return p.accepts()
}

// Checking one directive does not require constructing a map of every value.
func hasCacheDirective(h *fasthttp.ResponseHeader, wanted string) bool {
	for _, line := range h.PeekAll("Cache-Control") {
		for part := range strings.SplitSeq(string(line), ",") {
			name, _, _ := strings.Cut(part, "=")
			if strings.EqualFold(strings.TrimSpace(name), wanted) {
				return true
			}
		}
	}
	return false
}

// addVary 将字段追加到 Vary 头，保留既有的协商维度（如 CORS 的 Origin）
func addVary(h *fasthttp.ResponseHeader, field string) {
	existing := joinHeaderValues(h.PeekAll("Vary"))
	if existing == "" {
		h.Set("Vary", field)
		return
	}
	for v := range strings.SplitSeq(existing, ",") {
		if strings.TrimSpace(v) == "*" || strings.EqualFold(strings.TrimSpace(v), field) {
			return
		}
	}
	h.Set("Vary", existing+", "+field)
}

// CompressConfig controls buffered gzip responses. Zero values use the default
// compression level, a 256-byte minimum and common compressible content types.
type CompressConfig struct {
	// Level accepts gzip.HuffmanOnly, gzip.DefaultCompression, or 1 through 9.
	// Zero and invalid levels fall back to gzip.DefaultCompression.
	Level   int
	MinSize int
	// ContentTypes matches media types without parameters, case-insensitively.
	// A single wildcard is supported (text/*, application/*+json, */*).
	// Nil uses defaults; an explicitly empty slice disables all media types.
	ContentTypes []string
	// Skip runs before next and bypasses compression and its Vary header.
	// Prefer route-based predicates; response-dependent decisions use ContentTypes.
	Skip     func(*fasthttp.RequestCtx) bool
	Disabled bool
}

func Compress() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return CompressWithConfig(CompressConfig{})
}

// CompressWithConfig filters already compressed/binary media by default. Use
// ContentTypes: []string{"*/*"} to explicitly allow every media type.
// Responses with digest or signature metadata are left unchanged, including
// Vary, because transforming either the body or signed headers can invalidate
// their integrity protection. Generate such metadata outside Compress if it
// should describe the final compressed representation.
func CompressWithConfig(cfg CompressConfig) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if cfg.Level == 0 || cfg.Level < gzip.HuffmanOnly || cfg.Level > gzip.BestCompression {
		cfg.Level = gzip.DefaultCompression
	}
	if cfg.MinSize <= 0 {
		cfg.MinSize = 256
	}
	if cfg.ContentTypes == nil {
		cfg.ContentTypes = []string{"text/*", "application/json", "application/*+json", "application/xml", "application/*+xml", "application/javascript", "application/x-javascript", "application/wasm", "application/x-www-form-urlencoded", "image/svg+xml"}
	} else {
		cfg.ContentTypes = append([]string{}, cfg.ContentTypes...)
	}
	for i, value := range cfg.ContentTypes {
		cfg.ContentTypes[i] = strings.TrimSpace(value)
	}
	var gzipPool sync.Pool
	gzipPool.New = func() interface{} {
		w, _ := gzip.NewWriterLevel(io.Discard, cfg.Level)
		return w
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		if cfg.Disabled {
			return next
		}
		return func(ctx *fasthttp.RequestCtx) {
			if cfg.Skip != nil && cfg.Skip(ctx) {
				next(ctx)
				return
			}
			guard := guardResponse(ctx)
			head := ctx.IsHead()
			// Capture the original negotiation as a bool: next may mutate request headers.
			gzipAccepted := !head && requestAcceptsGzip(&ctx.Request.Header)
			next(ctx)
			if guard.abandoned(ctx) || ctx.Hijacked() {
				return
			}
			if hasResponseIntegrity(&ctx.Response.Header) {
				return
			}
			// Every negotiated variant needs Vary, including identity responses.
			addVary(&ctx.Response.Header, "Accept-Encoding")
			if !gzipAccepted {
				return
			}

			// 流式响应不压缩：Response.Body() 会把流读到 EOF（SSE 会阻塞、大流全量入内存），
			// 读错误文本还会被当作正文返回
			if ctx.Response.IsBodyStream() {
				return
			}

			body := ctx.Response.Body()
			statusCode := ctx.Response.StatusCode()
			if statusCode < 200 || len(body) < cfg.MinSize || statusCode == fasthttp.StatusPartialContent || statusCode == fasthttp.StatusNoContent || statusCode == fasthttp.StatusNotModified ||
				len(ctx.Response.Header.Peek("Content-Encoding")) > 0 {
				return
			}

			if !compressibleContentType(ctx.Response.Header.ContentType(), cfg.ContentTypes) || hasCacheDirective(&ctx.Response.Header, "no-transform") {
				return
			}

			buf := compressBufPool.Get().(*bytes.Buffer)
			buf.Reset()
			w := gzipPool.Get().(*gzip.Writer)
			w.Reset(buf)
			defer func() {
				// Detach the potentially large output before retaining the writer.
				w.Reset(io.Discard)
				gzipPool.Put(w)
				if buf.Cap() <= 256<<10 {
					buf.Reset()
					compressBufPool.Put(buf)
				}
			}()
			if _, err := w.Write(body); err != nil {
				w.Close()
				return
			}
			if err := w.Close(); err != nil {
				return
			}
			if buf.Len() >= len(body) {
				return
			}

			ctx.Response.SetBody(buf.Bytes())
			ctx.Response.Header.Set("Content-Encoding", "gzip")
			if etag := string(ctx.Response.Header.Peek("ETag")); etag != "" && !strings.HasPrefix(etag, "W/") {
				ctx.Response.Header.Del("ETag")
			}
			// 合并而非覆盖 Vary，保留压缩前已声明的协商维度（如 Origin）
			addVary(&ctx.Response.Header, "Accept-Encoding")
			ctx.Response.Header.Del("Content-Length")
		}
	}
}

// Preserve current and legacy digest fields as well as HTTP message signatures.
// A signature can cover Vary or Content-Length without covering a digest, so the
// check must precede every header mutation, even when gzip was not negotiated.
func hasResponseIntegrity(h *fasthttp.ResponseHeader) bool {
	for key := range h.All() {
		if isIntegrityField(key) {
			return true
		}
	}
	for _, key := range h.PeekTrailerKeys() {
		if isIntegrityField(key) {
			return true
		}
	}
	return false
}

func isIntegrityField(key []byte) bool {
	return bytes.EqualFold(key, []byte("Content-Digest")) ||
		bytes.EqualFold(key, []byte("Repr-Digest")) ||
		bytes.EqualFold(key, []byte("Digest")) ||
		bytes.EqualFold(key, []byte("Content-MD5")) ||
		bytes.EqualFold(key, []byte("Signature")) ||
		bytes.EqualFold(key, []byte("Signature-Input"))
}

func compressibleContentType(value []byte, patterns []string) bool {
	value, _, _ = bytes.Cut(value, []byte(";"))
	value = bytes.TrimSpace(value)
	for _, pattern := range patterns {
		if pattern == "*/*" {
			return true
		}
		prefix, suffix, wildcard := strings.Cut(pattern, "*")
		if !wildcard {
			if bytes.EqualFold(value, []byte(pattern)) {
				return true
			}
			continue
		}
		if len(value) >= len(prefix)+len(suffix) &&
			bytes.EqualFold(value[:len(prefix)], []byte(prefix)) &&
			bytes.EqualFold(value[len(value)-len(suffix):], []byte(suffix)) {
			return true
		}
	}
	return false
}
