package scrape

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"

	"gorm.io/gorm"
)

// 用真实 SQLite 和临时目录验证刮削写文件会等待完整同步，且等待不占用数据库连接。
func setupScrapeSTRMScope(t *testing.T, episode bool) (*models.SyncPath, *models.ScrapeMediaFile, func(context.Context, *models.ScrapeMediaFile, []uploadFile) error) {
	t.Helper()
	setupTemporarySyncRecordTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	previousSettings := models.SettingsGlobal
	models.SettingsGlobal = &models.Settings{}
	t.Cleanup(func() { models.SettingsGlobal = previousSettings })
	if err := db.Db.AutoMigrate(&models.SyncFile{}, &models.SyncPath{}, &models.ScrapeStrmPath{}, &models.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	sp := &models.SyncPath{SourceType: models.SourceTypeLocal, LocalPath: t.TempDir(), RemotePath: "/media"}
	if err := db.Db.Create(sp).Error; err != nil {
		t.Fatal(err)
	}
	scrapePath := &models.ScrapePath{BaseModel: models.BaseModel{ID: 1}}
	if err := db.Db.Create(&models.ScrapeStrmPath{ScrapePathID: scrapePath.ID, StrmPathID: sp.ID}).Error; err != nil {
		t.Fatal(err)
	}
	mediaFile := &models.ScrapeMediaFile{
		NewVideoBaseName: "video", NewPathName: "title", DestPath: "/media", SeasonNumber: 1,
		Media:        &models.Media{Path: "/media/title", VideoFileName: "video.mkv", VideoFileId: "/source/video.mkv"},
		MediaEpisode: &models.MediaEpisode{VideoFileName: "video.mkv", VideoFileId: "/source/video.mkv"},
	}
	run := func(ctx context.Context, mediaFile *models.ScrapeMediaFile, files []uploadFile) error {
		base := ScrapeBase{scrapePath: scrapePath, ctx: ctx}
		if episode {
			return (&tvShowScrapeImpl{ScrapeBase: base}).SyncFilesToSTRMPath(mediaFile, files)
		}
		return (&movieScrapeImpl{ScrapeBase: base}).SyncFilesToSTRMPath(mediaFile, files)
	}
	return sp, mediaFile, run
}

func scrapeSTRMVideoPath(sp *models.SyncPath, mediaFile *models.ScrapeMediaFile, episode bool) string {
	path := mediaFile.Media.Path
	if episode {
		path = mediaFile.GetDestFullSeasonPath()
	}
	return filepath.Join(sp.LocalPath, path, mediaFile.NewVideoBaseName+".strm")
}

func TestScrapeSTRMScopeWaitAndCancel(t *testing.T) {
	for _, episode := range []bool{false, true} {
		name := "电影"
		if episode {
			name = "剧集"
		}
		t.Run(name, func(t *testing.T) {
			for _, blocker := range []string{"同步目录", "目录ID", "视频", "元数据", "符号链接视频", "子目录符号链接视频"} {
				t.Run(blocker, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						sp, mediaFile, run := setupScrapeSTRMScope(t, episode)
						realRoot := sp.LocalPath
						if blocker == "符号链接视频" {
							link := filepath.Join(t.TempDir(), "link")
							if err := os.Symlink(realRoot, link); err != nil {
								t.Fatal(err)
							}
							if err := db.Db.Model(sp).Update("local_path", link).Error; err != nil {
								t.Fatal(err)
							}
						}
						videoPath := scrapeSTRMVideoPath(sp, mediaFile, episode)
						if blocker == "子目录符号链接视频" {
							realRoot = t.TempDir()
							videoDir := filepath.Dir(videoPath)
							if err := os.MkdirAll(filepath.Dir(videoDir), 0755); err != nil {
								t.Fatal(err)
							}
							if err := os.Symlink(realRoot, videoDir); err != nil {
								t.Fatal(err)
							}
						}
						meta := uploadFile{SourcePath: filepath.Join(t.TempDir(), "source.nfo"), DestPath: "/metadata", FileName: "video.nfo"}
						if err := os.WriteFile(meta.SourcePath, []byte("metadata"), 0600); err != nil {
							t.Fatal(err)
						}
						metaPath := filepath.Join(sp.LocalPath, meta.DestPath, meta.FileName)
						scope := sp.Scope()
						if blocker == "目录ID" {
							scope = syncscope.Scope{SyncPathID: sp.ID}
						} else if blocker == "视频" {
							scope = syncscope.Scope{LocalPath: videoPath}
						} else if blocker == "元数据" {
							scope = syncscope.Scope{LocalPath: metaPath}
						} else if blocker == "符号链接视频" {
							relative, err := filepath.Rel(sp.LocalPath, videoPath)
							if err != nil {
								t.Fatal(err)
							}
							scope = syncscope.Scope{LocalPath: filepath.Join(realRoot, relative)}
						} else if blocker == "子目录符号链接视频" {
							scope = syncscope.Scope{LocalPath: filepath.Join(realRoot, filepath.Base(videoPath))}
						}
						release, err := syncscope.Acquire(t.Context(), scope)
						if err != nil {
							t.Fatal(err)
						}
						defer release()
						ctx, cancel := context.WithCancel(t.Context())
						defer cancel()
						done := make(chan error, 1)
						go func() { done <- run(ctx, mediaFile, []uploadFile{meta}) }()
						synctest.Wait()
						select {
						case err := <-done:
							t.Fatalf("其他任务未结束时提前返回：%v", err)
						default:
						}
						if _, err := os.Stat(videoPath); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("等待时不应写入 STRM：%v", err)
						}
						var count int64
						if err := db.Db.Model(&models.Sync{}).Count(&count).Error; err != nil || count != 0 {
							t.Fatalf("等待时不应创建临时任务：count=%d err=%v", count, err)
						}
						cancel()
						if err := <-done; !errors.Is(err, context.Canceled) {
							t.Fatalf("等待取消应返回 context.Canceled：%v", err)
						}
						release()
						if err := run(t.Context(), mediaFile, []uploadFile{meta}); err != nil {
							t.Fatal(err)
						}
						for path, want := range map[string]string{videoPath: "/source/video.mkv", metaPath: "metadata"} {
							got, err := os.ReadFile(path)
							if err != nil || string(got) != want {
								t.Fatalf("写入文件 %s = %q，期望 %q，错误：%v", path, got, want, err)
							}
						}
					})
				})
			}
		})
	}
}

