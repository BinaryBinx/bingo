package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestSendErrorReplacesRepresentationAndClosesOldStream(t *testing.T) {
	for _, normalize := range []bool{true, false} {
		t.Run(fmt.Sprint(normalize), func(t *testing.T) {
			c := &RequestContext{RequestCtx: &fasthttp.RequestCtx{}}
			defer c.Response.Reset()
			if !normalize {
				c.Response.Header.DisableNormalizing()
			}
			stale := []string{"Content-Encoding", "Content-Digest", "Repr-Digest", "Digest", "Content-MD5", "Signature", "Signature-Input", "ETag", "Last-Modified", "Content-Range", "Content-Location", "Location", "Set-Cookie", "Age", "Expires", "Accept-Ranges"}
			for _, name := range stale {
				if !normalize {
					name = strings.ToLower(name)
				}
				c.SetHeader(name, "old")
			}
			c.Response.Header.AddTrailer("X-Checksum")
			c.SetHeader("X-Checksum", "old")
			c.SetHeader("Cache-Control", "public, max-age=3600")
			public := map[string]string{
				"X-Request-ID": "request-id", "Access-Control-Allow-Origin": "https://client.test",
				"X-Content-Type-Options": "nosniff", "WWW-Authenticate": "Bearer", "Retry-After": "2",
			}
			for name, value := range public {
				c.SetHeader(name, value)
			}
			stream := &jsonBodyStream{Reader: strings.NewReader("old stream")}
			c.SetBodyStream(stream, -1)
			c.SendError(503, errors.New("unavailable"), "try later")
			if !stream.closed || stream.reads != 0 || c.Response.IsBodyStream() {
				t.Fatal("error response consumed or retained the old stream")
			}
			var body ErrorResponse
			if err := json.Unmarshal(c.Response.Body(), &body); err != nil {
				t.Fatal(err)
			}
			if c.Response.StatusCode() != 503 || body != (ErrorResponse{Error: "unavailable", Message: "try later", Code: 503}) || string(c.Response.Header.ContentType()) != "application/json" {
				t.Fatalf("invalid JSON error: %+v", body)
			}
			for name, value := range public {
				if string(c.Response.Header.Peek(name)) != value {
					t.Errorf("lost %s", name)
				}
			}
			for key := range c.Response.Header.All() {
				for _, name := range append(stale, "Trailer", "X-Checksum") {
					if strings.EqualFold(string(key), name) {
						t.Errorf("retained %s", key)
					}
				}
			}
			if len(c.Response.Header.PeekTrailerKeys()) != 0 || string(c.Response.Header.Peek("Cache-Control")) != "no-store" {
				t.Fatal("error kept trailers or a cacheable policy")
			}
		})
	}
}

func TestSendErrorAcceptsNilCause(t *testing.T) {
	c := &RequestContext{RequestCtx: &fasthttp.RequestCtx{}}
	defer c.Response.Reset()
	c.SendError(400, nil, "invalid input")
	var body ErrorResponse
	if err := json.Unmarshal(c.Response.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "unknown error" || body.Message != "invalid input" || body.Code != 400 {
		t.Fatalf("unexpected nil-cause response: %+v", body)
	}
}
