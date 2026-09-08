package core

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

type failingJSONValue struct{}

func (failingJSONValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("application serialization failed")
}

func TestJSONSerializationErrorPreservesResponse(t *testing.T) {
	for name, value := range map[string]any{
		"unsupported": make(chan int),
		"nan":         math.NaN(),
		"custom":      failingJSONValue{},
	} {
		t.Run(name, func(t *testing.T) {
			c := &RequestContext{RequestCtx: &fasthttp.RequestCtx{}}
			defer c.Response.Reset()
			c.String(202, "old body")
			c.SetHeader("ETag", `"old"`)
			c.SetHeader("X-Request-ID", "original-id")
			if err := c.JSON(201, value); err == nil {
				t.Fatal("expected a serialization error")
			}
			if c.Response.StatusCode() != 202 || string(c.Response.Header.ContentType()) != "text/plain; charset=utf-8" || string(c.Response.Body()) != "old body" {
				t.Fatal("serialization error partially changed the response")
			}
			if string(c.Response.Header.Peek("ETag")) != `"old"` || string(c.Response.Header.Peek("X-Request-ID")) != "original-id" {
				t.Fatal("serialization error changed existing headers")
			}
		})
	}
}

func TestJSONSerializationErrorDoesNotConsumeOrCloseStream(t *testing.T) {
	c := &RequestContext{RequestCtx: &fasthttp.RequestCtx{}}
	defer c.Response.Reset()
	stream := &jsonBodyStream{Reader: strings.NewReader("existing stream")}
	c.SetStatusCode(202)
	c.SetContentType("text/event-stream")
	c.SetBodyStream(stream, -1)
	if err := c.JSON(201, failingJSONValue{}); err == nil {
		t.Fatal("expected a serialization error")
	}
	if c.Response.StatusCode() != 202 || string(c.Response.Header.ContentType()) != "text/event-stream" || c.Response.BodyStream() != stream || stream.closed || stream.reads != 0 {
		t.Fatal("serialization error disturbed the existing stream")
	}
}
