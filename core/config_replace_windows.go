//go:build windows

package core

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func replaceConfigFile(source, target string) error {
	from, err := configWindowsPath(source)
	if err != nil {
		return err
	}
	to, err := configWindowsPath(target)
	if err != nil {
		return err
	}
	// 同目录移动，不允许跨卷复制，也不先删除目标：失败时旧配置仍在。
	// WRITE_THROUGH 要求 Windows 等待移动操作完成后再返回。
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func configWindowsPath(path string) (*uint16, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// 保持与 Go 文件 API 相同的长路径能力；扩展 UNC 路径需要单独的前缀。
	if !strings.HasPrefix(path, `\\?\`) {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	return windows.UTF16PtrFromString(path)
}
