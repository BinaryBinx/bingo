package middleware

import (
	"crypto/sha256"
	"slices"
	"strings"
	"sync"

	"github.com/valyala/fasthttp"
)

// Cold responses have no known Vary schema. Complete headers are the default
// coordination key; callers may explicitly select stable dimensions instead.
// After waking, each request performs its own Vary-aware cache lookup.
// Uncacheable/error/expired responses are never shared.
// The leader runs in its own request goroutine and never lends RequestCtx to a
// waiter. Each waiter can leave independently when its own context is canceled.
type responseFlights struct {
	mu         sync.Mutex
	items      map[responseFlightKey]*responseFlight
	bytes      int64
	maxBytes   int64
	maxEntries int
	headers    []string
	closed     bool
}

type responseFlightKey struct {
	base       responseCacheKey
	headers    string
	generation uint64
}

type responseFlight struct {
	key  responseFlightKey
	done chan struct{}
	cost int64
}

func (g *responseFlights) join(base responseCacheKey, header *fasthttp.RequestHeader) (*responseFlight, bool) {
	return g.joinGeneration(base, header, 0)
}

func (g *responseFlights) joinGeneration(base responseCacheKey, header *fasthttp.RequestHeader, generation uint64) (*responseFlight, bool) {
	// Keep admission based on the complete size, even when a long key is stored
	// as a digest. Waiters always recheck the actual Vary representation.
	var dimensions string
	var size int
	if g.headers == nil {
		raw := header.Header()
		size = len(raw)
		if int64(size) > g.maxBytes {
			return nil, false
		}
		if size >= sha256.Size {
			sum := sha256.Sum256(raw)
			dimensions = string(sum[:])
		} else {
			dimensions = string(raw)
		}
	} else {
		dimensions = cacheVariant(g.headers, header)
		size = len(dimensions)
	}
	cost := int64(size + len(base.scheme) + len(base.host) + len(base.uri) + len(base.encoding) + len(base.origin) + 288)
	if cost > g.maxBytes {
		return nil, false
	}
	key := responseFlightKey{base, dimensions, generation}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, false
	}
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

// Invalid field names retain conservative behavior. Copy the input so callers
// cannot change a live middleware's grouping rules through a shared slice.
func normalizeCoalesceHeaders(input []string) []string {
	if input == nil {
		return nil
	}
	fields := make([]string, 0, len(input))
	for _, name := range input {
		field := strings.ToLower(strings.TrimSpace(name))
		if field == "" {
			return nil
		}
		for _, c := range field {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return nil
			}
		}
		if !slices.Contains(fields, field) {
			fields = append(fields, field)
		}
	}
	slices.Sort(fields)
	return fields
}

func (g *responseFlights) finish(flight *responseFlight) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.items[flight.key] != flight {
		return
	}
	delete(g.items, flight.key)
	g.bytes -= flight.cost
	close(flight.done)
}

func (g *responseFlights) invalidate(host, uri string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, flight := range g.items {
		if key.base.host == host && key.base.uri == uri {
			delete(g.items, key)
			g.bytes -= flight.cost
			close(flight.done)
		}
	}
}

func (g *responseFlights) clear(closing bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, flight := range g.items {
		close(flight.done)
	}
	g.items, g.bytes = nil, 0
	g.closed = g.closed || closing
}
