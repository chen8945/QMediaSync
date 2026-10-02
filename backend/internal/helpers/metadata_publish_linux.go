package helpers

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func metadataLinkUnsupported(source string, err error) bool {
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
		return true
	}
	// FAT/exFAT 的无硬链接能力以 EPERM 返回；其他文件系统的权限错误不能当作能力缺失。
	if errors.Is(err, unix.EPERM) {
		var stat unix.Statfs_t
		if unix.Statfs(source, &stat) == nil {
			return stat.Type == unix.EXFAT_SUPER_MAGIC || stat.Type == unix.MSDOS_SUPER_MAGIC
		}
	}
	return false
}

func renameMetadataNoReplace(source, target string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE); err != nil {
		return &os.LinkError{Op: "rename_noreplace", Old: source, New: target, Err: err}
	}
	return nil
}
