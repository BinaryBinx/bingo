package core

import (
	"errors"
	"strings"
)

// ErrRoutesFrozen means serving or shutdown has begun. Routes are immutable
// after that boundary, so the request path needs no routing lock.
var ErrRoutesFrozen = errors.New("bingo: routes are frozen after serving or shutdown begins")

// Handle registers a route before serving and returns ErrRoutesFrozen afterwards.
// As with the underlying router, invalid patterns and duplicate routes panic.
// Registration is serialized with other registrations and server startup.
func (app *App) Handle(method, path string, handler RequestHandler) error {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	if app.started || app.shuttingDown {
		return ErrRoutesFrozen
	}
	app.router.Handle(method, path, app.wrapHandler(handler))
	return nil
}

func (app *App) mustHandle(method, path string, handler RequestHandler) {
	if err := app.Handle(method, path, handler); err != nil {
		panic(err)
	}
}

// Handle registers a route in this group with the same startup-only contract.
func (g *RouterGroup) Handle(method, path string, handler RequestHandler) error {
	return g.app.Handle(method, joinGroupPath(g.prefix, path), handler)
}

// joinGroupPath normalizes only the boundary between a group and its child.
// Do not use path.Join: cleaning dot segments or inner slashes can change route
// patterns. An empty child retains the prefix, while "/" requests its slash form.
func joinGroupPath(prefix, child string) string {
	if prefix == "" {
		return "/" + strings.TrimLeft(child, "/")
	}
	if child == "" {
		return prefix
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(child, "/")
}
