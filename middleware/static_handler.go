package middleware

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/valyala/fasthttp"
)

// StaticHandler owns a reusable directory handle and a bounded snapshot cache.
// Register Close with the application's shutdown lifecycle. Calls to Middleware,
// Reload and Close are safe concurrently. Do not copy a StaticHandler.
type StaticHandler struct {
	mu       sync.RWMutex
	root     *os.Root
	rootPath string
	config   StaticConfig
	cache    *boundedCache[string, staticSnapshot]
	closed   bool
	closeErr error
}

// NewStaticHandler opens root and applies the same cache limits as Static.
// Construct handlers here rather than using a zero-value StaticHandler.
func NewStaticHandler(root string, cfg StaticConfig) (*StaticHandler, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(rootAbs)
	if err != nil {
		return nil, err
	}
	cfg = normalizeStaticConfig(cfg)
	return &StaticHandler{root: r, rootPath: rootAbs, config: cfg, cache: newBoundedCache[string, staticSnapshot](cfg.MaxEntries, cfg.MaxBytes)}, nil
}

func (s *StaticHandler) Middleware(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !ctx.IsGet() && !ctx.IsHead() {
			next(ctx)
			return
		}
		handled := func() bool {
			s.mu.RLock()
			defer s.mu.RUnlock()
			if s.closed || s.root == nil {
				resetErrorResponse(ctx, fasthttp.StatusServiceUnavailable, "Static resources closed")
				return true
			}
			return serveStatic(ctx, s.root, s.config, s.cache)
		}()
		// Downstream handlers may be long-lived or themselves call Reload/Close.
		// They must never execute while holding the static generation lock.
		if !handled {
			next(ctx)
		}
	}
}

// Reload switches to a newly opened root and an empty cache. Empty root reopens
// the configured path, e.g. after a deployment replaces a directory/symlink.
// Failure to open the new root leaves the current generation intact. Already
// opened response streams remain readable across Reload and Close.
func (s *StaticHandler) Reload(root string) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return os.ErrClosed
	}
	if root == "" {
		root = s.rootPath
	}
	s.mu.RUnlock()
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(rootAbs)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		r.Close()
		return os.ErrClosed
	}
	oldRoot, oldCache := s.root, s.cache
	s.root, s.rootPath = r, rootAbs
	s.cache = newBoundedCache[string, staticSnapshot](s.config.MaxEntries, s.config.MaxBytes)
	if oldCache != nil {
		oldCache.clear()
	}
	if oldRoot != nil {
		return oldRoot.Close()
	}
	return nil
}

// Close stops snapshot expiry timers, releases cached data and closes Root.
// It waits for file lookup/read operations; sending an already opened stream is
// owned by fasthttp. New static GET/HEAD requests receive 503 after closing.
func (s *StaticHandler) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.cache != nil {
		s.cache.clear()
		s.cache = nil
	}
	if s.root != nil {
		s.closeErr = s.root.Close()
		s.root = nil
	}
	return s.closeErr
}
