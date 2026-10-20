package syncstrm

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

func TestLocalMetadataCleanupFollowsMissingVideo(t *testing.T) {
	for _, tt := range []struct {
		name           string
		videos         []string
		remoteVideos   []string
		metadata       []string
		remoteMetadata []string
		uploads        []string
	}{
		{
			name: "movie_versions", videos: []string{"Film - 1080p.strm", "Film - 2160p.strm", "Film - Extended.strm"},
			remoteVideos: []string{"Film - Extended.mkv"},
			metadata:     []string{"Film - 1080p.nfo", "Film - 2160p.nfo", "Film - Extended.nfo"},
			uploads:      []string{"Film - Extended.nfo"},
		},
		{
			name: "episode_images_before_and_after_strm", videos: []string{"Show - S01E03.strm"},
			metadata: []string{"Show - S01E03.nfo", "Show - S01E03.jpg", "Show - S01E03.webp", "Show - S01E03-thumb.png", "Show - S01E03-poster.jpeg"},
		},
		{
			name: "generated_artwork_suffixes", videos: []string{"Film.strm"},
			metadata: []string{"Film-square.jpg", "Film-logo.jpg", "Film-background.jpg", "Film-4kbackground.jpg", "Film-backdrop.jpg"},
		},
		{
			name: "episode_versions_and_sibling", videos: []string{"Show - S01E03 - 1080p.strm", "Show - S01E03 - 2160p.strm", "Show - S01E04.strm"},
			remoteVideos: []string{"Show - S01E03 - 2160p.mkv", "Show - S01E04.mkv"},
			metadata:     []string{"Show - S01E03 - 1080p.nfo", "Show - S01E03 - 1080p.jpg", "Show - S01E03 - 2160p.nfo", "Show - S01E04.jpg"},
			uploads:      []string{"Show - S01E03 - 2160p.nfo", "Show - S01E04.jpg"},
		},
		{
			name: "exact_owner_precedes_image_suffix", videos: []string{"Episode.strm", "Episode-thumb.strm"},
			remoteVideos: []string{"Episode-thumb.mkv"},
			metadata:     []string{"Episode.nfo", "Episode-thumb.jpg", "Episode-thumb-poster.jpg"},
			uploads:      []string{"Episode-thumb.jpg", "Episode-thumb-poster.jpg"},
		},
		{
			name: "shared_artwork_and_nfo", videos: []string{"poster.strm", "tvshow.strm", "season01-poster.strm", "Episode.strm"},
			metadata: []string{"poster.jpg", "tvshow.nfo", "season.nfo", "season01.nfo", "season01-poster.jpg", "fanart.jpg", "squareart.jpg"},
			uploads:  []string{"poster.jpg", "tvshow.nfo", "season.nfo", "season01.nfo", "season01-poster.jpg", "fanart.jpg", "squareart.jpg"},
		},
		{
			name: "season_prefix_in_video_title", videos: []string{"Season of the Witch (2011).strm"},
			metadata: []string{"Season of the Witch (2011).nfo", "Season of the Witch (2011).jpg"},
		},
		{
			name: "unknown_owner_and_subtitle_keep_existing_upload", videos: []string{"Episode.strm"},
			metadata: []string{"unrelated.nfo", "Episode-extra.jpg", "Episode.zh.srt"},
			uploads:  []string{"unrelated.nfo", "Episode-extra.jpg", "Episode.zh.srt"},
		},
		{
			name: "same_stem_surviving_container", videos: []string{"Episode.strm"},
			remoteVideos: []string{"Episode.mp4"},
			metadata:     []string{"Episode.nfo", "Episode.jpg"},
			uploads:      []string{"Episode.nfo", "Episode.jpg"},
		},
		{
			name: "remote_metadata_is_outside_cleanup_scope", videos: []string{"Episode.strm"},
			metadata: []string{"Episode.nfo", "Episode.jpg"}, remoteMetadata: []string{"Episode.nfo", "Episode.jpg"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, parent := newLocalMetadataCleanupSync(t)
			for _, name := range append(slices.Clone(tt.videos), tt.metadata...) {
				writeScopeFile(t, filepath.Join(parent.LocalFilePath, name))
			}
			var liveVideos []string
			for _, name := range append(slices.Clone(tt.remoteVideos), tt.remoteMetadata...) {
				localName := name
				isVideo := slices.Contains(tt.remoteVideos, name)
				if isVideo {
					localName = strings.TrimSuffix(name, filepath.Ext(name)) + ".strm"
					liveVideos = append(liveVideos, localName)
				}
				file := &SyncFileCache{SourceType: models.SourceType115, FileId: name, ParentId: parent.FileId,
					FileType: v115open.TypeFile, FileName: name, Path: "Media/Empty", IsVideo: isVideo, IsMeta: !isVideo,
					LocalFilePath: filepath.Join(parent.LocalFilePath, localName)}
				if err := s.memSyncCache.Insert(file); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := s.compareLocalFilesWithTempTable(); err != nil {
					t.Fatal(err)
				}
				for _, name := range tt.videos {
					requireScopeFile(t, filepath.Join(parent.LocalFilePath, name), slices.Contains(liveVideos, name))
				}
				for _, name := range tt.metadata {
					retained := slices.Contains(tt.uploads, name) || slices.Contains(tt.remoteMetadata, name)
					requireScopeFile(t, filepath.Join(parent.LocalFilePath, name), retained)
				}
				var tasks []models.DbUploadTask
				if err := db.Db.Find(&tasks).Error; err != nil {
					t.Fatal(err)
				}
				var uploaded []string
				for _, task := range tasks {
					uploaded = append(uploaded, filepath.Base(task.LocalFullPath))
				}
				slices.Sort(uploaded)
				wantUploads := slices.Clone(tt.uploads)
				slices.Sort(wantUploads)
				if !slices.Equal(uploaded, wantUploads) {
					t.Fatalf("第 %d 次同步上传文件=%v，期望 %v", attempt+1, uploaded, wantUploads)
				}
			}
		})
	}
}

