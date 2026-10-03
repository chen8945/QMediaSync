package syncstrm

import (
	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

// hasActiveMetadataReplacement 在摘要计算前跳过已安排的替换，临时同步按本地目标隔离。
func (s *SyncStrm) hasActiveMetadataReplacement(path string) (bool, error) {
	source := models.DownloadSourceStrm
	if s.Account.SourceType == models.SourceTypeLocal {
		source = models.DownloadSourceLocalFile
	}
	var count int64
	err := db.Db.WithContext(s.Context).Model(&models.DbDownloadTask{}).
		Where("source = ? AND source_type = ? AND account_id = ? AND sync_path_id = ? AND local_full_path = ?", source, s.Account.SourceType, s.Account.ID, s.SyncPathId, path).
		Where("status IN ? AND replace_baseline <> ?", []models.DownloadStatus{models.DownloadStatusPending, models.DownloadStatusDownloading}, "").
		Limit(1).Count(&count).Error
	return count > 0, err
}
