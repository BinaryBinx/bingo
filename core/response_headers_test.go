package core

import (
	"sync"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/middleware"
	"github.com/valyala/fasthttp"
)

func TestHandlerRecoveryDropsOldIntegrityHeaders(t *testing.T) {
	app := reviewLatestApp(t)
	app.GET("/", func(c *RequestContext) {
		c.SetHeader("Content-Digest", "old")
		c.SetHeader("Signature", "old")
		panic("after signing")
	})
	var c fasthttp.RequestCtx
	defer c.Response.Reset()
	c.Request.SetRequestURI("/")
	app.handleRequest(&c)
	if c.Response.StatusCode() != 500 || len(c.Response.Header.Peek("Content-Digest")) != 0 || len(c.Response.Header.Peek("Signature")) != 0 {
		t.Fatal("invalid recovery response")
	}
}

func TestSetHeaderPublishesTimeoutMetadata(t *testing.T) {
	app := reviewLatestApp(t)
	app.Use(middleware.Timeout(20 * time.Millisecond))
	release, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); <-done }()
	app.GET("/", func(c *RequestContext) {
		defer close(done)
		c.SetHeader("X-Request-ID", "core-request")
		c.SetHeader("Access-Control-Allow-Origin", "https://client.test")
		c.SetHeader("Content-Digest", "not-safe")
		<-release
	})
	uri := servePhaseApp(t, app)
	var req fasthttp.Request
	var resp fasthttp.Response
	defer resp.Reset()
	req.SetRequestURI(uri)
	req.Header.SetConnectionClose()
	if err := fasthttp.DoTimeout(&req, &resp, time.Second); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != 408 || string(resp.Header.Peek("X-Request-ID")) != "core-request" || string(resp.Header.Peek("Access-Control-Allow-Origin")) != "https://client.test" || len(resp.Header.Peek("Content-Digest")) != 0 {
		t.Fatal("invalid timeout metadata")
	}
}