func newLocalMetadataCleanupSync(t *testing.T) (*SyncStrm, models.SyncFile) {
	t.Helper()
	s, parent := new115UploadParentSync(t)
	s.Config.MetaExt = []string{".nfo", ".jpg", ".jpeg", ".png", ".webp", ".tbn", ".svg", ".srt"}
	if err := s.memSyncCache.Insert(ledgerTestCache(parent, false)); err != nil {
		t.Fatal(err)
	}
	s.SyncDriver = &uploadParent115Driver{lookup: func(context.Context, string) (string, error) {
		t.Fatal("已有目录与文件缓存不应增加网盘请求")
		return "", errors.New("unexpected lookup")
	}}
	return s, parent
}

func TestLocalMetadataCleanupPreservesProtectedOwners(t *testing.T) {
	for _, mode := range []string{"excluded_video", "excluded_metadata", "failed_video", "failed_directory", "incremental", "metadata_disabled", "keep", "active_upload", "scope_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, parent := newLocalMetadataCleanupSync(t)
			video := filepath.Join(parent.LocalFilePath, "Episode.strm")
			meta := filepath.Join(parent.LocalFilePath, "Episode.nfo")
			for _, local := range []string{video, meta} {
				writeScopeFile(t, local)
			}
			switch mode {
			case "excluded_video":
				s.Config.ExcludeNames = []string{"episode.strm"}
			case "excluded_metadata":
				s.Config.ExcludeNames = []string{"episode.nfo"}
			case "failed_video":
				s.recordLocalFileFailure(video, errors.New("video unavailable"))
			case "failed_directory":
				s.recordScanFailure("Media/Empty", errors.New("listing failed"))
			case "incremental":
				s.skipMissingCleanup("incremental scan")
			case "metadata_disabled":
				s.Config.EnableDownloadMeta = 0
			case "keep":
				s.Config.NetNotFoundFileAction = models.SyncTreeItemMetaActionKeep
			case "active_upload":
				if err := models.AddUploadTaskFromSyncFile(&models.SyncFile{AccountId: s.Account.ID, SyncPathId: s.SyncPathId,
					SourceType: models.SourceType115, FileName: "Episode.nfo", LocalFilePath: meta, Path: "Media/Empty", ParentId: parent.FileId}); err != nil {
					t.Fatal(err)
				}
			case "scope_failure":
				s.scopeRelease = func() {}
				s.scopeLocalRoot = t.TempDir()
			}
			if err := s.Config.compileExcludeNameRegexes(); err != nil {
				t.Fatal(err)
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, meta, true)
			if s.NewUpload != 0 {
				t.Fatalf("受保护元数据不应新增上传任务：%d", s.NewUpload)
			}
		})
	}
}

func TestLocalMetadataCleanupDoesNotReadLedgerOrAddQueries(t *testing.T) {
	s, parent := newLocalMetadataCleanupSync(t)
	for _, name := range []string{"Episode.strm", "Episode.nfo", "Episode.jpg", "Episode.webp"} {
		writeScopeFile(t, filepath.Join(parent.LocalFilePath, name))
	}
	queries := 0
	if err := db.Db.Callback().Query().Before("gorm:query").Register("metadata_cleanup_queries", func(tx *gorm.DB) {
		if tx.Statement.Table != "db_upload_tasks" {
			t.Errorf("元数据关联不应查询其他表：%s", tx.Statement.Table)
		}
		queries++
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if queries != 3 || s.NewUpload != 0 {
		t.Fatalf("查询数=%d，上传数=%d，期望仅保留 3 次已有的活跃上传检查", queries, s.NewUpload)
	}
}
