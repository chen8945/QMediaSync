package directoryupload

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"

	"gorm.io/gorm"
)

func TestBuildTriggerPlanSkipsTerminalProcessedRecord(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(700, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	info := statFileForTriggerPlanTest(t, filePath)
	state := buildProcessedSourceState(rule, "movie.mkv", info)
	oldSeenAt := clock.Now().Add(-time.Hour).Unix()
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		ScopeHash:         state.scopeHash,
		SourceKey:         state.sourceKey,
		RelativePath:      "movie.mkv",
		LocalFullPath:     filePath,
		SourceFingerprint: state.sourceFingerprint,
		FileSize:          state.fileSize,
		LocalMtimeNs:      state.mtimeNs,
		Result:            models.DirectoryUploadProcessedResultUploaded,
		ProcessedAt:       oldSeenAt,
		LastSeenAt:        oldSeenAt,
	}).Error; err != nil {
		t.Fatalf("创建终态 processed 记录失败: %v", err)
	}

	service := NewService(ServiceOptions{Now: clock.Now})
	plan, err := service.buildTriggerPlan(context.Background(), rule, "movie.mkv", filePath, info, handleStableFileOptions{})
	if err != nil {
		t.Fatalf("构建触发计划失败: %v", err)
	}
	if !plan.skip {
		t.Fatal("终态 processed 记录应生成跳过计划")
	}
	if got, want := plan.reasonString(), "stable_file,terminal_processed"; got != want {
		t.Fatalf("trigger reasons = %q，期望 %q", got, want)
	}
	if plan.sourceState.sourceKey != state.sourceKey || plan.sourceState.sourceFingerprint != state.sourceFingerprint {
		t.Fatalf("source state = %+v，期望 source_key=%s fingerprint=%s", plan.sourceState, state.sourceKey, state.sourceFingerprint)
	}
	var processed models.DirectoryUploadProcessedFile
	if err := db.Db.Where("source_key = ?", state.sourceKey).First(&processed).Error; err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if processed.LastSeenAt != clock.Now().Unix() {
		t.Fatalf("last_seen_at = %d，期望更新为 %d", processed.LastSeenAt, clock.Now().Unix())
	}

	cachedPlan, err := service.buildTriggerPlan(context.Background(), rule, "movie.mkv", filePath, info, handleStableFileOptions{})
	if err != nil {
		t.Fatalf("构建内存命中触发计划失败: %v", err)
	}
	if !cachedPlan.skip || !cachedPlan.hasReason(triggerReasonMemoryProcessed) {
		t.Fatalf("cached trigger plan = %+v，期望命中内存终态缓存", cachedPlan)
	}
}

func TestBuildTriggerPlanForceBypassesTerminalProcessedRecord(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(710, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	info := statFileForTriggerPlanTest(t, filePath)
	state := buildProcessedSourceState(rule, "movie.mkv", info)
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		ScopeHash:         state.scopeHash,
		SourceKey:         state.sourceKey,
		RelativePath:      "movie.mkv",
		LocalFullPath:     filePath,
		SourceFingerprint: state.sourceFingerprint,
		FileSize:          state.fileSize,
		LocalMtimeNs:      state.mtimeNs,
		Result:            models.DirectoryUploadProcessedResultUploaded,
		ProcessedAt:       clock.Now().Unix(),
		LastSeenAt:        clock.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建终态 processed 记录失败: %v", err)
	}

	service := NewService(ServiceOptions{Now: clock.Now})
	plan, err := service.buildTriggerPlan(context.Background(), rule, "movie.mkv", filePath, info, handleStableFileOptions{Force: true})
	if err != nil {
		t.Fatalf("构建强制重扫触发计划失败: %v", err)
	}
	if plan.skip {
		t.Fatalf("强制重扫不应被终态 processed 记录跳过: %+v", plan)
	}
	if got, want := plan.reasonString(), "stable_file,force_reprocess,create_upload_task"; got != want {
		t.Fatalf("trigger reasons = %q，期望 %q", got, want)
	}
	if plan.hasReason(triggerReasonTerminalProcessed) {
		t.Fatalf("强制重扫不应查询终态 processed 记录: %+v", plan)
	}
}

func TestBuildTriggerPlanForceKeepsActiveUploadTaskDedup(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(720, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	info := statFileForTriggerPlanTest(t, filePath)
	if err := db.Db.Create(&models.DbUploadTask{
		Source:         models.UploadSourceDirectoryMonitor,
		AccountId:      rule.AccountId,
		SyncPathId:     rule.SyncPathId,
		SourceType:     models.SourceType115,
		LocalFullPath:  filePath,
		RemoteFullPath: "/remote/movie.mkv",
		FileName:       "movie.mkv",
		Status:         models.UploadStatusUploading,
		FileSize:       info.Size(),
		UploadResult:   models.UploadResultUnknown,
	}).Error; err != nil {
		t.Fatalf("创建活跃上传任务失败: %v", err)
	}

	service := NewService(ServiceOptions{Now: clock.Now})
	plan, err := service.buildTriggerPlan(context.Background(), rule, "movie.mkv", filePath, info, handleStableFileOptions{Force: true})
	if err != nil {
		t.Fatalf("构建强制重扫触发计划失败: %v", err)
	}
	if !plan.skip {
		t.Fatal("强制重扫仍应被活跃上传任务去重")
	}
	if got, want := plan.reasonString(), "stable_file,force_reprocess,active_upload_task"; got != want {
		t.Fatalf("trigger reasons = %q，期望 %q", got, want)
	}
}

func TestHandleStableFileStopsWhenUploadTaskQueryFails(t *testing.T) {
	for _, mode := range []string{"query_failure", "cancel_during_query"} {
		t.Run(mode, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, "movie.mkv")
			writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(720, 0))
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			remote := &fakeRemoteClient{parentID: "remote-parent"}
			service := NewService(ServiceOptions{RemoteClient: remote})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("upload task query failed")
			if mode == "cancel_during_query" {
				cause = context.Canceled
			}
			queryAttempts := 0
			const callback = "test:directory_upload_local_path_failure"
			if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "db_upload_tasks" {
					return
				}
				queryAttempts++
				if mode == "cancel_during_query" {
					cancel()
				} else {
					tx.AddError(cause)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := service.HandleStableFile(ctx, rule, filePath)
			if removeErr := db.Db.Callback().Query().Remove(callback); removeErr != nil {
				t.Fatal(removeErr)
			}
			if !errors.Is(err, cause) || queryAttempts != 1 {
				t.Fatalf("应返回上传检查的错误: err=%v, queryAttempts=%d", err, queryAttempts)
			}
			if remote.ensureDirCalls != 0 || remote.findFileCalls != 0 || len(remote.deletedFileIDs) != 0 {
				t.Fatalf("查询失败后不应访问网盘: %+v", remote)
			}
			for _, model := range []any{&models.DbUploadTask{}, &models.DirectoryUploadProcessedFile{}} {
				var count int64
				if err := db.Db.Model(model).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("查询失败后不应写入 %T: count=%d", model, count)
				}
			}
			assertPathExists(t, filePath)
		})
	}
}

func statFileForTriggerPlanTest(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取测试文件失败: %v", err)
	}
	return info
}
