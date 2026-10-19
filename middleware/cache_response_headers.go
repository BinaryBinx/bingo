package middleware

import (
	"bytes"
	"iter"
	"strings"

	"github.com/valyala/fasthttp"
)

// PeekAll uses exact names when fasthttp header normalization is disabled.
// Cache policy still follows HTTP's case-insensitive field names, including
// duplicate fields with different spellings. Iterate without copying values or
// changing the origin response's header names and normalization setting.
func responseHeaderValues(h *fasthttp.ResponseHeader, name string) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for key, value := range h.All() {
			if bytes.EqualFold(key, []byte(name)) && !yield(value) {
				return
			}
		}
	}
}

// Singleton freshness fields must reject duplicates even across casing variants.
// Two is sufficient to distinguish an ambiguous field from a missing/single one.
func singleResponseHeader(h *fasthttp.ResponseHeader, name string) (value string, count int) {
	for current := range responseHeaderValues(h, name) {
		count++
		if count == 2 {
			break
		}
		value = string(current)
	}
	return
}

// Policy carried in trailers cannot safely authorize a shared snapshot. All
// iterates initial headers only; do not lose a no-store/Vary/freshness restriction
// stored in fasthttp's separate trailer section when inspecting field names.
func hasCachePolicyTrailer(h *fasthttp.ResponseHeader) bool {
	for name := range h.Trailers() {
		switch strings.ToLower(string(name)) {
		case "cache-control", "vary", "age", "date", "expires", "set-cookie", "connection":
			return true
		}
	}
	return false
}
