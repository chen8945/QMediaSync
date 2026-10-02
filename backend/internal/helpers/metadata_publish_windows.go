package helpers

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func metadataLinkUnsupported(_ string, err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION)
}

func renameMetadataNoReplace(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// 不允许替换已有目标，也不允许跨卷复制，避免暴露半成品。
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return &os.LinkError{Op: "rename_noreplace", Old: source, New: target, Err: err}
	}
	return nil
}
