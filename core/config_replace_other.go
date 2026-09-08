//go:build !windows

package core

import "os"

func replaceConfigFile(source, target string) error {
	return os.Rename(source, target)
}
