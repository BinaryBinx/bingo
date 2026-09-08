package core

import (
	"errors"
	"os"
	"testing"
)

func TestAppErrorPreservesErrorChain(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "missing", Err: os.ErrNotExist}
	err := NewAppError(cause, "load failed", 500)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("errors.Is could not reach the original cause")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr != cause {
		t.Fatal("errors.As could not recover the original typed error")
	}
	if got := NewAppError(nil, "message", 500).Unwrap(); got != nil {
		t.Fatalf("nil cause should unwrap to nil, got %v", got)
	}
}
