package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

func TestSyncExclusionsAtStartPersistSkippedFiles(t *testing.T) {
	for _, tt := range []struct {
		name        string
		sourcePath  string
		config      SyncStrmConfig
		isFile      bool
		wantSkipped int
	}{
		{
			name: "file_name", sourcePath: "/Media/movie.mkv", isFile: true, wantSkipped: 1,
			config: SyncStrmConfig{ExcludeNames: []string{"movie.mkv"}},
		},
		{
			name: "ancestor_regex", sourcePath: "/Media/Extras/Season 1/movie.mkv", isFile: true, wantSkipped: 1,
			config: SyncStrmConfig{ExcludeNameRegexes: []string{`^Extras$`}},
		},
		{
			name: "directory", sourcePath: "/Media/Extras",
			config: SyncStrmConfig{ExcludeNames: []string{"extras"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account, _ := setupStrmExclusionTestDB(t)
				s := newSyncStrm(account, 0, tt.sourcePath, "entry", t.TempDir(), tt.config, false, 0, tt.isFile, true)
				if s == nil {
					t.Fatal("创建同步器失败")
				}
				s.SyncDriver = &excludedEntryDriver{}
				s.backgroundService = newSyncBackgroundService()
				t.Cleanup(func() {
					s.Cancel()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := s.backgroundService.stop(ctx); err != nil {
						t.Error(err)
					}
				})
				oldDownload, oldUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
				models.GlobalDownloadQueue, models.GlobalUploadQueue = models.NewDq(0), models.NewUq(1)
				defer func() {
					models.GlobalDownloadQueue.Stop()
					models.GlobalUploadQueue.Stop()
					time.Sleep(100 * time.Millisecond)
					synctest.Wait()
					models.GlobalDownloadQueue, models.GlobalUploadQueue = oldDownload, oldUpload
				}()
				if err := s.Start(); err != nil {
					t.Fatal(err)
				}
				waitBackgroundTest(t, s.backgroundService)
				saved := readBackgroundTestSync(t, s.Sync.ID)
				if saved.Status != models.SyncStatusCompleted || saved.ScanResult == nil {
					t.Fatalf("排除入口未保存完成结果：status=%d result=%+v", saved.Status, saved.ScanResult)
				}
				if result := saved.ScanResult; result.SucceededFiles != 0 || result.FailedFiles != 0 ||
					result.SkippedFiles != tt.wantSkipped || len(result.Failures) != 0 {
					t.Errorf("持久化排除结果=%+v，期望跳过 %d 个文件", result, tt.wantSkipped)
				}
				if saved.NewStrm != 0 || saved.NewMeta != 0 || saved.NewUpload != 0 {
					t.Errorf("排除入口产生新任务：strm=%d meta=%d upload=%d", saved.NewStrm, saved.NewMeta, saved.NewUpload)
				}
				if entries, err := os.ReadDir(s.TargetPath); err != nil || len(entries) != 0 {
					t.Errorf("排除入口产生输出文件：entries=%v err=%v", entries, err)
				}
				for _, model := range []any{&models.DbDownloadTask{}, &models.DbUploadTask{}} {
					var count int64
					if err := db.Db.Model(model).Count(&count).Error; err != nil || count != 0 {
						t.Errorf("排除入口创建传输任务：%T count=%d err=%v", model, count, err)
					}
				}
			})
		})
	}
}

type excludedEntryDriver struct{ driverImpl }

func (*excludedEntryDriver) DetailByFileId(context.Context, string) (*SyncFileCache, error) {
	return nil, errors.New("排除入口不应查询文件详情")
}

func (*excludedEntryDriver) GetPathIdByPath(context.Context, string) (string, error) {
	return "", errors.New("排除入口不应查询目录 ID")
}

