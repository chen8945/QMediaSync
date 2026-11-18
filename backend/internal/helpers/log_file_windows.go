package helpers

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openAppendLogFile(name string) (*os.File, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, `\\?\`) {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	// 同步历史可先于后台结束被删除，日志句柄须允许删除且保留追加语义。
	handle, err := windows.CreateFile(pathUTF16, windows.FILE_APPEND_DATA,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(handle), name), nil
}
