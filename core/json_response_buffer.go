package core

import "github.com/valyala/fasthttp"

const (
	minReusableJSONBody = 32 << 10
	jsonBufferSizeStep  = 8 << 10
)

// Keep large wire buffers out of fasthttp's adaptive pool, whose retention
// depends on the sizes of neighboring responses. The idle cap is 8 MiB across
// all Apps and does not depend on the number of open connections.
var jsonResponseBuffers = make(chan *reusableJSONBody, 32)

type reusableJSONBodyKey struct{}
type recycleJSONBodyKey struct{}

// The marker is not a closer. Middleware may clear request values while the
// response is still live. Only the top-level handler registers the closer.
type pendingJSONBody struct{ buffer *reusableJSONBody }

type reusableJSONBody struct {
	data     []byte
	response *fasthttp.Response // armed only after the middleware chain returns
}

func setReusableJSONBody(ctx *fasthttp.RequestCtx, encoded []byte) {
	var body *reusableJSONBody
	select {
	case body = <-jsonResponseBuffers:
	default:
		body = new(reusableJSONBody)
	}
	if cap(body.data) < len(encoded) {
		// Round up so nearby response sizes reuse the same storage and small
		// appends by middleware can fit without detaching the buffer.
		size := (len(encoded) + jsonBufferSizeStep - 1) &^ (jsonBufferSizeStep - 1)
		body.data = make([]byte, 0, size)
	}
	body.data = body.data[:len(encoded)]
	copy(body.data, encoded)
	_ = ctx.Response.CloseBodyStream()
	ctx.Response.SwapBody(body.data)
	ctx.SetUserValue(reusableJSONBodyKey{}, pendingJSONBody{body})
}

func armJSONBodyRecycle(ctx *fasthttp.RequestCtx) {
	if pending, ok := ctx.UserValue(reusableJSONBodyKey{}).(pendingJSONBody); ok {
		pending.buffer.response = &ctx.Response
		// Register after middleware's own closers, so they can still observe
		// the response that was sent before its body is detached.
		ctx.SetUserValue(recycleJSONBodyKey{}, pending.buffer)
	}
}

func (b *reusableJSONBody) Close() error {
	// fasthttp closes request user values after writing the response, before
	// it resets the response body.
	resp := b.response
	b.response = nil
	if resp == nil || resp.IsBodyStream() {
		return nil
	}
	// Middleware may replace, expand or move the original body. Reclaim only
	// our own allocation, never a replacement or somebody else's stream.
	if body := resp.Body(); len(body) == 0 || &body[0] != &b.data[0] {
		return nil
	}
	owned := resp.SwapBody(nil)
	if len(owned) == 0 || &owned[0] != &b.data[0] || cap(owned) > maxJSONBuffer {
		return nil
	}
	b.data = owned[:0]
	select {
	case jsonResponseBuffers <- b:
	default:
	}
	return nil
}