func TestSyncExclusionsPreserveDirectoryAndLedger(t *testing.T) {
	tests := []struct {
		name    string
		action  models.SyncTreeItemMetaAction
		meta    int64
		pattern bool
	}{
		{name: "upload", action: models.SyncTreeItemMetaActionUpload, meta: 1},
		{name: "delete_regex", action: models.SyncTreeItemMetaActionDelete, meta: 1, pattern: true},
		{name: "keep", action: models.SyncTreeItemMetaActionKeep, meta: 1},
		{name: "metadata_disabled", action: models.SyncTreeItemMetaActionDelete},
	}
	for _, tt := range tests {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%v", tt.name, cached), func(t *testing.T) {
				s, directory := new115UploadParentSync(t)
				s.Config.NetNotFoundFileAction, s.Config.EnableDownloadMeta = tt.action, tt.meta
				s.Config.DelEmptyLocalDir = true
				if tt.pattern {
					s.Config.ExcludeNameRegexes = []string{`(?i)^empty$`}
				} else {
					s.Config.ExcludeNames = []string{"empty"}
				}
				if err := s.Config.compileExcludeNameRegexes(); err != nil {
					t.Fatal(err)
				}
				rows := []models.SyncFile{
					exclusionTestFile(s, "video", "Media/Empty/Nested", "movie.mkv", true),
					exclusionTestFile(s, "meta", "Media/Empty/Nested", "movie.nfo", false),
					exclusionTestFile(s, "stale", "Media/ZSibling", "stale.mkv", true),
				}
				if err := db.Db.Create(&rows).Error; err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					writeScopeFile(t, row.LocalFilePath)
				}
				empty := filepath.Join(directory.LocalFilePath, "EmptyChild")
				if err := os.MkdirAll(empty, 0755); err != nil {
					t.Fatal(err)
				}
				if cached {
					for _, row := range append([]models.SyncFile{directory}, rows[:2]...) {
						file := ledgerTestCache(row, false)
						file.FileType, file.IsVideo, file.IsMeta = row.FileType, row.IsVideo, row.IsMeta
						file.MTime++
						if err := s.memSyncCache.Insert(file); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := s.compareLocalFilesWithTempTable(); err != nil {
					t.Fatal(err)
				}
				for _, row := range rows[:2] {
					requireScopeFile(t, row.LocalFilePath, true)
				}
				requireScopeFile(t, empty, true)
				requireScopeFile(t, rows[2].LocalFilePath, false)
				requireExclusionCompleted(t, s)
				if result := s.scanResultSnapshot(); result.SkippedFiles != 0 {
					t.Fatalf("未遍历排除子树，不应推算跳过文件数：%+v", result)
				}
				view := s.backgroundLedgerView(s.Sync)
				view.Context = t.Context()
				if err := view.handleTempTableDiff(); err != nil {
					t.Fatal(err)
				}
				requireExcludedLedger(t, append([]models.SyncFile{directory}, rows[:2]...), rows[2])
			})
		}
	}
}

func TestSyncExclusionsPreserveSourceNamedSTRM(t *testing.T) {
	for _, source := range []models.SourceType{models.SourceType115, models.SourceTypeBaiduPan, models.SourceTypeOpenList, models.SourceTypeLocal} {
		t.Run(string(source), func(t *testing.T) {
			account, sp := setupStrmExclusionTestDB(t)
			account.SourceType = source
			s := newSyncStrm(account, sp.ID, "/Media", "root", t.TempDir(), SyncStrmConfig{
				VideoExt: []string{".mkv"}, ExcludeNameRegexes: []string{`^movie\.mkv$`}, DelEmptyLocalDir: true,
			}, false, 0, false, false)
			t.Cleanup(s.Cancel)
			rows := []models.SyncFile{
				exclusionTestFile(s, "movie", "/Media/Film", "movie.mkv", true),
				exclusionTestFile(s, "stale", "/Media/Film", "stale.mkv", true),
			}
			if source == models.SourceTypeBaiduPan {
				rows[0].LocalFilePath = filepath.Join(s.GetLocalBaseDir(), "Previous", "saved.strm")
			}
			if err := db.Db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				writeScopeFile(t, row.LocalFilePath)
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, rows[0].LocalFilePath, true)
			requireScopeFile(t, rows[1].LocalFilePath, false)
			requireExclusionCompleted(t, s)
			view := s.backgroundLedgerView(s.Sync)
			view.Context = t.Context()
			if err := view.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			requireExcludedLedger(t, rows[:1], rows[1])
		})
	}
}

func TestSyncExclusionsPreserveLocalSTRMName(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cached  bool
		oldRoot bool
	}{
		{name: "current_root"},
		{name: "current_root_cached", cached: true},
		{name: "previous_root", oldRoot: true},
		{name: "previous_root_cached", cached: true, oldRoot: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := new115UploadParentSync(t)
			s.Config.ExcludeNames = []string{"movie.strm"}
			s.Config.EnableDownloadMeta = 0
			rows := []models.SyncFile{
				exclusionTestFile(s, "movie", "Media/Film", "movie.mkv", true),
				exclusionTestFile(s, "stale", "Media/Film", "stale.mkv", true),
			}
			if tt.oldRoot {
				rows[0].LocalFilePath = filepath.Join(t.TempDir(), "movie.strm")
			}
			if err := db.Db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				writeScopeFile(t, row.LocalFilePath)
			}
			if tt.cached {
				file := ledgerTestCache(rows[0], false)
				file.IsVideo = true
				file.MTime++
				if err := s.memSyncCache.Insert(file); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, rows[0].LocalFilePath, true)
			requireScopeFile(t, rows[1].LocalFilePath, false)
			requireExclusionCompleted(t, s)
			view := s.backgroundLedgerView(s.Sync)
			view.Context = t.Context()
			if err := view.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			requireExcludedLedger(t, rows[:1], rows[1])
		})
	}
}

