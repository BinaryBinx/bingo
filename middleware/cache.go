package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"

	"github.com/valyala/fasthttp"
)

// CacheConfig bounds retained snapshots, including keys and response headers.
// Zero capacity fields use defaults. Duration <= 0 disables caching.
type CacheConfig struct {
	Duration      time.Duration
	MaxEntries    int
	MaxBytes      int64
	MaxEntryBytes int64
	// CoalesceHeaders selects extra request-header dimensions for in-flight
	// coordination only. nil keeps the conservative complete-header key; an
	// explicit empty slice uses Host/URI/Accept-Encoding/Origin only. Waiting
	// requests still recheck the response's actual Vary before using a snapshot.
	CoalesceHeaders []string
}

type responseCacheKey struct{ host, uri, encoding, origin, variant string }

// Includes the intrusive links and a conservative per-entry share of the URL
// index, in addition to the snapshot, primary map and shard bookkeeping.
const responseCacheEntryOverhead = 416

type cacheHeader struct {
	key, value  string
	appendValue bool
}
type responseSnapshot struct {
	body      []byte
	headers   []cacheHeader
	stored    time.Time
	age       time.Duration
	vary      []string // A schema index, sharing the same bounded store as response bodies.
	requestID bool
}

// Cache caches public GET responses for at most duration. Default bounds are
// 10,000 entries, 64 MiB total snapshot data and 1 MiB per response snapshot.
func Cache(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return CacheWithConfig(CacheConfig{Duration: duration})
}

// CacheWithConfig creates a shared response cache. Successful unsafe requests
// automatically invalidate their target URI; use NewCacheHandler for explicit
// invalidation from application code.
func CacheWithConfig(cfg CacheConfig) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return NewCacheHandler(cfg).Middleware
}

// Middleware shares this cache across the handlers it wraps. Put it inside
// Timeout so completed writes can invalidate even after the client times out.
func (c *CacheHandler) Middleware(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	cfg, cache, flights := c.config, c.cache, &c.flights
	if cfg.Duration <= 0 {
		return next
	}
	return func(ctx *fasthttp.RequestCtx) {
		if c.closed.Load() {
			next(ctx)
			return
		}
		if !ctx.IsGet() {
			if unsafeCacheMethod(string(ctx.Method())) {
				c.handleWrite(ctx, next)
			} else {
				next(ctx)
			}
			return
		}
		if privateCacheRequest(&ctx.Request.Header) || hasResponseSignature(&ctx.Response.Header) {
			next(ctx)
			return
		}
		guard := guardResponse(ctx)
		key := responseCacheKey{strings.ToLower(string(ctx.Host())), cacheRequestTarget(ctx),
			strings.ToLower(joinHeaderValues(ctx.Request.Header.PeekAll("Accept-Encoding"))),
			string(ctx.Request.Header.Peek("Origin")), ""}
		stripe := c.invalidationStripe(key.host, key.uri)
		var generation uint64
		var started time.Time
		for {
			if c.closed.Load() {
				next(ctx)
				return
			}
			generation = stripe.generation.Load()
			started = time.Now()
			if item, ok := cachedResponse(cache, key, &ctx.Request.Header, started); ok && generation == stripe.generation.Load() {
				writeCachedResponse(ctx, item, started)
				return
			}
			flight, leader := flights.joinGeneration(key, &ctx.Request.Header, generation)
			if generation != stripe.generation.Load() {
				if leader {
					flights.finish(flight)
				}
				continue
			}
			if flight == nil {
				break
			}
			if !leader {
				workContext := requestcontext.From(ctx)
				select {
				case <-flight.done:
				case <-workContext.Done():
					resetErrorResponse(ctx, fasthttp.StatusRequestTimeout, "Request timeout")
					return
				}
				if workContext.Err() != nil {
					resetErrorResponse(ctx, fasthttp.StatusRequestTimeout, "Request timeout")
					return
				}
			}
			if generation != stripe.generation.Load() {
				if leader {
					flights.finish(flight)
				}
				continue
			}
			if leader {
				// finish is also safe after invalidation removed this flight.
				defer flights.finish(flight)
			}
			// Recheck the actual Vary schema. Uncacheable responses are never
			// transferred to waiters, which execute their own handler on a miss.
			started = time.Now()
			if item, ok := cachedResponse(cache, key, &ctx.Request.Header, started); ok && generation == stripe.generation.Load() {
				writeCachedResponse(ctx, item, started)
				return
			}
			break
		}
		// A handler can change request headers. Select representations from
		// the original request, including repeated and empty field lines.
		requestHeaders := requestHeaderSnapshots.get()
		defer requestHeaderSnapshots.put(requestHeaders)
		requestHeaders.copyFrom(&ctx.Request.Header)
		next(ctx)
		if guard.abandoned(ctx) || c.closed.Load() || ctx.Hijacked() {
			return
		}
		if ctx.Response.StatusCode() != fasthttp.StatusOK || ctx.Response.IsBodyStream() {
			return
		}
		now := time.Now()
		ttl, age := responseFreshness(&ctx.Response.Header, cfg.Duration, started, now)
		vary, cacheable := cacheVaryFields(&ctx.Response.Header)
		if ttl <= 0 || !cacheable {
			stripe.mu.Lock()
			if generation == stripe.generation.Load() {
				cache.delete(key)
			}
			stripe.mu.Unlock()
			return
		}
		variant := key
		if len(vary) != 0 {
			variant.variant = requestHeaders.variant(vary)
		}
		body := ctx.Response.Body()
		cost := int64(len(body) + len(key.host) + len(key.uri) + len(key.encoding) + len(key.origin) + len(variant.variant) + responseCacheEntryOverhead)
		if cost > cfg.MaxEntryBytes || cost > cfg.MaxBytes {
			return
		}
		var headers []cacheHeader
		seen := make(map[string]bool)
		ctx.Response.Header.VisitAll(func(k, v []byte) {
			name := string(k)
			if hopByHop(name) || strings.EqualFold(name, "X-Cache") || strings.EqualFold(name, "X-Request-ID") || strings.EqualFold(name, "Age") {
				return
			}
			headers = append(headers, cacheHeader{name, string(v), seen[name]})
			seen[name] = true
			cost += int64(len(k) + len(v) + 48)
		})
		if cost > cfg.MaxEntryBytes || cost > cfg.MaxBytes {
			return
		}
		item := responseSnapshot{body: append([]byte(nil), body...), headers: headers, stored: now, age: age, requestID: len(ctx.Response.Header.Peek("X-Request-ID")) > 0}
		// Serialize only publication and invalidation for this fixed stripe.
		// An old origin request may finish, but cannot repopulate a new epoch.
		stripe.mu.Lock()
		defer stripe.mu.Unlock()
		if generation != stripe.generation.Load() {
			return
		}
		if len(vary) != 0 {
			indexCost := int64(len(key.host) + len(key.uri) + len(key.encoding) + len(key.origin) + responseCacheEntryOverhead)
			for _, field := range vary {
				indexCost += int64(len(field) + 16)
			}
			if cfg.MaxEntries < 2 || indexCost > cfg.MaxEntryBytes || cost > cfg.MaxBytes-indexCost {
				return
			}
			// Including the schema in variant keys makes concurrent schema changes safe.
			// Eviction of either entry is a miss; no auxiliary unbounded index is retained.
			cache.put(variant, item, cost, now.Add(ttl))
			putVaryIndex(cache, key, vary, indexCost, now.Add(ttl))
		} else {
			cache.put(key, item, cost, now.Add(ttl))
		}
		ctx.Response.Header.Set("X-Cache", "MISS")
	}
}

