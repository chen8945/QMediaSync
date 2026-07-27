package directoryupload

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestCleanupSourceAfterStrmSuccessConcurrentTriggersConverge(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	uploadTask := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, uploadTask.ID, models.StrmGenerationStatusCompleted)

	oldRemove := removeSourceFile
	var entered atomic.Int32
	bothEntered := make(chan struct{})
	release := make(chan struct{})
	removeSourceFile = func(path string) error {
		if entered.Add(1) == 2 {
			close(bothEntered)
		}
		<-release
		return os.Remove(path)
	}
	t.Cleanup(func() { removeSourceFile = oldRemove })

	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsCh <- CleanupSourceAfterStrmSuccess(uploadTask.ID)
		}()
	}
	<-bothEntered
	close(release)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("并发清理失败: %v", err)
		}
	}
	assertPathMissing(t, filePath)
	var updated models.DbUploadTask
	if err := db.Db.First(&updated, uploadTask.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if updated.SourceCleanupStatus != models.UploadSourceCleanupStatusCompleted {
		t.Fatalf("并发清理后状态 = %s，期望 completed", updated.SourceCleanupStatus)
	}
}

func TestCleanupCompletedStrmDependenciesRetriesTransientDeleteFailure(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	uploadTask := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, uploadTask.ID, models.StrmGenerationStatusCompleted)

	oldRemove := removeSourceFile
	removeSourceFile = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { removeSourceFile = oldRemove })
	if _, err := CleanupCompletedStrmDependencies(100); err == nil {
		t.Fatal("首次临时删除失败应返回错误")
	}

	var afterFailure models.DbUploadTask
	if err := db.Db.First(&afterFailure, uploadTask.ID).Error; err != nil {
		t.Fatalf("读取首次清理后的上传任务失败: %v", err)
	}
	if afterFailure.SourceCleanupStatus != models.UploadSourceCleanupStatusPending {
		t.Fatalf("临时删除失败后状态 = %s，期望 pending 以便补偿重试", afterFailure.SourceCleanupStatus)
	}

	removeSourceFile = oldRemove
	cleaned, err := CleanupCompletedStrmDependencies(100)
	if err != nil {
		t.Fatalf("补偿重试失败: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("补偿重试清理数量 = %d，期望 1", cleaned)
	}
	assertPathMissing(t, filePath)
}

func TestCleanupSourceAfterStrmSuccessDeletesFileAndEmptyParents(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	logBuf := setDirectoryUploadTestLogger(t)
	monitorPath := t.TempDir()
	nested := filepath.Join(monitorPath, "show", "Season 01")
	if err := ensureDir(nested); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}
	filePath := filepath.Join(nested, "episode.mkv")
	writeFileWithMtime(t, filePath, []byte("episode"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatalf("源文件清理失败: %v", err)
	}
	assertPathMissing(t, filePath)
	assertPathMissing(t, nested)
	assertPathMissing(t, filepath.Join(monitorPath, "show"))
	assertPathExists(t, monitorPath)

	var got models.DbUploadTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if got.SourceCleanupStatus != models.UploadSourceCleanupStatusCompleted || got.SourceDeletedAt == 0 || got.SourceCleanupError != "" {
		t.Fatalf("清理状态 = %+v，期望 completed", got)
	}
	logOutput := logBuf.String()
	for _, want := range []string{
		"[目录上传] 已删除源文件",
		"upload_task_id=",
		"rule_id=",
		"path=" + filePath,
		"result=multipart_uploaded",
		"remote_file_id=remote-file",
		"[目录上传] 已删除空目录",
		"path=" + nested,
		"path=" + filepath.Join(monitorPath, "show"),
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s", want, logOutput)
		}
	}
}

