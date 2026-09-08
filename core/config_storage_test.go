package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BINGO_HOST", "BINGO_PORT", "BINGO_RUN_MODE", "BINGO_LOG_LEVEL",
		"BINGO_READ_TIMEOUT", "BINGO_WRITE_TIMEOUT", "BINGO_IDLE_TIMEOUT",
		"BINGO_MAX_BODY_SIZE", "BINGO_SERVER_NAME",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadConfigFromEnvCommitsValidValues(t *testing.T) {
	clearConfigEnv(t)
	for name, value := range map[string]string{
		"BINGO_HOST": "127.0.0.1", "BINGO_PORT": "9001",
		"BINGO_RUN_MODE": "release", "BINGO_LOG_LEVEL": "error",
		"BINGO_READ_TIMEOUT": "0", "BINGO_WRITE_TIMEOUT": "12", "BINGO_IDLE_TIMEOUT": "40",
		"BINGO_MAX_BODY_SIZE": "8192", "BINGO_SERVER_NAME": "configured",
	} {
		t.Setenv(name, value)
	}
	config := DefaultConfig()
	want := *config
	want.Host, want.Port = "127.0.0.1", 9001
	want.RunMode, want.LogLevel = RunModeRelease, "error"
	want.ReadTimeout, want.WriteTimeout, want.IdleTimeout = 0, 12*time.Second, 40*time.Second
	want.MaxRequestBodySize, want.ServerName = 8192, "configured"
	if err := LoadConfigFromEnv(config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*config, want) {
		t.Fatalf("unexpected configuration: got %+v want %+v", *config, want)
	}
}

func TestLoadConfigFromEnvRejectsInvalidValuesWithoutMutation(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"BINGO_PORT", "not-a-port"}, {"BINGO_PORT", "-1"}, {"BINGO_PORT", "65536"},
		{"BINGO_RUN_MODE", "production"}, {"BINGO_LOG_LEVEL", "verbose"},
		{"BINGO_READ_TIMEOUT", "1.5"}, {"BINGO_READ_TIMEOUT", "-1"},
		{"BINGO_WRITE_TIMEOUT", "9223372037"}, {"BINGO_IDLE_TIMEOUT", "9223372036854775808"},
		{"BINGO_MAX_BODY_SIZE", "0"}, {"BINGO_MAX_BODY_SIZE", "-8"},
		{"BINGO_MAX_BODY_SIZE", "9223372036854775808"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("BINGO_HOST", "would-change-on-partial-update")
			t.Setenv("BINGO_PORT", "9002")
			t.Setenv(tc.name, tc.value)
			config := DefaultConfig()
			before := *config
			err := LoadConfigFromEnv(config)
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("expected named invalid-configuration error, got %v", err)
			}
			if !reflect.DeepEqual(*config, before) {
				t.Fatalf("configuration was partially updated on failure: %+v", *config)
			}
		})
	}
}

func TestSaveConfigReplacesCompleteFileAndPreservesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	config := DefaultConfig()
	config.ServerName = strings.Repeat("old-value", 1024)
	if err := SaveConfig(config, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	config.ServerName = "new"
	config.Port = 9003
	if err := SaveConfig(config, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, config) {
		t.Fatalf("saved configuration did not round-trip: got %+v want %+v", loaded, config)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("permissions changed from %v to %v", before.Mode().Perm(), after.Mode().Perm())
	}
	assertNoConfigTempFiles(t, filepath.Dir(path))
}

func TestConfigInvalidInputPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("original contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(nil, path); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected invalid-configuration error, got %v", err)
	}
	invalid := DefaultConfig()
	invalid.Port = 70000
	if err := SaveConfig(invalid, path); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid config should not replace existing file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original contents" {
		t.Fatalf("failed save changed original: %q, %v", data, err)
	}
	if err := LoadConfigFromEnv(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil environment target should fail, got %v", err)
	}
	for _, input := range []string{"null", "[]", "{bad json"} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("input %q should be invalid, got %v", input, err)
		}
	}
	assertNoConfigTempFiles(t, filepath.Dir(path))
}

func TestLoadConfigPreservesDefaultsForOmittedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, []byte(`{"port":9004}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultConfig()
	want.Port = 9004
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing fields lost defaults: %+v", got)
	}
}

func assertNoConfigTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary configuration files remain: %v (error %v)", matches, err)
	}
}
