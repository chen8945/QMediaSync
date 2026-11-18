// Package testfs 提供跨平台文件系统测试夹具。
package testfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// LocalPath 为纯数据夹具的 POSIX 本地绝对路径补齐 Windows 盘符和分隔符。
func LocalPath(path string) string {
	if runtime.GOOS == "windows" {
		return filepath.FromSlash(filepath.VolumeName(os.TempDir()) + path)
	}
	return path
}

// Symlink 创建文件符号链接；Windows 缺少该权限时明确跳过用例。
func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		// ERROR_PRIVILEGE_NOT_HELD 在非 Windows 的 syscall 包中没有命名常量。
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
			t.Skipf("创建文件符号链接需要 Windows 开发者模式或符号链接权限：%v", err)
		}
		t.Fatal(err)
	}
}