func TestCleanupSourceAfterStrmSuccessUsesQueuedRuleWhenDisabledChildOverlaps(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := ensureDir(child); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	filePath := filepath.Join(child, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	syncPath, parentRule := createDirectoryUploadRuleForTest(t, parent)
	parentRule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(parentRule).Error; err != nil {
		t.Fatalf("保存父级目录上传规则失败: %v", err)
	}
	childRule := &models.DirectoryUploadRule{
		SyncPathId:                    syncPath.ID,
		AccountId:                     parentRule.AccountId,
		Enabled:                       false,
		MonitorPath:                   child,
		RemoteRootPath:                "/remote/child",
		RemoteRootId:                  "remote-child",
		Recursive:                     true,
		WatchMode:                     models.DirectoryUploadWatchModeAuto,
		StabilitySeconds:              0,
		StabilityCheckIntervalSeconds: 1,
		StabilityRequiredCount:        1,
		ProcessedCacheTTLSeconds:      600,
		DeleteSourceAfterSuccess:      false,
	}
	if err := db.Db.Create(childRule).Error; err != nil {
		t.Fatalf("创建停用子规则失败: %v", err)
	}
	childRule.Enabled = false
	if err := db.Db.Save(childRule).Error; err != nil {
		t.Fatalf("停用子规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, parentRule, filePath, models.UploadResultMultipartUploaded)
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            parentRule.ID,
		SyncPathId:        syncPath.ID,
		AccountId:         parentRule.AccountId,
		ScopeHash:         models.BuildDirectoryUploadScopeHash(parentRule),
		SourceKey:         models.BuildDirectoryUploadSourceKey(models.BuildDirectoryUploadScopeHash(parentRule), task.RelativePath),
		RelativePath:      task.RelativePath,
		LocalFullPath:     filePath,
		SourceFingerprint: task.SourceFingerprint,
		FileSize:          task.FileSize,
		LocalMtimeNs:      task.LocalMtimeNs,
		Result:            models.DirectoryUploadProcessedResultUploadedPendingStrm,
		UploadTaskId:      task.ID,
		ProcessedAt:       time.Now().Unix(),
		LastSeenAt:        time.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建目录监控处理记录失败: %v", err)
	}
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatalf("源文件清理失败: %v", err)
	}
	assertPathMissing(t, filePath)
	assertPathMissing(t, child)
	assertPathExists(t, parent)
}

func TestCleanupCompletedStrmDependenciesCompensatesMissedImmediateEvent(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	syncPath, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	uploadTask := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	strmTask := &models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: uploadTask.ID,
		SyncPathId:   syncPath.ID,
		Status:       models.StrmGenerationStatusCompleted,
	}
	if err := db.Db.Create(strmTask).Error; err != nil {
		t.Fatalf("创建已完成 STRM 任务失败: %v", err)
	}
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        syncPath.ID,
		AccountId:         rule.AccountId,
		SourceKey:         "dependency-compensation-source",
		LocalFullPath:     filePath,
		SourceFingerprint: uploadTask.SourceFingerprint,
		Result:            models.DirectoryUploadProcessedResultUploaded,
		UploadTaskId:      uploadTask.ID,
		ProcessedAt:       time.Now().Unix(),
		LastSeenAt:        time.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建 processed 依赖失败: %v", err)
	}

	cleaned, err := CleanupCompletedStrmDependencies(100)
	if err != nil {
		t.Fatalf("执行 STRM 清理补偿失败: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("补偿清理数量 = %d，期望 1", cleaned)
	}
	assertPathMissing(t, filePath)
}

func TestCleanupCompletedStrmDependenciesDoesNotCountSkippedCleanup(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	syncPath, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	uploadTask := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	strmTask := &models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: uploadTask.ID,
		SyncPathId:   syncPath.ID,
		Status:       models.StrmGenerationStatusCompleted,
	}
	if err := db.Db.Create(strmTask).Error; err != nil {
		t.Fatalf("创建已完成 STRM 任务失败: %v", err)
	}
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        syncPath.ID,
		AccountId:         rule.AccountId,
		SourceKey:         "skipped-cleanup-source",
		LocalFullPath:     filePath,
		SourceFingerprint: uploadTask.SourceFingerprint,
		Result:            models.DirectoryUploadProcessedResultUploaded,
		UploadTaskId:      uploadTask.ID,
		ProcessedAt:       time.Now().Unix(),
		LastSeenAt:        time.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建 processed 依赖失败: %v", err)
	}

	cleaned, err := CleanupCompletedStrmDependencies(100)
	if err != nil {
		t.Fatalf("执行 STRM 清理补偿失败: %v", err)
	}
	if cleaned != 0 {
		t.Fatalf("跳过源文件清理时统计数量 = %d，期望 0", cleaned)
	}
	assertPathExists(t, filePath)
}

