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

// Compress 压缩中间件
func Compress() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	var gzipPool sync.Pool
	gzipPool.New = func() interface{} {
		return gzip.NewWriter(nil)
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			guard := guardResponse(ctx)
			head := ctx.IsHead()
			// Capture the original negotiation as a bool: next may mutate request headers.
			gzipAccepted := !head && requestAcceptsGzip(&ctx.Request.Header)
			next(ctx)
			if guard.abandoned(ctx) || ctx.Hijacked() {
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
			if statusCode < 200 || len(body) < 256 || statusCode == fasthttp.StatusPartialContent || statusCode == fasthttp.StatusNoContent || statusCode == fasthttp.StatusNotModified ||
				len(ctx.Response.Header.Peek("Content-Encoding")) > 0 {
				return
			}

			if hasCacheDirective(&ctx.Response.Header, "no-transform") {
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
