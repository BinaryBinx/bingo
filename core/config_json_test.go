package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigFileAcceptsModernAndLegacyMultiCoreNames(t *testing.T) {
	for _, input := range []string{
		`{"multi_core":{"enabled":false,"num_cpu":2,"workers_per_core":8,"enable_cpu_affinity":true,"max_conns":2,"read_buffer_size":8192,"write_buffer_size":16384}}`,
		`{"multi_core":{"Enabled":false,"NumCPU":2,"WorkersPerCore":8,"EnableCPUAffinity":true,"MaxConns":2,"ReadBufferSize":8192,"WriteBufferSize":16384}}`,
		`{"MULTI_CORE":{"ENABLED":false,"numCPU":2,"workersPerCore":8,"enableCPUAffinity":true,"maxConns":2,"readBufferSize":8192,"writeBufferSize":16384}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		for _, load := range []func(string) (*Config, error){LoadConfig, LoadConfigStrict} {
			got, err := load(path)
			if err != nil {
				t.Fatal(err)
			}
			want := DefaultConfig()
			want.MultiCore = MultiCoreConfig{Enabled: false, NumCPU: 2, WorkersPerCore: 8, EnableCPUAffinity: true, MaxConns: 2, ReadBufferSize: 8192, WriteBufferSize: 16384}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("file %s loaded %+v, want %+v", input, got.MultiCore, want.MultiCore)
			}
		}
	}
}

func TestConfigFileCanonicalNamesTakePrecedence(t *testing.T) {
	for _, input := range []string{
		`{"NumCPU":4,"num_cpu":0,"EnableCPUAffinity":true,"enable_cpu_affinity":false,"MaxConns":8,"max_conns":2}`,
		`{"max_conns":2,"MaxConns":8,"enable_cpu_affinity":false,"EnableCPUAffinity":true,"num_cpu":0,"NumCPU":4}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"multi_core":`+input+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfigStrict(path)
		if err != nil {
			t.Fatal(err)
		}
		want := DefaultConfig()
		want.MultiCore.MaxConns = 2
		if !reflect.DeepEqual(got, want) {
			t.Errorf("canonical zero/false values or omitted defaults lost: %+v", got.MultiCore)
		}
	}
}

func TestLoadConfigStrictRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	for _, tc := range []struct{ input, field string }{
		{`{"read_timout":1000000000}`, "read_timout"},
		{`{"multi_core":{"max_conn":2}}`, "max_conn"},
		{`{"multi_core":{"NumCP":2}}`, "NumCP"},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfigStrict(path)
		if got != nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("strict load failed to identify %s: %+v %v", tc.field, got, err)
		}
		got, err = LoadConfig(path)
		if err != nil || !reflect.DeepEqual(got, DefaultConfig()) {
			t.Errorf("compatibility load changed: %+v %v", got, err)
		}
	}
}

func TestConfigFileRejectsMalformedAndInvalidValues(t *testing.T) {
	for _, input := range []string{
		``, `null`, `[]`, `true`, `{} {}`, `{} trailing`,
		`{"multi_core":[]}`, `{"multi_core":"invalid"}`,
		`{"multi_core":{"max_conns":0}}`,
		`{"multi_core":{"max_conns":0,"MaxConns":8}}`,
		`{"multi_core":{"NumCPU":-1}}`,
		`{"multi_core":{"read_buffer_size":"8192"}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		for _, load := range []func(string) (*Config, error){LoadConfig, LoadConfigStrict} {
			if got, err := load(path); got != nil || !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("accepted invalid configuration %q: %+v %v", input, got, err)
			}
		}
	}
}

func TestSaveConfigWritesCanonicalMultiCoreNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := DefaultConfig()
	config.MultiCore.MaxConns = 2
	if err := SaveConfig(config, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		MultiCore map[string]json.RawMessage `json:"multi_core"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"enabled", "num_cpu", "workers_per_core", "enable_cpu_affinity", "max_conns", "read_buffer_size", "write_buffer_size"} {
		if _, ok := fields.MultiCore[field]; !ok {
			t.Errorf("saved file omitted %s", field)
		}
	}
	if len(fields.MultiCore) != 7 {
		t.Errorf("unexpected legacy fields: %s", data)
	}
	got, err := LoadConfigStrict(path)
	if err != nil || !reflect.DeepEqual(got, config) {
		t.Fatalf("strict save/load did not round-trip: %+v %v", got, err)
	}
}
