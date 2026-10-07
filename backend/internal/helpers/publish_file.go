package helpers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SyncAndPublishFileNoReplace 同步完整临时文件后原子发布，已有目标始终保留。
// 调用方负责移除临时路径；发布后的目录同步失败可能留下完整目标文件。
func SyncAndPublishFileNoReplace(source, target string) error {
	if filepath.Dir(source) != filepath.Dir(target) {
		return fmt.Errorf("发布临时文件必须与目标位于同一目录")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("发布源必须是普通文件")
	}
	file, err := os.OpenFile(source, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return publishSyncedFileNoReplace(source, target)
}
