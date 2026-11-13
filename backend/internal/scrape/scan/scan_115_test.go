package scan

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	v115open "qmediasync/internal/v115/openapi"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func Test115ScanExcludesPlaybackDirectoryAndSubtree(t *testing.T) {
	for _, tt := range []struct {
		name   string
		rootID string
		want   int
	}{
		{name: "根目录扫描保留非根目录同名路径", rootID: "0", want: 1},
		{name: "直接选中临时目录", rootID: "playback"},
		{name: "直接选中临时子目录", rootID: "playback-child"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			originalDB, originalLogger := db.Db, helpers.AppLogger
			originalList, originalSettings := list115ScanFilesPage, models.SettingsGlobal
			t.Cleanup(func() {
				db.Db, helpers.AppLogger = originalDB, originalLogger
				list115ScanFilesPage, models.SettingsGlobal = originalList, originalSettings
			})
			testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := testDB.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			if err := testDB.AutoMigrate(&models.ScrapeMediaFile{}); err != nil {
				t.Fatal(err)
			}
			db.Db = testDB
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			models.SettingsGlobal = &models.Settings{FileListPageSize: 100}
			paths := map[string]string{
				"0": "", "playback": "多端播放", "playback-child": "多端播放/child",
				"media": "Media", "nested": "Media/多端播放",
			}
			files := map[string][]v115open.File{
				"0": {
					{FileId: "playback", FileName: "多端播放", FileCategory: v115open.TypeDir, Aid: "1"},
					{FileId: "media", FileName: "Media", FileCategory: v115open.TypeDir, Aid: "1"},
				},
				"playback": {
					{FileId: "copy", FileName: "copy.mkv", FileCategory: v115open.TypeFile, Aid: "1"},
					{FileId: "playback-child", FileName: "child", FileCategory: v115open.TypeDir, Aid: "1"},
				},
				"playback-child": {{FileId: "child-copy", FileName: "copy.mkv", FileCategory: v115open.TypeFile, Aid: "1"}},
				"media":          {{FileId: "nested", FileName: "多端播放", FileCategory: v115open.TypeDir, Aid: "1"}},
				"nested":         {{FileId: "movie", FileName: "movie.mkv", FileCategory: v115open.TypeFile, Aid: "1"}},
			}
			calls := make(map[string]int)
			list115ScanFilesPage = func(_ context.Context, _ *v115open.OpenClient, parentID string, _, _ int) (*v115open.FileListResp, error) {
				calls[parentID]++
				return &v115open.FileListResp{
					RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: files[parentID]},
					PathStr:      paths[parentID], Count: len(files[parentID]),
				}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			scanner := New115ScanImpl(&models.ScrapePath{
				ID: 1, SourceType: models.SourceType115,
				MediaType: models.MediaTypeMovie, VideoExtList: []string{".mkv"},
			}, nil, ctx)
			scanner.pathTasks = make(chan string, 10)
			scanner.wg.Add(1)
			scanner.pathTasks <- tt.rootID
			done := make(chan struct{})
			go func() {
				scanner.wg.Wait()
				close(scanner.pathTasks)
			}()
			go func() {
				defer close(done)
				scanner.startPathWorkWithLimiter(0)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("扫描未在限定时间内结束")
			}
			var scanned []models.ScrapeMediaFile
			if err := db.Db.Find(&scanned).Error; err != nil {
				t.Fatal(err)
			}
			if len(scanned) != tt.want {
				t.Fatalf("入库 %d 个刮削文件，期望 %d：%+v", len(scanned), tt.want, scanned)
			}
			if len(scanned) == 1 && (scanned[0].VideoFileId != "movie" || scanned[0].Path != "Media/多端播放") {
				t.Fatalf("只应保留非根目录同名目录中的视频：%+v", scanned[0])
			}
			if tt.rootID == "0" && (calls["playback"] != 0 || calls["playback-child"] != 0) {
				t.Fatalf("扫描不应进入临时目录：%v", calls)
			}
		})
	}
}

func Test115ScanKeepsCapturedPageSizeAcrossDirectories(t *testing.T) {
	oldDB, oldSettings, oldLogger := db.Db, models.SettingsGlobal, helpers.AppLogger
	oldList := list115ScanFilesPage
	oldThreads, oldProxy := models.GetThreadSettingsSnapshot(), models.GetHTTPProxy()
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := testDB.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatal(err)
	}
	settings := &models.Settings{FileDetailThreads: 1, FileListPageSize: 100, HttpProxy: oldProxy}
	if err := testDB.Create(settings).Error; err != nil {
		t.Fatal(err)
	}
	db.Db = testDB
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.SettingsGlobal = nil
	models.LoadSettings()
	t.Cleanup(func() {
		if err := testDB.Model(settings).Updates(oldThreads.ToMap()).Error; err != nil {
			t.Error(err)
		}
		models.LoadSettings()
		db.Db, models.SettingsGlobal, helpers.AppLogger = oldDB, oldSettings, oldLogger
		list115ScanFilesPage = oldList
		_ = sqlDB.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scanner := New115ScanImpl(&models.ScrapePath{SourceType: models.SourceType115}, nil, ctx)
	limits := make([]int, 0, 2)
	list115ScanFilesPage = func(_ context.Context, _ *v115open.OpenClient, parentID string, _, limit int) (*v115open.FileListResp, error) {
		limits = append(limits, limit)
		if parentID == "0" {
			if err := testDB.Model(settings).Update("file_list_page_size", 200).Error; err != nil {
				return nil, err
			}
			models.LoadSettings()
			return &v115open.FileListResp{Count: 1, RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: []v115open.File{{FileId: "child", FileName: "child", FileCategory: v115open.TypeDir, Aid: "1"}}}}, nil
		}
		return &v115open.FileListResp{}, nil
	}
	scanner.pathTasks = make(chan string, 2)
	scanner.wg.Add(1)
	scanner.pathTasks <- "0"
	done := make(chan struct{})
	go func() {
		scanner.wg.Wait()
		close(scanner.pathTasks)
	}()
	go func() { defer close(done); scanner.startPathWorkWithLimiter(0) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan did not finish")
	}
	if len(limits) != 2 || limits[0] != 100 || limits[1] != 100 || models.GetFileListPageSize() != 200 {
		t.Fatalf("scan mixed newly saved page size into its snapshot: %v", limits)
	}
}
