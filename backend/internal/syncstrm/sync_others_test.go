package syncstrm

import (
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestStartFileHonorsGlobalExclusions(t *testing.T) {
	originalDB, originalSettings := db.Db, models.SettingsGlobal
	originalLogger, original115Logger, originalConfigDir := helpers.AppLogger, helpers.V115Log, helpers.ConfigDir
	t.Cleanup(func() {
		db.Db, models.SettingsGlobal = originalDB, originalSettings
		helpers.AppLogger, helpers.V115Log, helpers.ConfigDir = originalLogger, original115Logger, originalConfigDir
	})
	account, _ := setupStrmGenerationServiceTestDB(t)
	helpers.ConfigDir = t.TempDir()
	tests := []struct {
		name     string
		exact    []string
		patterns []string
		filename string
		parent   string
	}{
		{name: "原文件名规则", exact: []string{"sample.mkv"}, filename: "SAMPLE.mkv", parent: "/Media"},
		{name: "原父目录规则", exact: []string{"extras"}, filename: "movie.mkv", parent: "/Media/Extras/Season 1"},
		{name: "仅正则文件名规则", patterns: []string{"(?i)sample"}, filename: "Movie.SAMPLE.mkv", parent: "/Media"},
		{name: "正则父目录规则", patterns: []string{"^Extras$"}, filename: "movie.mkv", parent: "/Media/Extras/Season 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models.SettingsGlobal.ExcludeNameArr = tt.exact
			models.SettingsGlobal.ExcludeNameRegexArr = tt.patterns
			file := &SyncFileCache{
				FileId:     "file-excluded",
				ParentId:   "parent-id",
				FileType:   v115open.TypeFile,
				FileName:   tt.filename,
				Path:       tt.parent,
				FileSize:   1024,
				PickCode:   "pick-excluded",
				SourceType: models.SourceType115,
				IsVideo:    true,
			}
			target := t.TempDir()
			syncer := NewSyncStrmByPath(account, file.GetFullRemotePath(), file.FileId, target, true)
			if syncer == nil {
				t.Fatal("创建手动同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.SyncDriver = &fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{file.FileId: file}}
			if err := syncer.StartFile(); err != nil {
				t.Fatalf("排除的文件应直接跳过，不应进入 STRM 生成：%v", err)
			}
			if syncer.NewStrm != 0 {
				t.Fatalf("排除的文件生成了 %d 个 STRM", syncer.NewStrm)
			}
			var files int
			if err := filepath.WalkDir(target, func(_ string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					files++
				}
				return err
			}); err != nil {
				t.Fatalf("检查输出目录失败：%v", err)
			}
			if files != 0 {
				t.Fatalf("排除的文件不应写入输出目录，实际有 %d 个文件", files)
			}
		})
	}
}
