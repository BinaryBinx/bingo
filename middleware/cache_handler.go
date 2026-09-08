package middleware

import (
	"errors"
	"hash/maphash"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/valyala/fasthttp"
)

// CacheHandler owns a bounded public-response cache and its invalidation state.
// Construct it with NewCacheHandler; do not copy it after use. Fixed stripes
// fence old origin work; the URL index contains retained entries only and is
// accounted for in snapshot capacity. Close releases snapshots and timers.
type CacheHandler struct {
	config      CacheConfig
	cache       *boundedCache[responseCacheKey, responseSnapshot]
	flights     responseFlights
	seed        maphash.Seed
	stripes     [256]cacheInvalidationStripe
	lifecycleMu sync.Mutex
	closed      atomic.Bool
}

var ErrCacheClosed = errors.New("bingo: response cache is closed")

type cacheInvalidationStripe struct {
	mu         sync.Mutex
	generation atomic.Uint64
}

// NewCacheHandler exposes explicit invalidation while retaining CacheWithConfig
// defaults. All Middleware calls on this instance share snapshots and flights.
func NewCacheHandler(cfg CacheConfig) *CacheHandler {
	cfg.CoalesceHeaders = normalizeCoalesceHeaders(cfg.CoalesceHeaders)
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 10000
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 64 << 20
	}
	if cfg.MaxEntryBytes <= 0 {
		cfg.MaxEntryBytes = 1 << 20
	}
	cache := newBoundedCache[responseCacheKey, responseSnapshot](cfg.MaxEntries, cfg.MaxBytes)
	cache.groupOf = func(key responseCacheKey) cacheGroupKey { return cacheGroupKey{key.host, key.uri} }
	return &CacheHandler{
		config:  cfg,
		cache:   cache,
		flights: responseFlights{maxEntries: min(cfg.MaxEntries, 256), maxBytes: min(cfg.MaxBytes, 1<<20), headers: cfg.CoalesceHeaders},
		seed:    maphash.MakeSeed(),
	}
}

// Invalidate removes every cached representation of host and requestURI and
// wakes its coalesced requests. host is the request Host, including any port;
// requestURI is an exact origin-form path and query (for example /items?id=1),
// never an absolute URL or a fragment. Other query strings are separate targets.
// Absolute-form HTTP requests use the same key as their origin-form target;
// escaping, dot segments and query spelling are otherwise preserved exactly.
// Call after committing a write that occurs outside this cache's middleware.
// Existing origin requests may finish for their own clients, but cannot refill
// invalidated snapshots. Host spelling is case-insensitive; ports remain exact.
func (c *CacheHandler) Invalidate(host, requestURI string) error {
	if !strings.HasPrefix(requestURI, "/") || strings.ContainsAny(requestURI, "#\r\n") {
		return errors.New("cache invalidation requires an origin-form path and query")
	}
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return errors.New("cache invalidation requires an origin-form path and query")
	}
	if strings.ContainsAny(host, "/?#@\\\r\n\t ") {
		return errors.New("cache invalidation requires a request Host")
	}
	if !c.invalidate(strings.ToLower(host), requestURI) {
		return ErrCacheClosed
	}
	return nil
}

func (c *CacheHandler) invalidationStripe(host, uri string) *cacheInvalidationStripe {
	key := struct{ host, uri string }{host, uri}
	return &c.stripes[maphash.Comparable(c.seed, key)%uint64(len(c.stripes))]
}

func (c *CacheHandler) invalidate(host, uri string) bool {
	stripe := c.invalidationStripe(host, uri)
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	if c.closed.Load() {
		return false
	}
	c.cache.deleteGroup(cacheGroupKey{host, uri})
	// Publish the new generation only after all old variants are gone. A lookup
	// spanning deletion fails its generation recheck rather than seeing an old
	// snapshot as part of the new generation.
	stripe.generation.Add(1)
	c.flights.invalidate(host, uri)
	return true
}

// Clear discards all snapshots and wakes waiters. The cache remains usable, but
// requests started before Clear cannot refill it with their old results.
func (c *CacheHandler) Clear() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed.Load() {
		return ErrCacheClosed
	}
	c.clear(false)
	return nil
}

// Close permanently disables this shared cache and releases its timers, index
// and snapshots. Middleware subsequently calls next directly. In-flight handlers
// may finish but cannot publish old results. Repeated Close calls wait for the
// same cleanup and succeed. Register it with OnShutdown when the owner stops.
func (c *CacheHandler) Close() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed.Load() {
		return nil
	}
	c.closed.Store(true)
	c.clear(true)
	return nil
}

func (c *CacheHandler) clear(closing bool) {
	// Publication and per-URL invalidation hold one stripe before the store.
	// Acquire every stripe in a fixed order to fence all outstanding results.
	for i := range c.stripes {
		c.stripes[i].mu.Lock()
	}
	defer func() {
		for i := len(c.stripes) - 1; i >= 0; i-- {
			c.stripes[i].mu.Unlock()
		}
	}()
	c.cache.clear()
	for i := range c.stripes {
		c.stripes[i].generation.Add(1)
	}
	c.flights.clear(closing)
}

// Safe methods do not change representations. Unknown methods are conservatively
// treated as unsafe, as required by RFC 9111 section 4.4.
func unsafeCacheMethod(method string) bool {
	switch method {
	case fasthttp.MethodGet, fasthttp.MethodHead, fasthttp.MethodOptions, fasthttp.MethodTrace:
		return false
	default:
		return true
	}
}

// Strip only the scheme and authority from absolute-form request targets.
// fasthttp.URI.RequestURI also normalizes/decodes paths, which would merge
// previously isolated escaped targets. net/url preserves RawPath and RawQuery.
func cacheRequestTarget(ctx *fasthttp.RequestCtx) string {
	target := string(ctx.RequestURI())
	if strings.HasPrefix(target, "/") {
		return target
	}
	parsed, err := url.ParseRequestURI(target)
	if err == nil && parsed.IsAbs() && parsed.Host != "" {
		return parsed.RequestURI()
	}
	return target
}

func (c *CacheHandler) handleWrite(ctx *fasthttp.RequestCtx, next fasthttp.RequestHandler) {
	guard := guardResponse(ctx)
	// Capture before next: routing or application code may rewrite the request.
	host, uri := strings.ToLower(string(ctx.Host())), cacheRequestTarget(ctx)
	base, _ := url.ParseRequestURI(uri)
	if base != nil {
		base.Scheme, base.Host = string(ctx.URI().Scheme()), host
	}
	next(ctx)
	// Timeout can return while its worker still owns Response. Inside
	// the worker, a successful late write must invalidate even after cancellation.
	if guard.worker == nil && guard.abandoned(ctx) {
		return
	}
	if ctx.Hijacked() {
		return
	}
	status := ctx.Response.StatusCode()
	if status < 200 || status >= 400 {
		return
	}
	if !c.invalidate(host, uri) {
		return
	}
	if base == nil {
		return
	}
	for _, header := range []string{"Location", "Content-Location"} {
		for _, value := range ctx.Response.Header.PeekAll(header) {
			reference, err := url.Parse(string(value))
			if err != nil || reference.User != nil {
				continue
			}
			target := base.ResolveReference(reference)
			// A response cannot evict another origin's entries through redirects.
			if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
				continue
			}
			targetURI := target.RequestURI()
			if targetURI != uri {
				c.invalidate(host, targetURI)
			}
		}
	}
}