func TestFindCompletedStrmDependencyUploadTasksExcludesUnrelatedPendingCleanup(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	completedFilePath := filepath.Join(monitorPath, "completed.mkv")
	writeFileWithMtime(t, completedFilePath, []byte("completed"), time.Now())
	completedUpload := createCleanupUploadTask(t, rule, completedFilePath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, completedUpload.ID, models.StrmGenerationStatusCompleted)
	var completedStrm models.StrmGenerationTask
	if err := db.Db.Where("upload_task_id = ?", completedUpload.ID).First(&completedStrm).Error; err != nil {
		t.Fatalf("读取 completed STRM 任务失败: %v", err)
	}
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		SourceKey:         "completed-strm-candidate",
		LocalFullPath:     completedFilePath,
		SourceFingerprint: completedUpload.SourceFingerprint,
		Result:            models.DirectoryUploadProcessedResultUploaded,
		UploadTaskId:      completedUpload.ID,
		ProcessedAt:       time.Now().Unix(),
		LastSeenAt:        time.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建 completed STRM 依赖失败: %v", err)
	}

	unrelatedFilePath := filepath.Join(monitorPath, "unrelated.mkv")
	writeFileWithMtime(t, unrelatedFilePath, []byte("unrelated"), time.Now())
	unrelatedUpload := createCleanupUploadTask(t, rule, unrelatedFilePath, models.UploadResultMultipartUploaded)

	tasks, err := findCompletedStrmDependencyUploadTasks(0, 100)
	if err != nil {
		t.Fatalf("查询 STRM 清理候选失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != completedUpload.ID {
		t.Fatalf("STRM 清理候选 = %+v，期望仅包含 upload_task_id=%d，且不包含 %d", tasks, completedUpload.ID, unrelatedUpload.ID)
	}
}

func TestCleanupSourceAfterStrmSuccessKeepsSourceWhenDisabledOrStrmFailed(t *testing.T) {
	tests := []struct {
		name       string
		deleteFlag bool
		strmStatus models.StrmGenerationStatus
	}{
		{name: "删除开关关闭", deleteFlag: false, strmStatus: models.StrmGenerationStatusCompleted},
		{name: "STRM 未完成", deleteFlag: true, strmStatus: models.StrmGenerationStatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, "movie.mkv")
			writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			rule.DeleteSourceAfterSuccess = tt.deleteFlag
			if err := db.Db.Save(rule).Error; err != nil {
				t.Fatalf("保存目录上传规则失败: %v", err)
			}
			task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
			createCleanupStrmTask(t, task.ID, tt.strmStatus)

			if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
				t.Fatalf("源文件清理返回错误: %v", err)
			}
			assertPathExists(t, filePath)
		})
	}
}

func TestCleanupSourceAfterStrmSuccessUsesTaskCleanupIntent(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = false
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	task.SourceCleanupStatus = models.UploadSourceCleanupStatusNone
	if err := db.Db.Save(task).Error; err != nil {
		t.Fatalf("保存任务清理意图失败: %v", err)
	}
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("更新目录上传规则失败: %v", err)
	}

	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatalf("源文件清理返回错误: %v", err)
	}
	assertPathExists(t, filePath)

	var got models.DbUploadTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if got.SourceCleanupStatus != models.UploadSourceCleanupStatusNone || got.SourceDeletedAt != 0 {
		t.Fatalf("清理状态 = %+v，期望保持 none 且不删除", got)
	}
}

func TestCleanupSourceAfterStrmSuccessRequiresSafeUploadResult(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultSkippedAfterRapidWait)
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatalf("源文件清理返回错误: %v", err)
	}
	assertPathExists(t, filePath)
}

func TestCleanupSourceAfterStrmSuccessKeepsReplacedSourceFile(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	originalMtime := time.Unix(1000, 0)
	writeFileWithMtime(t, filePath, []byte("movie"), originalMtime)
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	createCleanupUploadSession(t, task.ID, filePath)
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)
	replacedMtime := originalMtime.Add(time.Hour)
	writeFileWithMtime(t, filePath, []byte("new movie content"), replacedMtime)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err == nil {
		t.Fatal("源文件已被替换时应返回错误")
	}
	assertPathExists(t, filePath)

	var got models.DbUploadTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if got.SourceCleanupStatus != models.UploadSourceCleanupStatusFailed || got.SourceCleanupError == "" {
		t.Fatalf("清理状态 = %+v，期望 failed 并记录错误", got)
	}
}

func TestCleanupSourceAfterStrmSuccessUsesSourceFingerprint(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	originalMtime := time.Unix(1000, 100)
	writeFileWithMtime(t, filePath, []byte("movie"), originalMtime)
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)
	replacedMtime := time.Unix(1000, 200)
	writeFileWithMtime(t, filePath, []byte("movie"), replacedMtime)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err == nil {
		t.Fatal("同秒同大小但 fingerprint 不同的源文件不应被删除")
	}
	assertPathExists(t, filePath)

	var got models.DbUploadTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if got.SourceCleanupStatus != models.UploadSourceCleanupStatusFailed || got.SourceCleanupError == "" {
		t.Fatalf("清理状态 = %+v，期望 failed 并记录 fingerprint 错误", got)
	}
}

