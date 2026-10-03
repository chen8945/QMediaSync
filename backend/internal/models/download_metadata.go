package models

import (
	"fmt"
	"strings"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func (task *DbDownloadTask) savePublishedMetadata(hash string) error {
	if err := db.Db.Model(task).Update("published_sha256", hash).Error; err != nil {
		return fmt.Errorf("保存元数据恢复信息失败：%w", err)
	}
	task.PublishedSHA256 = hash
	return nil
}

func (task *DbDownloadTask) downloadMetadata(url, ua string) error {
	contentMD5 := task.RemoteMd5
	if task.SourceType == SourceTypeBaiduPan {
		// 百度列表的 md5 是云端哈希，保留兼容字段但不用于内容校验；旧队列同样适用。
		contentMD5 = ""
	}
	return helpers.DownloadMetadataFile(url, task.LocalFullPath, ua, task.ReplaceBaseline, task.Size, task.RemoteSha1, contentMD5, task.MTime, task.savePublishedMetadata)
}

// 发布成功但任务状态未保存时，只认已经校验过的内容和时间。
func (task *DbDownloadTask) finishPublishedMetadata() bool {
	if task.PublishedSHA256 == "" {
		return false
	}
	fingerprint, err := helpers.MetadataFingerprint(task.LocalFullPath)
	if err != nil {
		return false
	}
	parts := strings.Split(fingerprint, ":")
	if len(parts) != 3 || parts[2] != task.PublishedSHA256 {
		return false
	}
	if task.Size >= 0 && parts[0] != fmt.Sprint(task.Size) {
		return false
	}
	if task.MTime > 0 && parts[1] != fmt.Sprint(task.MTime*1000000000) {
		return false
	}
	task.Complete()
	return true
}
