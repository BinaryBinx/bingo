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

func acceptsGzip(value string) bool {
	values := [][]byte{[]byte(value)}
	gzipQuality, wildcard := -1.0, -1.0
	for _, value := range values {
		for _, coding := range strings.Split(string(value), ",") {
			parts := strings.Split(coding, ";")
			name := strings.TrimSpace(parts[0])
			quality := 1.0
			for _, parameter := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if !ok || !strings.EqualFold(key, "q") {
					quality = 0
					continue
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || !(parsed >= 0 && parsed <= 1) {
					quality = 0
				} else {
					quality = parsed
				}
			}
			if strings.EqualFold(name, "gzip") {
				if gzipQuality < 0 {
					gzipQuality = quality
				} else {
					gzipQuality = min(gzipQuality, quality)
				}
			}
			if name == "*" {
				if wildcard < 0 {
					wildcard = quality
				} else {
					wildcard = min(wildcard, quality)
				}
			}
		}
	}
	if gzipQuality >= 0 {
		return gzipQuality > 0
	}
	return wildcard > 0
}

// addVary 将字段追加到 Vary 头，保留既有的协商维度（如 CORS 的 Origin）
func addVary(h *fasthttp.ResponseHeader, field string) {
	existing := joinHeaderValues(h.PeekAll("Vary"))
	if existing == "" {
		h.Set("Vary", field)
		return
	}
	for _, v := range strings.Split(existing, ",") {
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
			acceptEncoding := joinHeaderValues(ctx.Request.Header.PeekAll("Accept-Encoding"))
			next(ctx)
			if guard.abandoned(ctx) || ctx.Hijacked() {
				return
			}
			// Every negotiated variant needs Vary, including identity responses.
			addVary(&ctx.Response.Header, "Accept-Encoding")
			if head || !acceptsGzip(acceptEncoding) || len(cacheDirectives(ctx.Response.Header.PeekAll("Cache-Control"))["no-transform"]) > 0 {
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