func TestSyncExclusionsMetadataNameSkipsOnlyMatchedFile(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", cached), func(t *testing.T) {
			s, _ := new115UploadParentSync(t)
			s.SyncDriver = &uploadParent115Driver{}
			s.Config.ExcludeNames = []string{"movie.nfo"}
			s.Config.CheckMetaMtime = 1
			rows := []models.SyncFile{
				exclusionTestFile(s, "meta", "Media/Empty/extras", "movie.nfo", false),
				exclusionTestFile(s, "stale", "Media/Empty/extras", "stale.mkv", true),
			}
			if err := db.Db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				writeScopeFile(t, row.LocalFilePath)
			}
			if cached {
				file := ledgerTestCache(rows[0], false)
				file.IsMeta = true
				file.MTime = 4102444800
				if err := s.memSyncCache.Insert(file); err != nil {
					t.Fatal(err)
				}
				s.AddDownloadTaskTemp(file)
			}
			if err := s.AddDownloadTaskFromMemCache(); err != nil {
				t.Fatal(err)
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, rows[0].LocalFilePath, true)
			requireScopeFile(t, rows[1].LocalFilePath, false)
			requireExclusionCompleted(t, s)
			for _, model := range []any{&models.DbDownloadTask{}, &models.DbUploadTask{}} {
				var count int64
				if err := db.Db.Model(model).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("排除元数据仍创建传输任务：%T count=%d err=%v", model, count, err)
				}
			}
			if err := s.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			requireExcludedLedger(t, rows[:1], rows[1])
		})
	}
}

func TestSyncExclusionsRemoteScanSkipsGenerationAndDownloads(t *testing.T) {
	_, sp := setupStrmExclusionTestDB(t)
	source := t.TempDir()
	for _, name := range []string{"extras/movie.mkv", "extras/movie.nfo", "movie.mkv", "movie.nfo", "skip.nfo"} {
		writeScopeFile(t, filepath.Join(source, name))
	}
	s := newSyncStrm(nil, sp.ID, source, source, t.TempDir(), SyncStrmConfig{
		VideoExt: []string{".mkv"}, MetaExt: []string{".nfo"}, EnableDownloadMeta: 1,
		ExcludeNames: []string{"extras", "skip.nfo"},
	}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.StartOther()
	select {
	case err := <-s.PathErrChan:
		t.Fatal(err)
	default:
	}
	if err := s.AddDownloadTaskFromMemCache(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, filepath.Join(s.TargetPath, "movie.strm"), true)
	requireScopeFile(t, filepath.Join(s.TargetPath, "extras", "movie.strm"), false)
	var downloads []models.DbDownloadTask
	if err := db.Db.Find(&downloads).Error; err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 1 || downloads[0].LocalFullPath != filepath.Join(s.TargetPath, "movie.nfo") || s.NewMeta != 1 || s.NewStrm != 1 || s.NewUpload != 0 {
		t.Fatalf("downloads=%+v meta=%d strm=%d upload=%d", downloads, s.NewMeta, s.NewStrm, s.NewUpload)
	}
	if status, err := s.scanOutcome(); status != models.SyncStatusCompleted || err != nil || s.scanResultSnapshot().SkippedFiles != 1 {
		t.Fatalf("排除未正常跳过：status=%d err=%v result=%+v", status, err, s.scanResultSnapshot())
	}
}

func TestSyncExclusionsProtectBeforeGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, sp := setupStrmExclusionTestDB(t)
		source, target := t.TempDir(), t.TempDir()
		for _, name := range []string{"movie.mp4", "independent.mkv"} {
			writeScopeFile(t, filepath.Join(source, name))
		}
		if err := db.Db.Model(sp).Updates(map[string]any{
			"source_type": models.SourceTypeLocal, "account_id": 0, "remote_path": source, "local_path": target,
			"custom_config": true, "exclude_name": `["movie.mkv"]`, "video_ext": `[".mkv", ".mp4"]`,
		}).Error; err != nil {
			t.Fatal(err)
		}
		s := newSyncStrm(nil, sp.ID, source, source, target, SyncStrmConfig{
			VideoExt: []string{".mkv", ".mp4"}, ExcludeNames: []string{"movie.mkv"},
		}, false, 0, false, true)
		s.backgroundService = newSyncBackgroundService()
		t.Cleanup(func() {
			s.Cancel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.backgroundService.stop(ctx); err != nil {
				t.Error(err)
			}
		})
		old := exclusionTestFile(s, "movie", source, "movie.mkv", true)
		if err := db.Db.Create(&old).Error; err != nil {
			t.Fatal(err)
		}
		writeScopeFile(t, old.LocalFilePath)
		oldDownload, oldUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
		models.GlobalDownloadQueue, models.GlobalUploadQueue = models.NewDq(0), models.NewUq(1)
		defer func() {
			models.GlobalDownloadQueue.Stop()
			models.GlobalUploadQueue.Stop()
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
			models.GlobalDownloadQueue, models.GlobalUploadQueue = oldDownload, oldUpload
		}()
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		waitBackgroundTest(t, s.backgroundService)
		if content, err := os.ReadFile(old.LocalFilePath); err != nil || string(content) != "original" {
			t.Fatalf("排除保护晚于生成：content=%q err=%v", content, err)
		}
		requireScopeFile(t, filepath.Join(target, "independent.strm"), true)
		var remaining models.SyncFile
		if err := db.Db.First(&remaining, old.ID).Error; err != nil || !reflect.DeepEqual(remaining, old) {
			t.Fatalf("后台未保留排除源文件身份：row=%+v err=%v", remaining, err)
		}
		if status, err := s.scanOutcome(); status != models.SyncStatusCompleted || err != nil || s.NewStrm != 1 || s.scanResultSnapshot().SkippedFiles != 1 {
			t.Fatalf("result=%+v strm=%d status=%d err=%v", s.scanResultSnapshot(), s.NewStrm, status, err)
		}
	})
}