func TestScrapeSTRMScopeReloadsChangedDirectory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp, mediaFile, run := setupScrapeSTRMScope(t, false)
		oldPath := scrapeSTRMVideoPath(sp, mediaFile, false)
		release, err := syncscope.Acquire(t.Context(), sp.Scope())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		done := make(chan error, 1)
		go func() { done <- run(t.Context(), mediaFile, nil) }()
		synctest.Wait()
		newRoot := t.TempDir()
		newRelease, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: newRoot})
		if err != nil {
			t.Fatal(err)
		}
		defer newRelease()
		if err := db.Db.Model(sp).Update("local_path", newRoot).Error; err != nil {
			t.Fatal(err)
		}
		release()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("新目录仍被占用，不应继续写入：%v", err)
		default:
		}
		if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("不应使用旧目录：%v", err)
		}
		newRelease()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(scrapeSTRMVideoPath(sp, mediaFile, false)); err != nil {
			t.Fatalf("应写入新目录：%v", err)
		}
	})
}

func TestScrapeSTRMScopeIndependentFileAndFailureRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp, mediaFile, run := setupScrapeSTRMScope(t, false)
		release, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: filepath.Join(sp.LocalPath, "/media/title/other.strm")})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := run(ctx, mediaFile, nil); err != nil {
			t.Fatalf("同目录中的独立文件不应等待：%v", err)
		}
		release()
		meta := uploadFile{SourcePath: filepath.Join(t.TempDir(), "missing.nfo"), DestPath: "/media/title", FileName: "video.nfo"}
		if err := run(t.Context(), mediaFile, []uploadFile{meta}); err == nil {
			t.Fatal("元数据复制失败必须返回错误")
		}
		after, err := syncscope.Acquire(ctx, sp.Scope())
		if err != nil {
			t.Fatalf("写入失败后未释放范围：%v", err)
		}
		after()
	})
}

