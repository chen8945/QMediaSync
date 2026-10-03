package syncstrm

import (
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestActiveMetadataReplacementScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*models.DbDownloadTask)
		want   bool
	}{
		{"pending", func(*models.DbDownloadTask) {}, true},
		{"downloading", func(task *models.DbDownloadTask) { task.Status = models.DownloadStatusDownloading }, true},
		{"completed", func(task *models.DbDownloadTask) { task.Status = models.DownloadStatusCompleted }, false},
		{"failed", func(task *models.DbDownloadTask) { task.Status = models.DownloadStatusFailed }, false},
		{"account", func(task *models.DbDownloadTask) { task.AccountId++ }, false},
		{"sync_path", func(task *models.DbDownloadTask) { task.SyncPathId++ }, false},
		{"target", func(task *models.DbDownloadTask) { task.LocalFullPath += ".other" }, false},
		{"missing_only", func(task *models.DbDownloadTask) { task.ReplaceBaseline = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScanResultTestSync(t)
			if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
				t.Fatal(err)
			}
			task := models.DbDownloadTask{Source: models.DownloadSourceLocalFile, SourceType: s.Account.SourceType, AccountId: s.Account.ID, SyncPathId: s.SyncPathId, LocalFullPath: "/target/movie.nfo", ReplaceBaseline: "baseline", Status: models.DownloadStatusPending}
			tc.change(&task)
			if err := db.Db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			got, err := s.hasActiveMetadataReplacement("/target/movie.nfo")
			if err != nil || got != tc.want {
				t.Fatalf("active=%v err=%v want=%v", got, err, tc.want)
			}
		})
	}
}
