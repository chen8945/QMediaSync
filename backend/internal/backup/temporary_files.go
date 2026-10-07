package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// CleanupTemporaryFiles 在持有实例锁、后台工作启动前，只清理备份功能明确命名的临时项。
// 历史数字目录无法确认归属，普通 ZIP 和其他配置文件始终保留。
func CleanupTemporaryFiles(configDir string) error {
	config, err := os.OpenRoot(configDir)
	if err != nil {
		return err
	}
	defer config.Close()
	backups, err := openTemporaryDirectory(config, "backups")
	if err != nil || backups == nil {
		return err
	}
	defer backups.Close()
	entries, err := fs.ReadDir(backups.FS(), ".")
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		name := entry.Name()
		workDir := strings.HasPrefix(name, "backup-export-") || strings.HasPrefix(name, "backup-restore-")
		part := strings.HasPrefix(name, ".backup-publish-") && strings.HasSuffix(name, ".part")
		if !workDir && !part {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("拒绝清理符号链接临时项：%s", name))
			continue
		}
		if workDir && entry.IsDir() {
			cleanupErr = errors.Join(cleanupErr, backups.RemoveAll(name))
		} else if part && entry.Type().IsRegular() {
			cleanupErr = errors.Join(cleanupErr, backups.Remove(name))
		}
	}
	temp, err := openTemporaryDirectory(backups, "temp")
	if err != nil || temp == nil {
		return errors.Join(cleanupErr, err)
	}
	defer temp.Close()
	entries, err = fs.ReadDir(temp.FS(), ".")
	if err != nil {
		return errors.Join(cleanupErr, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "upload_") || !strings.HasSuffix(name, ".zip") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("拒绝清理符号链接上传项：%s", name))
		} else if entry.Type().IsRegular() {
			cleanupErr = errors.Join(cleanupErr, temp.Remove(name))
		}
	}
	return cleanupErr
}

func openTemporaryDirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("备份临时目录不是普通目录：%s", name)
	}
	return parent.OpenRoot(name)
}
