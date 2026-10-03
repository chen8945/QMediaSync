package backup

import "qmediasync/internal/models"

// 下载任务备份包含恢复执行所需的信息；这些字段不出现在队列 API 中。
type downloadTaskBackup struct {
	models.DbDownloadTask
	ReplaceBaseline   string `json:"replace_baseline,omitempty"`
	PublishedSHA256   string `json:"published_sha256,omitempty"`
	RemoteDownloadURL string `json:"remote_download_url,omitempty"`
	LocalSourcePath   string `json:"local_source_path,omitempty"`
	EmbyItemID        string `json:"emby_item_id,omitempty"`
	DedupScopeHash    string `json:"dedup_scope_hash,omitempty"`
	DedupLocatorHash  string `json:"dedup_locator_hash,omitempty"`
}

func downloadBackup(task models.DbDownloadTask) downloadTaskBackup {
	return downloadTaskBackup{task, task.ReplaceBaseline, task.PublishedSHA256, task.RemoteDownloadUrl, task.LocalSourcePath, task.EmbyItemId, task.DedupScopeHash, task.DedupLocatorHash}
}

func (row downloadTaskBackup) restore() models.DbDownloadTask {
	task := row.DbDownloadTask
	task.ReplaceBaseline = row.ReplaceBaseline
	task.PublishedSHA256 = row.PublishedSHA256
	task.RemoteDownloadUrl = row.RemoteDownloadURL
	task.LocalSourcePath = row.LocalSourcePath
	task.EmbyItemId = row.EmbyItemID
	task.DedupScopeHash = row.DedupScopeHash
	task.DedupLocatorHash = row.DedupLocatorHash
	return task
}
