package middleware

import (
	"bytes"

	"github.com/valyala/fasthttp"
)

// A cached response gains Age/X-Cache and may get a fresh request ID. Replaying
// a message signature can therefore invalidate it even when the body is intact.
// Leave signed responses untouched; applications can sign after Cache returns
// if signatures should also be generated for cache hits. Body digests alone do
// not require this bypass, since snapshots preserve the representation bytes.
func hasResponseSignature(h *fasthttp.ResponseHeader) bool {
	for name := range h.All() {
		if isResponseSignatureField(name) {
			return true
		}
	}
	for _, name := range h.PeekTrailerKeys() {
		if isResponseSignatureField(name) {
			return true
		}
	}
	return false
}

func isResponseSignatureField(name []byte) bool {
	return bytes.EqualFold(name, []byte("Signature")) || bytes.EqualFold(name, []byte("Signature-Input"))
}
