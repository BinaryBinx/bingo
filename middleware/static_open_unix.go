//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package middleware

import (
	"os"
	"syscall"
)

// O_NONBLOCK prevents a path replaced with a FIFO after Stat from blocking Open.
// Regular files ignore this flag; the caller checks the opened file's mode.
func openStaticFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