func TestScrapeSTRMPathLookupErrors(t *testing.T) {
	sp, _, _ := setupScrapeSTRMScope(t, false)
	scrapePath := &models.ScrapePath{BaseModel: models.BaseModel{ID: 1}}
	if err := db.Db.Delete(sp).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := scrapePath.GetSyncPathByPathContext(t.Context(), "/media/title"); err != nil || got != nil {
		t.Fatalf("关联目录已删除时应跳过：%v，%v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := scrapePath.GetSyncPathByPathContext(ctx, "/media/title"); !errors.Is(err, context.Canceled) {
		t.Fatalf("查询取消必须返回原因：%v", err)
	}
	if err := db.Db.Migrator().DropTable(&models.ScrapeStrmPath{}); err != nil {
		t.Fatal(err)
	}
	if _, err := scrapePath.GetSyncPathByPathContext(t.Context(), "/media/title"); err == nil {
		t.Fatal("查询失败不能当作未关联目录")
	}
}

func TestScrapeSTRMSharedConfigAllowsIndependentOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp, mediaFile, run := setupScrapeSTRMScope(t, false)
		release, err := syncscope.Acquire(t.Context(), syncscope.Scope{SyncPathID: sp.ID, SharedConfig: true}, syncscope.Scope{LocalPath: filepath.Join(sp.LocalPath, "other.strm")})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := run(ctx, mediaFile, nil); err != nil {
			t.Fatalf("同一同步目录的独立刮削必须并行：%v", err)
		}
	})
}

func TestScrapeSTRMProtectsOldLocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp, mediaFile, run := setupScrapeSTRMScope(t, false)
		oldPath := filepath.Join(t.TempDir(), "old.strm")
		old := models.SyncFile{SyncPathId: sp.ID, SourceType: models.SourceTypeLocal, FileId: mediaFile.Media.VideoFileId, Path: "/old", FileName: "old.mkv", LocalFilePath: oldPath}
		if err := db.Db.Create(&old).Error; err != nil {
			t.Fatal(err)
		}
		release, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: oldPath})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := run(ctx, mediaFile, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("必须等待旧输出：%v", err)
		}
	})
}

func TestScrapeSTRMIndependentTasksRunTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp, mediaFile, run := setupScrapeSTRMScope(t, false)
		var blocked atomic.Bool
		entered, resume := make(chan struct{}), make(chan struct{})
		if err := db.Db.Callback().Create().Before("gorm:begin_transaction").Register("test:pause_first_scrape", func(tx *gorm.DB) {
			if tx.Statement.Table == "syncs" && blocked.CompareAndSwap(false, true) {
				close(entered)
				<-resume
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer db.Db.Callback().Create().Remove("test:pause_first_scrape")
		first := make(chan error, 1)
		go func() { first <- run(t.Context(), mediaFile, nil) }()
		<-entered
		next := *mediaFile
		media := *mediaFile.Media
		media.Path = "/media/other"
		media.VideoFileId = "/source/other.mkv"
		next.Media = &media
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		err := run(ctx, &next, nil)
		close(resume)
		if firstErr := <-first; firstErr != nil {
			t.Fatal(firstErr)
		}
		if err != nil {
			t.Fatalf("第一项仍持有配置保护时，独立刮削必须完成：%v", err)
		}
		for _, item := range []*models.ScrapeMediaFile{mediaFile, &next} {
			if _, err := os.Stat(scrapeSTRMVideoPath(sp, item, false)); err != nil {
				t.Fatal(err)
			}
		}
	})
}