func putVaryIndex(cache *boundedCache[responseCacheKey, responseSnapshot], key responseCacheKey, vary []string, cost int64, expires time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	// A short-lived variant must not hide longer-lived siblings. Keep the old
	// immutable index when it already covers this expiry; bodies still expire
	// independently. Changed schemas never inherit an unrelated index's TTL.
	if old := cache.items[key]; old != nil && slices.Equal(old.value.vary, vary) && !expires.After(old.expires) {
		return
	}
	cache.putLocked(key, responseSnapshot{vary: vary}, cost, expires)
}

func cachedResponse(cache *boundedCache[responseCacheKey, responseSnapshot], key responseCacheKey, h *fasthttp.RequestHeader, now time.Time) (responseSnapshot, bool) {
	item, ok := cache.get(key, now)
	if ok && len(item.vary) != 0 {
		key.variant = cacheVariant(item.vary, h)
		return cache.get(key, now)
	}
	return item, ok
}

func writeCachedResponse(ctx *fasthttp.RequestCtx, item responseSnapshot, now time.Time) {
	ctx.Response.ResetBody()
	ctx.SetStatusCode(fasthttp.StatusOK)
	// Cached snapshots are shared: responses must keep their own mutable body copy.
	ctx.SetBody(item.body)
	// Replace the first occurrence and append subsequent values, preserving outer headers.
	for _, h := range item.headers {
		if h.appendValue {
			ctx.Response.Header.Add(h.key, h.value)
		} else {
			ctx.Response.Header.Set(h.key, h.value)
		}
	}
	ctx.Response.Header.Set("Age", strconv.FormatInt(int64((item.age+now.Sub(item.stored))/time.Second), 10))
	if item.requestID && len(ctx.Response.Header.Peek("X-Request-ID")) == 0 {
		ctx.Response.Header.Set("X-Request-ID", generateRequestID())
	}
	ctx.Response.Header.Set("X-Cache", "HIT")
}

