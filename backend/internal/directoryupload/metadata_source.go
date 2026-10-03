package directoryupload

import (
	"errors"
	"fmt"
	"strings"

	"qmediasync/internal/models"
)

// ResolveMetadataSource 返回监控根内的元数据真实路径，不改写用于清理的原始上传路径。
func ResolveMetadataSource(task *models.DbUploadTask) (string, error) {
	if task == nil || task.Source != models.UploadSourceDirectoryMonitor {
		return "", errors.New("元数据源文件必须来自目录监控上传")
	}
	rule, err := findCleanupRule(task)
	if err != nil {
		return "", fmt.Errorf("读取元数据源文件的目录监控规则失败：%w", err)
	}
	if rule == nil {
		return "", errors.New("元数据源文件缺少目录监控规则")
	}
	if rule.SyncPathId != task.SyncPathId || rule.AccountId != task.AccountId {
		return "", errors.New("元数据源文件的目录监控规则与上传任务不匹配")
	}
	if strings.TrimSpace(rule.MonitorPath) == "" {
		return "", errors.New("元数据源文件的监控目录不能为空")
	}
	resolved, err := ensurePathResolvesWithinMonitor(rule.MonitorPath, task.LocalFullPath)
	if err != nil {
		return "", fmt.Errorf("校验元数据源文件真实路径失败：%w", err)
	}
	return resolved, nil
}