func TestCleanupSourceAfterStrmSuccessRequiresSourceFingerprint(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	task.SourceFingerprint = ""
	if err := db.Db.Save(task).Error; err != nil {
		t.Fatalf("清空任务 fingerprint 失败: %v", err)
	}
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err == nil {
		t.Fatal("缺少 source_fingerprint 时应拒绝删除源文件")
	}
	assertPathExists(t, filePath)
}

func TestCleanupSourceAfterStrmSuccessKeepsSourceWhenUploadFailed(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultMultipartUploaded)
	task.Status = models.UploadStatusFailed
	if err := db.Db.Save(task).Error; err != nil {
		t.Fatalf("保存上传失败状态失败: %v", err)
	}
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatalf("源文件清理返回错误: %v", err)
	}
	assertPathExists(t, filePath)
}

func TestCleanupSourceAfterStrmSuccessStopsAtNonEmptyParent(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	nested := filepath.Join(monitorPath, "show", "Season 01")
	if err := ensureDir(nested); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}
	filePath := filepath.Join(nested, "episode.mkv")
	siblingPath := filepath.Join(monitorPath, "show", "poster.jpg")
	writeFileWithMtime(t, filePath, []byte("episode"), time.Now())
	writeFileWithMtime(t, siblingPath, []byte("poster"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, filePath, models.UploadResultRapidUpload)
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatalf("源文件清理失败: %v", err)
	}
	assertPathMissing(t, filePath)
	assertPathMissing(t, nested)
	assertPathExists(t, filepath.Join(monitorPath, "show"))
	assertPathExists(t, siblingPath)
}

func TestCleanupSourceAfterStrmSuccessRecordsPathBoundaryError(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "movie.mkv")
	writeFileWithMtime(t, outsidePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("保存目录上传规则失败: %v", err)
	}
	task := createCleanupUploadTask(t, rule, outsidePath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)

	if err := CleanupSourceAfterStrmSuccess(task.ID); err == nil {
		t.Fatal("源文件越界时应返回错误")
	}
	assertPathExists(t, outsidePath)

	var got models.DbUploadTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if got.SourceCleanupStatus != models.UploadSourceCleanupStatusFailed || got.SourceCleanupError == "" {
		t.Fatalf("清理状态 = %+v，期望 failed 并记录错误", got)
	}
}

func createCleanupUploadTask(t *testing.T, rule *models.DirectoryUploadRule, filePath string, result models.UploadResult) *models.DbUploadTask {
	t.Helper()
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取测试文件信息失败: %v", err)
	}
	task := &models.DbUploadTask{
		Source:              models.UploadSourceDirectoryMonitor,
		AccountId:           rule.AccountId,
		SyncPathId:          rule.SyncPathId,
		SourceType:          models.SourceType115,
		LocalFullPath:       filePath,
		RelativePath:        mustRel(t, rule.MonitorPath, filePath),
		RemoteFullPath:      "/remote/" + filepath.Base(filePath),
		RemotePathId:        rule.RemoteRootId,
		FileName:            filepath.Base(filePath),
		Status:              models.UploadStatusCompleted,
		FileSize:            info.Size(),
		LocalMtime:          info.ModTime().Unix(),
		LocalMtimeNs:        info.ModTime().UnixNano(),
		SourceFingerprint:   models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano()),
		UploadResult:        result,
		RemoteFileId:        "remote-file",
		RemotePickCode:      "pick-code",
		SourceCleanupStatus: models.UploadSourceCleanupStatusPending,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	return task
}

func createCleanupUploadSession(t *testing.T, uploadTaskID uint, filePath string) {
	t.Helper()
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取测试文件信息失败: %v", err)
	}
	if err := db.Db.Create(&models.UploadSession{
		UploadTaskId:  uploadTaskID,
		LocalFullPath: filePath,
		FileSize:      info.Size(),
		LocalMtime:    info.ModTime().Unix(),
		Status:        models.UploadSessionStatusCompleted,
	}).Error; err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
}

func createCleanupStrmTask(t *testing.T, uploadTaskID uint, status models.StrmGenerationStatus) {
	t.Helper()
	if err := db.Db.Create(&models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: uploadTaskID,
		SyncPathId:   1,
		Status:       status,
	}).Error; err != nil {
		t.Fatalf("创建 STRM 任务失败: %v", err)
	}
}
