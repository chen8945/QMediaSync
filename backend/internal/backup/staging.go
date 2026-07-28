package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"qmediasync/internal/helpers"
)

// ErrUploadStagingUnavailable 表示上传暂存目录不可用，无法受理上传恢复。
var ErrUploadStagingUnavailable = errors.New("上传暂存目录不可用")

// EnsureUploadStagingDir 创建并返回受限的上传暂存目录。
func EnsureUploadStagingDir() (string, error) {
	directory := UploadStagingDir()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("%w：%w", ErrUploadStagingUnavailable, err)
	}
	return directory, nil
}

// StageUploadArtifact 将一个上传工件完整写入专用暂存目录。
// 每个工件只受 D1 的单工件上限约束；不维护应用级数量或累计容量配额。
func StageUploadArtifact(source io.Reader) (string, int64, error) {
	directory, err := EnsureUploadStagingDir()
	if err != nil {
		return "", 0, err
	}
	return stageUploadArtifact(directory, source, artifactMaxUploadSize)
}

func stageUploadArtifact(directory string, source io.Reader, maxSize int64) (string, int64, error) {
	if source == nil || maxSize < 0 {
		return "", 0, fmt.Errorf("%w：上传工件参数无效", ErrInvalidArtifact)
	}
	temporary, err := createArtifactTempFile(directory, ".upload-*.zip")
	if err != nil {
		return "", 0, err
	}
	temporaryPath := temporary.Name()
	success := false
	defer func() {
		if !success {
			temporary.Close()
			os.Remove(temporaryPath)
		}
	}()

	written, err := io.Copy(temporary, io.LimitReader(source, maxSize+1))
	if err != nil {
		return "", 0, fmt.Errorf("写入上传工件: %w", err)
	}
	if written > maxSize {
		return "", 0, fmt.Errorf("%w：上传工件超出限制", ErrInvalidArtifact)
	}
	if err := temporary.Sync(); err != nil {
		return "", 0, fmt.Errorf("同步上传工件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", 0, fmt.Errorf("关闭上传工件: %w", err)
	}
	success = true
	return temporaryPath, written, nil
}

// ClearUploadStaging 清空上传暂存目录中的全部候选工件。
// 只有定时备份取得执行权后和进程启动时会调用它；手动备份必须保留这些文件，
// 且任何情况下都不得清理 config/tmp 的其他子目录。
func ClearUploadStaging() {
	// 预检记录必须无条件失效：即使暂存目录不存在，也不能让旧的一次性凭据继续可用。
	defer ClearPreflightRecords()

	directory := UploadStagingDir()
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		helpers.AppLogger.Warnf("读取上传暂存目录失败：%v", err)
		return
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			helpers.AppLogger.Warnf("清理上传暂存文件失败：%v", err)
		}
	}
}
