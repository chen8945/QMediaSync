//go:build !windows

package logstream

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(_ *os.File, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("无法取得日志文件身份")
	}
	return fmt.Sprintf("%x-%x", stat.Dev, stat.Ino), nil
}