func TestSyncExclusionsPreparationQueries(t *testing.T) {
	for _, mode := range []string{"no_rules", "paged_once", "database_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := new115UploadParentSync(t)
			if mode != "no_rules" {
				s.Config.ExcludeNames = []string{"movie.mkv"}
			}
			rows := make([]models.SyncFile, syncLedgerPageSize+1)
			for i := range rows {
				rows[i] = exclusionTestFile(s, fmt.Sprint(i), "Media", fmt.Sprintf("other-%d.mkv", i), true)
			}
			rows[len(rows)-1].FileName = "movie.mkv"
			rows[len(rows)-1].LocalFilePath = s.MakeFullLocalPath(&rows[len(rows)-1])
			if err := db.Db.CreateInBatches(&rows, 32).Error; err != nil {
				t.Fatal(err)
			}
			queries := 0
			wantErr := errors.New("ledger unavailable")
			if err := db.Db.Callback().Query().Before("gorm:query").Register("exclusions:count", func(tx *gorm.DB) {
				if tx.Statement.Table == "sync_files" {
					queries++
					if mode == "database_failure" {
						tx.AddError(wantErr)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Db.Callback().Query().Remove("exclusions:count") })
			err := s.prepareSyncExclusions()
			if mode == "database_failure" {
				if !errors.Is(err, wantErr) || !isFatalSyncError(err) {
					t.Fatalf("必要账本错误未停止执行：%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.prepareSyncExclusions(); err != nil {
				t.Fatal(err)
			}
			wantQueries := 0
			if mode == "paged_once" {
				wantQueries = 2
				if !s.localPathExcluded(rows[len(rows)-1].LocalFilePath) {
					t.Fatal("最后一页的排除项未受保护")
				}
			}
			if queries != wantQueries {
				t.Fatalf("queries=%d want=%d", queries, wantQueries)
			}
		})
	}
}

func exclusionTestFile(s *SyncStrm, id, parent, name string, video bool) models.SyncFile {
	row := models.SyncFile{SyncPathId: s.SyncPathId, AccountId: s.Account.ID, SourceType: s.Account.SourceType,
		FileId: id, FileName: name, ParentId: "parent", Path: parent, FileType: v115open.TypeFile,
		IsVideo: video, IsMeta: !video, MTime: 1, PickCode: id}
	if s.Account.SourceType != models.SourceType115 {
		row.ParentId, row.FileId = parent, filepath.Join(parent, name)
	}
	row.LocalFilePath = s.MakeFullLocalPath(&row)
	return row
}

func requireExclusionCompleted(t *testing.T, s *SyncStrm) {
	t.Helper()
	if status, err := s.scanOutcome(); status != models.SyncStatusCompleted || err != nil || len(s.scanResultSnapshot().Failures) != 0 {
		t.Fatalf("排除导致扫描失败：status=%d err=%v result=%+v", status, err, s.scanResultSnapshot())
	}
	if s.NewMeta != 0 || s.NewStrm != 0 || s.NewUpload != 0 {
		t.Fatalf("排除项产生新任务：meta=%d strm=%d upload=%d", s.NewMeta, s.NewStrm, s.NewUpload)
	}
}

func requireExcludedLedger(t *testing.T, preserved []models.SyncFile, deleted models.SyncFile) {
	t.Helper()
	for _, want := range preserved {
		var got models.SyncFile
		if err := db.Db.First(&got, want.ID).Error; err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("排除账本未原样保留：got=%+v want=%+v err=%v", got, want, err)
		}
	}
	if err := db.Db.First(&models.SyncFile{}, deleted.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("未排除兄弟账本未清理：%v", err)
	}
}
