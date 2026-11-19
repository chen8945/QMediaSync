package logstream

import (
	"errors"
	"os"
)

var (
	ErrCursorInvalid = errors.New("日志位置已失效")
	ErrNotRegular    = errors.New("日志目标不是普通文件")
)

// OpenFile 在同一文件句柄上取得并校验身份，调用方负责关闭。
func OpenFile(path, expectedFileID string) (*os.File, os.FileInfo, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, "", err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrNotRegular
	}
	var fileID string
	if err == nil {
		fileID, err = fileIdentity(file, info)
	}
	if err == nil && expectedFileID != "" && expectedFileID != fileID {
		err = ErrCursorInvalid
	}
	if err != nil {
		file.Close()
		return nil, nil, "", err
	}
	return file, info, fileID, nil
}
