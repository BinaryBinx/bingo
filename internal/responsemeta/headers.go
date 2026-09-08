// Package responsemeta keeps error-response policy consistent across the core
// and middleware without sharing a mutable response with timeout goroutines.
package responsemeta

import (
	"net/http"
	"strings"
	"sync"

	"github.com/valyala/fasthttp"
)

// Both HTTP adapters replace the representation using the same policy. Keep
// retry/authentication headers and public metadata, but discard the old body's
// validators, cache policy and trailer values before writing a new error body.
var errorResponseFields = [...]string{
	"Content-Type", "Content-Encoding", "Content-Length", "Content-Range", "Content-Location",
	"Transfer-Encoding", "Trailer", "ETag", "Last-Modified", "Location", "Set-Cookie",
	"Content-Digest", "Repr-Digest", "Digest", "Content-MD5", "Signature", "Signature-Input",
	"Cache-Control", "Age", "Expires", "Accept-Ranges",
}

func errorResponseField(name string) bool {
	for _, field := range errorResponseFields {
		if strings.EqualFold(name, field) {
			return true
		}
	}
	return false
}

// ResetError removes metadata describing the old representation, including
// trailers and signatures, while retaining security, CORS and tracing headers.
func ResetError(ctx *fasthttp.RequestCtx, status int, body string) {
	// Also discard original trailers and non-canonical spellings when callers
	// explicitly disabled fasthttp's header-name normalization.
	var names []string
	for _, key := range ctx.Response.Header.PeekTrailerKeys() {
		names = append(names, string(key))
	}
	ctx.Response.Header.VisitAll(func(k, _ []byte) {
		if errorResponseField(string(k)) {
			names = append(names, string(k))
		}
	})
	for _, name := range names {
		ctx.Response.Header.Del(name)
	}
	for _, name := range errorResponseFields {
		ctx.Response.Header.Del(name)
	}
	ctx.Response.ResetBody()
	ctx.SetContentType("text/plain; charset=utf-8")
	ctx.Response.Header.Set("Cache-Control", "no-store")
	ctx.SetStatusCode(status)
	ctx.SetBodyString(body)
}

// ResetHTTPErrorHeaders applies ResetError's header policy to net/http and
// hijacked handshake responses. The caller supplies the new type, status and
// body. Header maps may contain non-canonical keys or undeclared Go trailers.
func ResetHTTPErrorHeaders(h http.Header) {
	var trailers []string
	for name, values := range h {
		if strings.EqualFold(name, "Trailer") {
			for _, value := range values {
				for _, trailer := range strings.Split(value, ",") {
					trailers = append(trailers, strings.TrimSpace(trailer))
				}
			}
		}
	}
	for name := range h {
		remove := errorResponseField(name) || strings.HasPrefix(name, http.TrailerPrefix)
		if !remove {
			for _, trailer := range trailers {
				if strings.EqualFold(name, trailer) {
					remove = true
					break
				}
			}
		}
		if remove {
			delete(h, name)
		}
	}
	h.Set("Cache-Control", "no-store")
}

type timeoutHeadersKey struct{}
type header struct{ name, value string }

// TimeoutHeaders owns copies only. The timeout goroutine never reads RequestCtx
// or Response to obtain metadata after starting the business worker.
type TimeoutHeaders struct {
	mu     sync.Mutex
	values []header
	sealed bool
}

func TrackTimeoutHeaders(ctx *fasthttp.RequestCtx, snapshot *TimeoutHeaders) {
	ctx.SetUserValue(timeoutHeadersKey{}, snapshot)
	PublishTimeoutHeaders(ctx)
}

// PublishTimeoutHeaders must run on the goroutine owning the response. It is a
// no-op without Timeout. Built-in header middleware and RequestContext.SetHeader
// publish before entering downstream business code.
func PublishTimeoutHeaders(ctx *fasthttp.RequestCtx) {
	snapshot, _ := ctx.UserValue(timeoutHeadersKey{}).(*TimeoutHeaders)
	if snapshot == nil {
		return
	}
	snapshot.publish(ctx)
}

// PublishTimeoutHeader avoids rescanning all response fields for ordinary
// application headers that cannot appear in a timeout response.
func PublishTimeoutHeader(ctx *fasthttp.RequestCtx, name string) {
	snapshot, _ := ctx.UserValue(timeoutHeadersKey{}).(*TimeoutHeaders)
	if snapshot != nil && preserveHeader(name) {
		snapshot.publish(ctx)
	}
}

func (snapshot *TimeoutHeaders) publish(ctx *fasthttp.RequestCtx) {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.sealed {
		return
	}
	clear(snapshot.values)
	snapshot.values = snapshot.values[:0]
	ctx.Response.Header.VisitAll(func(k, v []byte) {
		if preserveHeader(string(k)) {
			snapshot.values = append(snapshot.values, header{string(k), string(v)})
		}
	})
}

func preserveHeader(name string) bool {
	for _, allowed := range []string{
		"X-Request-ID", "Vary", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials",
		"Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Access-Control-Max-Age",
		"X-Content-Type-Options", "X-Frame-Options", "X-XSS-Protection", "Strict-Transport-Security",
		"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Referrer-Policy", "Permissions-Policy",
		"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy", "Cross-Origin-Resource-Policy",
	} {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	return false
}

// Apply seals the snapshot and writes only to the independent error response.
// Later publications cannot mutate a response already handed to the server.
func (snapshot *TimeoutHeaders) Apply(h *fasthttp.ResponseHeader) {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	snapshot.sealed = true
	for _, item := range snapshot.values {
		h.Del(item.name)
	}
	for _, item := range snapshot.values {
		h.Add(item.name, item.value)
	}
	h.Set("Cache-Control", "no-store")
}
