package helpers

import (
	"os"

	"golang.org/x/sys/windows"
)

func publishSyncedFileNoReplace(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// Windows 不支持普通目录 Sync；使用写穿透的同卷重命名，不覆盖也不跨卷复制。
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename_noreplace", Old: source, New: target, Err: err}
	}
	return nil
}
