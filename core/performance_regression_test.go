package core

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/middleware"
	"github.com/valyala/fasthttp"
)

func TestLazyParamsOverridesAndReuse(t *testing.T) {
	c := acquireRequestContext()
	c.RequestCtx = &fasthttp.RequestCtx{}
	c.SetUserValue("id", "route-id")
	buf := []byte("bytes")
	c.SetUserValue("byte-id", buf)
	c.SetUserValue("unrelated", 123)
	if c.GetParam("id") != "route-id" || c.GetParam("byte-id") != "bytes" || c.GetParam("unrelated") != "" {
		t.Fatal("incorrect lazy parameters")
	}
	owned := c.GetParam("byte-id")
	buf[0] = 'X'
	if owned != "bytes" {
		t.Fatal("byte parameter returned a mutable alias")
	}
	c.SetParam("id", "")
	if c.GetParam("id") != "" {
		t.Fatal("empty override ignored")
	}
	c.SetParam("id", "override")
	if c.GetParam("id") != "override" || c.UserValue("id") != "route-id" {
		t.Fatal("override changed router user values")
	}
	releaseRequestContext(c)
	c = acquireRequestContext()
	defer releaseRequestContext(c)
	c.RequestCtx = &fasthttp.RequestCtx{}
	if c.GetParam("id") != "" || c.GetParam("byte-id") != "" {
		t.Fatal("parameters leaked between requests")
	}
	var empty RequestContext
	empty.SetParam("id", "standalone")
	if empty.GetParam("id") != "standalone" {
		t.Fatal("zero-value context cannot set a parameter")
	}
}

type jsonBodyStream struct {
	io.Reader
	closed bool
	reads  int
}

func (s *jsonBodyStream) Read(p []byte) (int, error) { s.reads++; return s.Reader.Read(p) }

func (s *jsonBodyStream) Close() error { s.closed = true; return nil }

func TestJSONBodyOwnershipAndReplacement(t *testing.T) {
	c := &RequestContext{RequestCtx: &fasthttp.RequestCtx{}}
	stream := &jsonBodyStream{Reader: strings.NewReader("unused")}
	c.SetBodyStream(stream, -1)
	payload := struct {
		Text string `json:"text"`
	}{strings.Repeat("a", 65536)}
	if err := c.JSON(201, payload); err != nil {
		t.Fatal(err)
	}
	if !stream.closed || stream.reads != 0 || c.Response.IsBodyStream() || c.Response.StatusCode() != 201 {
		t.Fatal("JSON did not replace and close the stream")
	}
	first := append([]byte(nil), c.Response.Body()...)
	for i := 0; i < 50; i++ {
		var other fasthttp.Response
		other.SetBody(bytes.Repeat([]byte("z"), 70000))
		other.Reset()
	}
	if !bytes.Equal(first, c.Response.Body()) || !json.Valid(c.Response.Body()) {
		t.Fatal("JSON storage was reused while response was live")
	}
	c.Response.AppendBodyString(" ")
	if !json.Valid(c.Response.Body()) {
		t.Fatal("middleware cannot append to raw JSON body")
	}
	c.Response.Reset()
	if err := c.JSON(200, map[string]string{"next": "request"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(c.Response.Body()), strings.Repeat("a", 100)) {
		t.Fatal("JSON leaked previous response")
	}
	c.Response.Reset()
}

func TestCacheOutsideCompressSkipsRepeatedEncoding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RunMode = RunModeRelease
	app := NewApp(cfg)
	app.Use(middleware.Cache(time.Minute))
	compressCalls := 0
	app.Use(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(c *fasthttp.RequestCtx) { compressCalls++; next(c) }
	})
	app.Use(middleware.Compress())
	payload := struct {
		Text string `json:"text"`
	}{strings.Repeat("data", 4096)}
	app.GET("/json", func(c *RequestContext) {
		if err := c.JSON(200, payload); err != nil {
			t.Error(err)
		}
	})
	for _, encoding := range []string{"gzip", "gzip", "", ""} {
		var c fasthttp.RequestCtx
		c.Request.SetRequestURI("/json")
		c.Request.Header.Set("Accept-Encoding", encoding)
		app.handleRequest(&c)
		body := c.Response.Body()
		if encoding == "gzip" {
			var err error
			body, err = c.Response.BodyGunzip()
			if err != nil {
				t.Fatal(err)
			}
		}
		var actual struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &actual); err != nil || actual != payload {
			t.Fatal("cached compressed JSON changed")
		}
		c.Response.Reset()
	}
	if compressCalls != 2 {
		t.Fatalf("compression chain entered %d times; want once per encoding", compressCalls)
	}
}
