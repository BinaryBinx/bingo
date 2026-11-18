package core

import (
	"strings"
	"testing"

	"github.com/BinaryBinx/bingo/middleware"
	"github.com/valyala/fasthttp"
)

type observeJSONCleanup func()

func (f observeJSONCleanup) Close() error {
	f()
	return nil
}

func TestLargeJSONBufferLifetime(t *testing.T) {
	if nativeJSONDefault == nil {
		t.Skip("native Sonic required")
	}
	payload := strings.Repeat("a", 64<<10)
	for _, mode := range []string{"append", "gzip", "request-reset", "replacement", "stream"} {
		t.Run(mode, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.RunMode = RunModeRelease
			app := NewApp(cfg)
			stream := &jsonBodyStream{Reader: strings.NewReader("replacement stream")}
			if mode == "gzip" {
				app.Use(middleware.Compress())
			}
			appended := `"` + payload + `" `
			app.Use(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
				return func(ctx *fasthttp.RequestCtx) {
					next(ctx)
					if ctx.Response.IsBodyStream() || len(ctx.Response.Body()) != len(payload)+2 {
						t.Fatal("middleware did not receive a buffered JSON body")
					}
					switch mode {
					case "append":
						ctx.Response.AppendBodyString(" ")
						ctx.SetUserValue("observer", observeJSONCleanup(func() {
							if string(ctx.Response.Body()) != appended {
								t.Error("user cleanup did not see the finalized response")
							}
						}))
					case "request-reset":
						ctx.Request.Reset()
					case "replacement":
						ctx.Response.SwapBody([]byte("new body"))
					case "stream":
						ctx.Response.SetBodyStream(stream, -1)
					}
				}
			})
			app.GET("/", func(c *RequestContext) {
				if err := c.JSON(200, payload); err != nil {
					t.Fatal(err)
				}
			})

			var ctx fasthttp.RequestCtx
			var req fasthttp.Request
			req.SetRequestURI("/")
			if mode == "gzip" {
				req.Header.Set("Accept-Encoding", "gzip")
			}
			ctx.Init(&req, nil, nil)
			defer ctx.Response.Reset()
			app.handleRequest(&ctx)
			if mode == "append" && string(ctx.Response.Body()) != appended {
				t.Fatal("appended body changed before writing")
			}
			if mode == "gzip" {
				body, err := ctx.Response.BodyGunzip()
				if err != nil || string(body) != `"`+payload+`"` {
					t.Fatalf("compressed JSON changed before writing: %v", err)
				}
			}
			// Mirrors fasthttp's reset order after writing the response.
			ctx.Request.Reset()
			switch mode {
			case "append", "gzip":
				if len(ctx.Response.Body()) != 0 {
					t.Fatal("written JSON was not detached for reuse")
				}
			case "request-reset":
				if string(ctx.Response.Body()) != `"`+payload+`"` {
					t.Fatal("request reset reclaimed a response before it could be written")
				}
			case "replacement":
				if string(ctx.Response.Body()) != "new body" {
					t.Fatal("recycling touched a replacement body")
				}
			case "stream":
				if ctx.Response.BodyStream() != stream || stream.closed || stream.reads != 0 {
					t.Fatal("recycling read or closed a replacement stream")
				}
			}
		})
	}
}
