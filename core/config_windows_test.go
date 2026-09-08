//go:build windows

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSaveConfigWindowsLongPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("a", 120), strings.Repeat("b", 120), "config.json")
	config := DefaultConfig()
	if err := SaveConfig(config, path); err != nil {
		t.Fatal(err)
	}
	config.Port = 9090
	if err := SaveConfig(config, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Port != config.Port {
		t.Fatalf("long-path configuration did not round-trip: %+v, %v", loaded, err)
	}
	assertNoConfigTempFiles(t, filepath.Dir(path))
}

func TestSaveConfigWindowsFailedReplacementPreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const original = "original configuration"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// 允许读写但不共享 DELETE，模拟编辑器或其他进程阻止 Windows 文件替换。
	handle, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := SaveConfig(DefaultConfig(), path); err == nil {
		t.Fatal("expected replacement to fail while the original denies deletion")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("failed replacement changed original: %q, %v", data, err)
	}
	assertNoConfigTempFiles(t, filepath.Dir(path))
}

func TestSaveConfigWindowsReadonlyTargetCleansTemporaryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const original = "read-only original"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0600) })
	if err := SaveConfig(DefaultConfig(), path); err == nil {
		t.Fatal("expected replacement of a read-only target to fail")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("failed replacement changed original: %q, %v", data, err)
	}
	assertNoConfigTempFiles(t, filepath.Dir(path))
}
