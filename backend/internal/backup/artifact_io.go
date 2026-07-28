package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

func createArtifactTempFile(dir string, pattern string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建工件目录: %w", err)
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, fmt.Errorf("创建工件临时文件: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, fmt.Errorf("设置工件临时文件权限: %w", err)
	}
	return file, nil
}

// writeArtifactAtomically 只发布尚不存在的目标，避免覆盖已经发布的工件。
func writeArtifactAtomically(destination string, write func(*os.File) error) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("工件目标已存在: %w", os.ErrExist)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查工件目标: %w", err)
	}
	return replaceFileAtomically(destination, write)
}

// replaceFileAtomically 以受限权限写入临时文件后原子替换目标，并 fsync 文件与目录，
// 使进程或主机中断后只能读到替换前或替换后的完整内容。
func replaceFileAtomically(destination string, write func(*os.File) error) error {
	destinationDir := filepath.Dir(destination)
	temporary, err := createArtifactTempFile(destinationDir, ".artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		if !published {
			os.Remove(temporaryPath)
		}
	}()

	if err := write(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步工件临时文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭工件临时文件: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("发布工件: %w", err)
	}
	published = true
	return syncDirectory(destinationDir)
}

// syncDirectory 持久化目录项，使 rename 结果在主机断电后仍然可见。
func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("打开目录: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("同步目录: %w", err)
	}
	return nil
}