func privateCacheRequest(h *fasthttp.RequestHeader) bool {
	private, origins := false, 0
	// Scan once instead of ten full header lookups on every cache hit. EqualFold
	// also supports callers that explicitly disable fasthttp header normalization.
	h.VisitAll(func(key, value []byte) {
		name := string(key)
		if strings.EqualFold(name, "Origin") {
			origins++
		}
		if private || len(value) == 0 {
			return
		}
		for _, sensitive := range []string{"Authorization", "Cookie", "Cache-Control", "Pragma", "Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
			if strings.EqualFold(name, sensitive) {
				private = true
				return
			}
		}
	})
	return private || origins > 1
}

func joinHeaderValues(values [][]byte) string {
	if len(values) == 1 {
		return strings.TrimSpace(string(values[0]))
	}
	var b strings.Builder
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strings.TrimSpace(string(v)))
	}
	return b.String()
}

// A directive name is always checked, including directives with quoted field lists.
// Unknown extensions are ignored; ambiguous freshness values are rejected below.
func cacheDirectives(values [][]byte) map[string][]string {
	d := make(map[string][]string)
	for _, line := range values {
		for _, part := range strings.Split(string(line), ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			d[name] = append(d[name], strings.Trim(strings.TrimSpace(value), "\""))
		}
	}
	return d
}

func responseFreshness(h *fasthttp.ResponseHeader, limit time.Duration, started, now time.Time) (time.Duration, time.Duration) {
	if hasResponseSignature(h) || hasHeaderValue(h.PeekAll("Set-Cookie")) || hasHeaderValue(h.PeekAll("Connection")) {
		return 0, 0
	}
	d := cacheDirectives(h.PeekAll("Cache-Control"))
	for _, name := range []string{"private", "no-store", "no-cache"} {
		if len(d[name]) > 0 {
			return 0, 0
		}
	}
	age := time.Duration(0)
	if values := h.PeekAll("Age"); len(values) > 0 {
		if len(values) != 1 {
			return 0, 0
		}
		var ok bool
		age, ok = secondsDuration(string(values[0]))
		if !ok {
			return 0, 0
		}
	}
	// Account for response delay as well as any upstream Age / apparent age.
	if age > time.Duration(1<<63-1)-now.Sub(started) {
		return 0, 0
	}
	age += now.Sub(started)
	date := now
	if values := h.PeekAll("Date"); len(values) > 0 {
		if len(values) != 1 {
			return 0, 0
		}
		var err error
		date, err = http.ParseTime(string(values[0]))
		if err != nil {
			return 0, 0
		}
		if apparent := now.Sub(date); apparent > age {
			age = apparent
		}
	}
	freshness := limit
	values, explicit := d["s-maxage"]
	if !explicit {
		values, explicit = d["max-age"]
	}
	if explicit {
		if len(values) != 1 {
			return 0, 0
		}
		var ok bool
		freshness, ok = secondsDuration(values[0])
		if !ok {
			return 0, 0
		}
	} else if expires := h.PeekAll("Expires"); len(expires) > 0 {
		if len(expires) != 1 {
			return 0, 0
		}
		expiry, err := http.ParseTime(string(expires[0]))
		if err != nil {
			return 0, 0
		}
		freshness = expiry.Sub(date)
	}
	if freshness <= age {
		return 0, age
	}
	remaining := freshness - age
	if remaining > limit {
		remaining = limit
	}
	return remaining, age
}

func secondsDuration(v string) (time.Duration, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 || n > int64((1<<63-1)/time.Second) {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

func hopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

func hasHeaderValue(values [][]byte) bool {
	for _, v := range values {
		if len(v) > 0 {
			return true
		}
	}
	return false
}

// Common encoding/origin dimensions stay in the allocation-free base key.
// Other valid Vary fields use a bounded schema index only when requested by the origin.
func cacheVaryFields(h *fasthttp.ResponseHeader) ([]string, bool) {
	var fields []string
	for _, line := range h.PeekAll("Vary") {
		for _, raw := range strings.Split(string(line), ",") {
			field := strings.ToLower(strings.TrimSpace(raw))
			if field == "" {
				continue
			}
			if field == "*" || hopByHop(field) {
				return nil, false
			}
			for _, c := range field {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
					return nil, false
				}
			}
			if field != "accept-encoding" && field != "origin" && !slices.Contains(fields, field) {
				fields = append(fields, field)
			}
		}
	}
	slices.Sort(fields)
	return fields, true
}
func cacheVariant(fields []string, h *fasthttp.RequestHeader) string {
	var key strings.Builder
	for _, field := range fields {
		writeCacheVariantFrame(&key, field)
		h.VisitAll(func(k, v []byte) {
			if strings.EqualFold(string(k), field) {
				writeCacheVariantFrame(&key, string(v))
			}
		})
		key.WriteByte(';')
	}
	return key.String()
}
