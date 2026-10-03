package syncstrm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestGenerationBatchConfigBoundary(t *testing.T) {
	account, first := setupStrmGenerationServiceTestDB(t)
	if err := db.Db.Model(first).Update("strm_base_url", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(models.SettingsGlobal).Update("strm_base_url", "http://old-global").Error; err != nil {
		t.Fatal(err)
	}
	second := *first
	second.ID = 0
	second.BaseCid = "second"
	second.LocalPath = t.TempDir()
	second.StrmBaseUrl = "http://old-directory"
	if err := db.Db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	enqueue := func(path *models.SyncPath, n int) {
		t.Helper()
		_, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: path.ID, AccountId: account.ID, FileSize: 1024, Mtime: 1, Sha1: "test-sha", FileId: fmt.Sprint(n), ParentId: path.BaseCid, PickCode: fmt.Sprint(n), Path: "/remote", FileName: fmt.Sprintf("movie%d.mkv", n)})
		if err != nil {
			t.Fatal(err)
		}
	}
	enqueue(first, 1)
	enqueue(first, 2)
	enqueue(&second, 3)
	service := NewStrmGenerationService()
	service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
		t.Fatal("完整夹具不应请求网盘")
		return nil, nil
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
	service.acquireRefreshSubmission = func(context.Context) (func(), error) { return func() {}, nil }
	service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) { return true, nil }
	var urls []string
	original := service.processStrmFile
	service.processStrmFile = func(syncer *SyncStrm, file *SyncFileCache) error {
		urls = append(urls, syncer.Config.StrmBaseUrl)
		if len(urls) == 1 {
			if err := db.Db.Model(models.SettingsGlobal).Update("strm_base_url", "http://new-global").Error; err != nil {
				return err
			}
			if err := db.Db.Model(&second).Update("strm_base_url", "http://new-directory").Error; err != nil {
				return err
			}
		}
		if err := original(syncer, file); err != nil {
			return err
		}
		data, err := os.ReadFile(file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(data), syncer.Config.StrmBaseUrl) {
			t.Fatalf("STRM 内容未使用本项配置：%s", data)
		}
		return nil
	}
	settingsQueries := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("count_batch_settings", func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			settingsQueries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); err != nil || n != 3 {
		t.Fatalf("first batch: %d %v", n, err)
	}
	if settingsQueries != 1 {
		t.Fatalf("配置查询 = %d，期望每批一次", settingsQueries)
	}
	enqueue(first, 4)
	enqueue(&second, 5)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); err != nil || n != 2 {
		t.Fatalf("second batch: %d %v", n, err)
	}
	want := "http://old-global,http://old-global,http://old-directory,http://new-global,http://new-directory"
	if strings.Join(urls, ",") != want {
		var tasks []models.StrmGenerationTask
		db.Db.Find(&tasks)
		t.Fatalf("批次配置：%v tasks=%+v", urls, tasks)
	}
	if settingsQueries != 2 {
		t.Fatalf("两批配置查询 = %d", settingsQueries)
	}
}

func TestGenerationConfigInheritanceAndIsolation(t *testing.T) {
	defaults := models.SettingStrm{StrmBaseUrl: "global", DownloadMeta: 1, UploadMeta: 2, DeleteDir: 1, CheckMetaMtime: 1, AddPath: 3, MinVideoSize: 42, VideoExtArr: []string{".mkv"}, MetaExtArr: []string{".nfo"}, ExcludeNameArr: []string{"skip"}, ExcludeNameRegexArr: []string{"^skip"}}
	path := &models.SyncPath{SettingStrm: models.GetStrmSettingDefault(), SourceType: models.SourceTypeOpenList}
	config := configFromSyncPathDefaults(path, defaults)
	if config.StrmBaseUrl != "" || config.MinVideoSize != 42 || config.EnableDownloadMeta != 1 || !config.DelEmptyLocalDir || config.StrmUrlNeedPath != 3 || config.CheckMetaMtime != 1 || config.NetNotFoundFileAction != 2 {
		t.Fatalf("继承错误：%+v", config)
	}
	path.StrmBaseUrl = "custom"
	path.MinVideoSize = 0
	path.VideoExtArr = []string{".mp4"}
	custom := configFromSyncPathDefaults(path, defaults)
	if custom.StrmBaseUrl != "custom" || custom.MinVideoSize != 0 || custom.VideoExt[0] != ".mp4" {
		t.Fatalf("自定义错误：%+v", custom)
	}
	custom.VideoExt[0] = "changed"
	config.MetaExt[0] = "changed"
	if path.VideoExtArr[0] != ".mp4" || defaults.MetaExtArr[0] != ".nfo" {
		t.Fatal("配置数组被共享")
	}
	configs := generationConfigs{{models.SourceType115, 1, 1}: config}
	service := NewStrmGenerationService()
	for _, key := range []generationConfigKey{{models.SourceTypeOpenList, 1, 1}, {models.SourceType115, 2, 1}, {models.SourceType115, 1, 2}} {
		path.SourceType, path.AccountId, path.ID = key.source, key.accountID, key.syncPathID
		if _, err := service.buildGenerationSyncer(path, &models.Account{}, configs); err == nil {
			t.Fatal("不同来源、账号或目录共享了配置")
		}
	}
}

func TestGenerationConfigFinalizingAndCancellation(t *testing.T) {
	_, path := setupStrmGenerationServiceTestDB(t)
	queries := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("count_config_queries", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	tasks := []*models.StrmGenerationTask{{SyncPathId: path.ID, Status: models.StrmGenerationStatusFinalizing}}
	if _, err := prepareGenerationConfigs(t.Context(), tasks); err != nil || queries != 0 {
		t.Fatalf("finalizing: queries=%d err=%v", queries, err)
	}
	tasks[0].Status = models.StrmGenerationStatusPending
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepareGenerationConfigs(ctx, tasks); err != context.Canceled || queries != 0 {
		t.Fatalf("cancel: queries=%d err=%v", queries, err)
	}
	config := SyncStrmConfig{VideoExt: []string{".mkv"}, MetaExt: []string{".nfo"}, ExcludeNames: []string{"skip"}, ExcludeNameRegexes: []string{"^skip"}}
	clone := cloneGenerationConfig(config)
	clone.VideoExt[0], clone.MetaExt[0], clone.ExcludeNames[0], clone.ExcludeNameRegexes[0] = "x", "x", "x", "x"
	if config.VideoExt[0] != ".mkv" || config.MetaExt[0] != ".nfo" || config.ExcludeNames[0] != "skip" || config.ExcludeNameRegexes[0] != "^skip" {
		t.Fatal("不同任务共享可变配置数组")
	}
}
