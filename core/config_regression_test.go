package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/middleware"
	"github.com/valyala/fasthttp"
)

func TestConfigurationIsFrozenAndReleasePreservesLimits(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RunMode = RunModeRelease
	app := NewApp(cfg)
	defer app.Shutdown()
	if app.GetMaxRequestBodySize() != 4<<20 || app.GetMultiCoreConfig().MaxConns != 10000 || app.GetReadTimeout() != 30*time.Second {
		t.Fatal("release silently changed explicit limits")
	}
	cfg.ReadTimeout = time.Second
	snapshot := app.GetConfig()
	snapshot.ReadTimeout = 2 * time.Second
	if app.server.ReadTimeout != 30*time.Second || app.GetReadTimeout() != 30*time.Second {
		t.Fatal("caller mutated effective config")
	}
	app.SetRunMode(RunModeTest)
	if app.GetConfig().RunMode != RunModeTest {
		t.Fatal("snapshot lost current mode")
	}
	if app.wsUpgrader != nil {
		t.Fatal("unused upgrader should be lazy")
	}
}

func TestConfigEnvFailureIsAtomic(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"BINGO_PORT", "70000"}, {"BINGO_RUN_MODE", "typo"}, {"BINGO_READ_TIMEOUT", "-1"}, {"BINGO_READ_TIMEOUT", "9223372037"}, {"BINGO_MAX_BODY_SIZE", "0"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			cfg := DefaultConfig()
			before := *cfg
			t.Setenv("BINGO_HOST", "127.0.0.1")
			t.Setenv(tc.name, tc.value)
			if err := LoadConfigFromEnv(cfg); err == nil {
				t.Fatal("expected invalid config error")
			}
			if *cfg != before {
				t.Fatal("failed load partially committed config")
			}
		})
	}
}

func TestConfigFileValidation(t *testing.T) {
	for _, body := range []string{"null", `{"port":70000}`, `{"run_mode":"typo"}`, `{"read_timeout":-1}`} {
		file := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(file); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := SaveConfig(nil, filepath.Join(t.TempDir(), "config.json")); err == nil {
		t.Fatal("saved null config")
	}
}

func TestBindJSONDoesNotRetainLargeIgnoredFields(t *testing.T) {
	decode := func(n int) []string {
		names := make([]string, 0, n)
		for i := 0; i < n; i++ {
			raw := &fasthttp.RequestCtx{}
			raw.Request.SetBodyString(`{"name":"small","ignored":"` + strings.Repeat("x", 1<<20) + `"}`)
			var value struct{ Name string }
			ctx := &RequestContext{RequestCtx: raw}
			if err := ctx.BindJSON(&value); err != nil {
				t.Fatal(err)
			}
			names = append(names, value.Name)
		}
		return names
	}
	decode(1) // Compile the exact schema before measuring retained heap.
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	names := decode(8)
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if delta > 4<<20 {
		t.Fatalf("eight short names retained %d bytes", delta)
	}
	runtime.KeepAlive(names)
}

func TestTimeoutCancelsBusinessContext(t *testing.T) {
	app := reviewLatestApp(t)
	app.Use(middleware.Timeout(10 * time.Millisecond))
	cancelled := make(chan error, 1)
	app.GET("/", func(ctx *RequestContext) { business := ctx.Context(); <-business.Done(); cancelled <- business.Err() })
	// Init attaches the fasthttp server state needed by its native timeout wrapper.
	ctx := &fasthttp.RequestCtx{}
	var req fasthttp.Request
	req.SetRequestURI("/")
	ctx.Init(&req, nil, nil)
	app.handleRequest(ctx)
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("business context never cancelled")
	}
}
