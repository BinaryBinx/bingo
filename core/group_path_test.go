package core

import (
	"fmt"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestGroupPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		prefix, child, want string
	}{
		{"/api", "/items", "/api/items"},
		{"/api/", "/items", "/api/items"},
		{"/api", "items", "/api/items"},
		{"api///", "///items/", "/api/items/"},
		{"/api", "", "/api"},
		{"/api/", "", "/api/"},
		{"/api", "/", "/api/"},
		{"", "", "/"},
		{"/", "items", "/items"},
		{"", "/items", "/items"},
		{"/api", "/items//child", "/api/items//child"},
		{"/api", "/items/../child", "/api/items/../child"},
	} {
		t.Run(fmt.Sprintf("%s+%s", tc.prefix, tc.child), func(t *testing.T) {
			app := reviewLatestApp(t)
			app.router.RedirectTrailingSlash = false
			app.router.RedirectFixedPath = false
			group := app.Group(tc.prefix)
			leaf := func(c *RequestContext) { c.String(200, "grouped") }
			for _, register := range []func(string, RequestHandler){group.GET, group.POST, group.PUT, group.DELETE, group.PATCH, group.HEAD, group.OPTIONS} {
				register(tc.child, leaf)
			}
			if err := group.Handle("CUSTOM", tc.child, leaf); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "CUSTOM"} {
				var c fasthttp.RequestCtx
				c.Request.SetRequestURI("http://example.test" + tc.want)
				c.Request.Header.SetMethod(method)
				app.handleRequest(&c)
				if c.Response.StatusCode() != 200 || string(c.Response.Body()) != "grouped" {
					t.Errorf("%s %s returned %d %q", method, tc.want, c.Response.StatusCode(), c.Response.Body())
				}
				c.Response.Reset()
			}
		})
	}
}

func TestNestedGroupsKeepPatterns(t *testing.T) {
	app := reviewLatestApp(t)
	group := app.Group("/api/").Group("v1/").Group("/users")
	group.GET("", func(c *RequestContext) { c.String(200, "without slash") })
	group.GET("/{id:[0-9]+}", func(c *RequestContext) { c.String(200, "%s", c.GetParam("id")) })
	group.Group("/{id:[0-9]+}/").GET("files/{path:*}", func(c *RequestContext) {
		c.String(200, "%s:%s", c.GetParam("id"), c.GetParam("path"))
	})
	for path, body := range map[string]string{
		"/api/v1/users":    "without slash",
		"/api/v1/users/42": "42", "/api/v1/users/42/files/a/b.txt": "42:a/b.txt",
	} {
		var c fasthttp.RequestCtx
		c.Request.SetRequestURI("http://example.test" + path)
		app.handleRequest(&c)
		if c.Response.StatusCode() != 200 || string(c.Response.Body()) != body {
			t.Errorf("%s returned %d %q, want %q", path, c.Response.StatusCode(), c.Response.Body(), body)
		}
		c.Response.Reset()
	}
}
