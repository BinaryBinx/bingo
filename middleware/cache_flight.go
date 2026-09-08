package middleware

import (
	"sync"

	"github.com/valyala/fasthttp"
)

// Cold responses have no known Vary schema. Only requests with identical full
// headers may wait together; after waking, each request performs its own normal
// Vary-aware cache lookup. Uncacheable/error/expired responses are never shared.
// The leader runs in its own request goroutine and never lends RequestCtx to a
// waiter. Each waiter can leave independently when its own context is canceled.
type responseFlights struct {
	mu         sync.Mutex
	items      map[responseFlightKey]*responseFlight
	bytes      int64
	maxBytes   int64
	maxEntries int
}

type responseFlightKey struct {
	base    responseCacheKey
	headers string
}

type responseFlight struct {
	key  responseFlightKey
	done chan struct{}
	cost int64
}

func (g *responseFlights) join(base responseCacheKey, header *fasthttp.RequestHeader) (*responseFlight, bool) {
	raw := header.Header()
	cost := int64(len(raw) + len(base.host) + len(base.uri) + len(base.encoding) + len(base.origin) + 256)
	if cost > g.maxBytes {
		return nil, false
	}
	key := responseFlightKey{base, string(raw)}
	g.mu.Lock()
	defer g.mu.Unlock()
	if flight := g.items[key]; flight != nil {
		return flight, false
	}
	if len(g.items) >= g.maxEntries || cost > g.maxBytes-g.bytes {
		return nil, false
	}
	if g.items == nil {
		g.items = make(map[responseFlightKey]*responseFlight)
	}
	flight := &responseFlight{key: key, done: make(chan struct{}), cost: cost}
	g.items[key] = flight
	g.bytes += cost
	return flight, true
}

func (g *responseFlights) finish(flight *responseFlight) {
	g.mu.Lock()
	delete(g.items, flight.key)
	g.bytes -= flight.cost
	close(flight.done)
	g.mu.Unlock()
}
