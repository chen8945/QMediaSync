package models

import (
	"io"
	"log"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupDirectoryUploadRuleTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&SyncPath{}, &SyncFile{}, &EmbyLibrarySyncPath{}, &EmbyMediaSyncFile{}, &DirectoryUploadRule{}, &DirectoryUploadProcessedFile{}, &DbUploadTask{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
}

func TestSaveDirectoryUploadRulesCancelsPendingCleanupBeforeDeletingRule(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)
	syncPath := &SyncPath{AccountId: 3, SourceType: SourceType115, LocalPath: t.TempDir(), RemotePath: "/remote", BaseCid: "root", DirectoryUploadEnabled: true}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}
	rule := &DirectoryUploadRule{
		SyncPathId:               syncPath.ID,
		AccountId:                syncPath.AccountId,
		Enabled:                  true,
		MonitorPath:              filepath.Join(t.TempDir(), "watch"),
		RemoteRootPath:           "/remote/uploads",
		RemoteRootId:             "uploads",
		Recursive:                true,
		DeleteSourceAfterSuccess: true,
	}
	if err := db.Db.Create(rule).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}
	uploadTask := &DbUploadTask{Source: UploadSourceDirectoryMonitor, Status: UploadStatusCompleted, SourceCleanupStatus: UploadSourceCleanupStatusPending}
	if err := db.Db.Create(uploadTask).Error; err != nil {
		t.Fatalf("创建待清理上传任务失败: %v", err)
	}
	if err := db.Db.Create(&DirectoryUploadProcessedFile{RuleId: rule.ID, SyncPathId: syncPath.ID, SourceKey: "pending-cleanup", UploadTaskId: uploadTask.ID}).Error; err != nil {
		t.Fatalf("创建 processed 记录失败: %v", err)
	}

	if _, err := SaveDirectoryUploadRulesForSyncPath(syncPath.ID, false, []*DirectoryUploadRule{}); err != nil {
		t.Fatalf("删除规则失败: %v", err)
	}
	var updated DbUploadTask
	if err := db.Db.First(&updated, uploadTask.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if updated.SourceCleanupStatus != UploadSourceCleanupStatusNone || updated.SourceCleanupError == "" {
		t.Fatalf("取消后 cleanup=%s error=%q，期望 none 并记录原因", updated.SourceCleanupStatus, updated.SourceCleanupError)
	}
}

func TestSaveDirectoryUploadRulesCancelsPendingCleanupWhenRemoteBoundaryChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(rule *DirectoryUploadRule)
	}{
		{
			name: "修改远端根目录路径",
			mutate: func(rule *DirectoryUploadRule) {
				rule.RemoteRootPath = "/remote/other"
			},
		},
		{
			name: "修改远端根目录 ID",
			mutate: func(rule *DirectoryUploadRule) {
				rule.RemoteRootId = "other-root"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadRuleTestDB(t)
			syncPath := &SyncPath{AccountId: 3, SourceType: SourceType115, LocalPath: t.TempDir(), RemotePath: "/remote", BaseCid: "root", DirectoryUploadEnabled: true}
			if err := db.Db.Create(syncPath).Error; err != nil {
				t.Fatalf("创建同步目录失败: %v", err)
			}
			rule := &DirectoryUploadRule{
				SyncPathId:               syncPath.ID,
				AccountId:                syncPath.AccountId,
				Enabled:                  true,
				MonitorPath:              filepath.Join(t.TempDir(), "watch"),
				RemoteRootPath:           "/remote/uploads",
				RemoteRootId:             "uploads",
				Recursive:                true,
				DeleteSourceAfterSuccess: true,
			}
			if err := db.Db.Create(rule).Error; err != nil {
				t.Fatalf("创建目录上传规则失败: %v", err)
			}
			uploadTask := &DbUploadTask{Source: UploadSourceDirectoryMonitor, Status: UploadStatusCompleted, SourceCleanupStatus: UploadSourceCleanupStatusPending}
			if err := db.Db.Create(uploadTask).Error; err != nil {
				t.Fatalf("创建待清理上传任务失败: %v", err)
			}
			if err := db.Db.Create(&DirectoryUploadProcessedFile{RuleId: rule.ID, SyncPathId: syncPath.ID, SourceKey: "pending-cleanup", UploadTaskId: uploadTask.ID}).Error; err != nil {
				t.Fatalf("创建 processed 记录失败: %v", err)
			}

			updatedRule := *rule
			tt.mutate(&updatedRule)
			if _, err := SaveDirectoryUploadRulesForSyncPath(syncPath.ID, true, []*DirectoryUploadRule{&updatedRule}); err != nil {
				t.Fatalf("更新规则失败: %v", err)
			}
			var updatedTask DbUploadTask
			if err := db.Db.First(&updatedTask, uploadTask.ID).Error; err != nil {
				t.Fatalf("读取上传任务失败: %v", err)
			}
			if updatedTask.SourceCleanupStatus != UploadSourceCleanupStatusNone || updatedTask.SourceCleanupError == "" {
				t.Fatalf("清理边界变化后 cleanup=%s error=%q，期望 none 并记录原因", updatedTask.SourceCleanupStatus, updatedTask.SourceCleanupError)
			}
		})
	}
}

func TestDirectoryUploadRuleSaveAndDefaults(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)

	syncPath := &SyncPath{
		ID:                     10,
		AccountId:              3,
		SourceType:             SourceType115,
		LocalPath:              "/strm",
		RemotePath:             "/remote",
		BaseCid:                "remote-root",
		DirectoryUploadEnabled: true,
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}

	rule := &DirectoryUploadRule{
		SyncPathId:     syncPath.ID,
		AccountId:      syncPath.AccountId,
		Enabled:        true,
		MonitorPath:    "/watch",
		RemoteRootPath: "/remote/uploads",
		RemoteRootId:   "upload-root",
	}
	if err := SaveDirectoryUploadRule(rule); err != nil {
		t.Fatalf("保存目录监控上传规则失败: %v", err)
	}

	enabled, err := GetEnabledDirectoryUploadRules()
	if err != nil {
		t.Fatalf("查询启用规则失败: %v", err)
	}
	if len(enabled) != 1 {
		t.Fatalf("启用规则数量 = %d，期望 1", len(enabled))
	}
	got := enabled[0]
	if got.WatchMode != DirectoryUploadWatchModeAuto {
		t.Fatalf("watch_mode = %s，期望 auto", got.WatchMode)
	}
	if got.StabilitySeconds != 15 || got.StabilityCheckIntervalSeconds != 2 || got.StabilityRequiredCount != 3 {
		t.Fatalf("稳定性默认值 = %+v，期望 15/2/3", got)
	}
	if got.RescanIntervalSeconds != 30 || !got.StartupScanEnabled || got.ProcessedCacheTTLSeconds != 600 {
		t.Fatalf("补偿扫描默认值 = %+v，期望 rescan=30 startup=true ttl=600", got)
	}
	if got.DeleteSourceAfterSuccess {
		t.Fatal("delete_source_after_success 默认应为 false")
	}
	if got.OverwriteMode != DirectoryUploadOverwriteSkipSame {
		t.Fatalf("overwrite_mode = %s，期望 skip_same", got.OverwriteMode)
	}
	if !got.Recursive {
		t.Fatal("recursive 默认应为 true")
	}

	got.DeleteSourceAfterSuccess = true
	if err := SaveDirectoryUploadRule(got); err != nil {
		t.Fatalf("开启删除源文件开关失败: %v", err)
	}
	reloaded, err := GetDirectoryUploadRuleById(got.ID)
	if err != nil {
		t.Fatalf("重新读取规则失败: %v", err)
	}
	if !reloaded.DeleteSourceAfterSuccess {
		t.Fatal("delete_source_after_success 应可保存为 true")
	}
}

func TestSaveDirectoryUploadRulesForSyncPathReplacesFinalSetAndPreservesRuleEnabled(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)

	syncPath := &SyncPath{
		AccountId:              3,
		SourceType:             SourceType115,
		LocalPath:              filepath.Join(t.TempDir(), "strm"),
		RemotePath:             "/remote",
		BaseCid:                "remote-root",
		DirectoryUploadEnabled: true,
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}

	kept := &DirectoryUploadRule{
		SyncPathId:     syncPath.ID,
		AccountId:      syncPath.AccountId,
		Enabled:        true,
		MonitorPath:    filepath.Join(t.TempDir(), "keep"),
		RemoteRootPath: "/remote/keep",
		RemoteRootId:   "remote-keep",
		Recursive:      true,
	}
	removed := &DirectoryUploadRule{
		SyncPathId:     syncPath.ID,
		AccountId:      syncPath.AccountId,
		Enabled:        true,
		MonitorPath:    filepath.Join(t.TempDir(), "remove"),
		RemoteRootPath: "/remote/remove",
		RemoteRootId:   "remote-remove",
		Recursive:      true,
	}
	rules := []*DirectoryUploadRule{kept, removed}
	if err := db.Db.Create(&rules).Error; err != nil {
		t.Fatalf("创建目录监控规则失败: %v", err)
	}
	if err := db.Db.Create(&DirectoryUploadProcessedFile{
		RuleId:       removed.ID,
		SyncPathId:   syncPath.ID,
		AccountId:    syncPath.AccountId,
		ScopeHash:    "scope",
		SourceKey:    "source",
		RelativePath: "movie.mkv",
		Result:       DirectoryUploadProcessedResultUploaded,
	}).Error; err != nil {
		t.Fatalf("创建目录监控处理记录失败: %v", err)
	}

	kept.MonitorPath = filepath.Join(t.TempDir(), "updated")
	created := &DirectoryUploadRule{
		SyncPathId:     syncPath.ID,
		AccountId:      syncPath.AccountId,
		Enabled:        false,
		MonitorPath:    filepath.Join(t.TempDir(), "created"),
		RemoteRootPath: "/remote/created",
		RemoteRootId:   "remote-created",
		Recursive:      true,
	}
	saved, err := SaveDirectoryUploadRulesForSyncPath(syncPath.ID, false, []*DirectoryUploadRule{kept, created})
	if err != nil {
		t.Fatalf("最终集合保存目录监控规则失败: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("保存后规则数量 = %d，期望 2", len(saved))
	}

	var reloaded SyncPath
	if err := db.Db.First(&reloaded, syncPath.ID).Error; err != nil {
		t.Fatalf("读取同步目录失败: %v", err)
	}
	if reloaded.DirectoryUploadEnabled {
		t.Fatal("目录监控总开关应被关闭")
	}
	var keptReloaded DirectoryUploadRule
	if err := db.Db.First(&keptReloaded, kept.ID).Error; err != nil {
		t.Fatalf("读取保留规则失败: %v", err)
	}
	if !keptReloaded.Enabled {
		t.Fatal("关闭总开关不应修改规则自身 enabled")
	}
	if keptReloaded.MonitorPath != kept.MonitorPath {
		t.Fatalf("保留规则监控目录 = %s，期望 %s", keptReloaded.MonitorPath, kept.MonitorPath)
	}
	var removedTotal int64
	if err := db.Db.Model(&DirectoryUploadRule{}).Where("id = ?", removed.ID).Count(&removedTotal).Error; err != nil {
		t.Fatalf("统计被删除规则失败: %v", err)
	}
	if removedTotal != 0 {
		t.Fatalf("被移出最终集合的规则数量 = %d，期望 0", removedTotal)
	}
	var processedTotal int64
	if err := db.Db.Model(&DirectoryUploadProcessedFile{}).Where("rule_id = ?", removed.ID).Count(&processedTotal).Error; err != nil {
		t.Fatalf("统计被删除规则处理记录失败: %v", err)
	}
	if processedTotal != 0 {
		t.Fatalf("被删除规则处理记录数量 = %d，期望 0", processedTotal)
	}
}

func TestGetEnabledDirectoryUploadRulesRequiresSyncPathMasterEnabled(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)

	disabledSyncPath := &SyncPath{
		AccountId:              3,
		SourceType:             SourceType115,
		LocalPath:              filepath.Join(t.TempDir(), "disabled-strm"),
		RemotePath:             "/disabled",
		BaseCid:                "disabled-root",
		DirectoryUploadEnabled: false,
	}
	enabledSyncPath := &SyncPath{
		AccountId:              3,
		SourceType:             SourceType115,
		LocalPath:              filepath.Join(t.TempDir(), "enabled-strm"),
		RemotePath:             "/enabled",
		BaseCid:                "enabled-root",
		DirectoryUploadEnabled: true,
	}
	if err := db.Db.Create(&[]*SyncPath{disabledSyncPath, enabledSyncPath}).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}
	rules := []*DirectoryUploadRule{
		{
			SyncPathId:     disabledSyncPath.ID,
			AccountId:      3,
			Enabled:        true,
			MonitorPath:    filepath.Join(t.TempDir(), "disabled-rule"),
			RemoteRootPath: "/disabled/uploads",
			RemoteRootId:   "disabled-upload",
			Recursive:      true,
		},
		{
			SyncPathId:     enabledSyncPath.ID,
			AccountId:      3,
			Enabled:        true,
			MonitorPath:    filepath.Join(t.TempDir(), "enabled-rule"),
			RemoteRootPath: "/enabled/uploads",
			RemoteRootId:   "enabled-upload",
			Recursive:      true,
		},
	}
	if err := db.Db.Create(&rules).Error; err != nil {
		t.Fatalf("创建目录监控规则失败: %v", err)
	}

	enabledRules, err := GetEnabledDirectoryUploadRules()
	if err != nil {
		t.Fatalf("查询启用目录监控规则失败: %v", err)
	}
	if len(enabledRules) != 1 || enabledRules[0].SyncPathId != enabledSyncPath.ID {
		t.Fatalf("启用规则 = %+v，期望只返回总开关开启的同步目录规则", enabledRules)
	}
}

func TestSaveDirectoryUploadRuleRejectsDuplicateScope(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)

	syncPath := &SyncPath{
		AccountId:  3,
		SourceType: SourceType115,
		LocalPath:  "/strm",
		RemotePath: "/remote",
		BaseCid:    "remote-root",
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}

	rule := &DirectoryUploadRule{
		SyncPathId:     syncPath.ID,
		AccountId:      syncPath.AccountId,
		Enabled:        true,
		MonitorPath:    "/watch",
		RemoteRootPath: "/remote/uploads",
		RemoteRootId:   "upload-root",
	}
	if err := SaveDirectoryUploadRule(rule); err != nil {
		t.Fatalf("保存第一条目录监控上传规则失败: %v", err)
	}

	duplicate := &DirectoryUploadRule{
		SyncPathId:     syncPath.ID,
		AccountId:      syncPath.AccountId,
		Enabled:        false,
		MonitorPath:    "/watch",
		RemoteRootPath: "/remote/uploads",
		RemoteRootId:   "upload-root",
	}
	if err := SaveDirectoryUploadRule(duplicate); err == nil {
		t.Fatal("完全相同的目录监控上传规则应被拒绝")
	}
}

func TestDeleteSyncPathByIdDeletesDirectoryUploadRules(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)

	deletedSyncPath := &SyncPath{
		AccountId:  3,
		SourceType: SourceType115,
		LocalPath:  "/strm/deleted",
		RemotePath: "/remote/deleted",
		BaseCid:    "remote-deleted",
	}
	if err := db.Db.Create(deletedSyncPath).Error; err != nil {
		t.Fatalf("创建待删除同步目录失败: %v", err)
	}
	keptSyncPath := &SyncPath{
		AccountId:  3,
		SourceType: SourceType115,
		LocalPath:  "/strm/kept",
		RemotePath: "/remote/kept",
		BaseCid:    "remote-kept",
	}
	if err := db.Db.Create(keptSyncPath).Error; err != nil {
		t.Fatalf("创建保留同步目录失败: %v", err)
	}

	rules := []*DirectoryUploadRule{
		{
			SyncPathId:     deletedSyncPath.ID,
			AccountId:      3,
			Enabled:        true,
			MonitorPath:    "/watch/deleted",
			RemoteRootPath: "/remote/deleted/uploads",
			RemoteRootId:   "upload-deleted",
		},
		{
			SyncPathId:     keptSyncPath.ID,
			AccountId:      3,
			Enabled:        true,
			MonitorPath:    "/watch/kept",
			RemoteRootPath: "/remote/kept/uploads",
			RemoteRootId:   "upload-kept",
		},
	}
	if err := db.Db.Create(&rules).Error; err != nil {
		t.Fatalf("创建目录监控规则失败: %v", err)
	}

	if ok := DeleteSyncPathById(deletedSyncPath.ID); !ok {
		t.Fatal("删除同步目录应成功")
	}

	var deletedRuleCount int64
	if err := db.Db.Model(&DirectoryUploadRule{}).Where("sync_path_id = ?", deletedSyncPath.ID).Count(&deletedRuleCount).Error; err != nil {
		t.Fatalf("统计被删除同步目录的规则失败: %v", err)
	}
	if deletedRuleCount != 0 {
		t.Fatalf("被删除同步目录的目录监控规则数量 = %d，期望 0", deletedRuleCount)
	}
	var keptRuleCount int64
	if err := db.Db.Model(&DirectoryUploadRule{}).Where("sync_path_id = ?", keptSyncPath.ID).Count(&keptRuleCount).Error; err != nil {
		t.Fatalf("统计保留同步目录的规则失败: %v", err)
	}
	if keptRuleCount != 1 {
		t.Fatalf("保留同步目录的目录监控规则数量 = %d，期望 1", keptRuleCount)
	}
}

func TestDeleteSyncPathByIdDeletesDirectoryUploadProcessedFiles(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)

	deletedSyncPath := &SyncPath{
		AccountId:  3,
		SourceType: SourceType115,
		LocalPath:  "/strm/deleted",
		RemotePath: "/remote/deleted",
		BaseCid:    "remote-deleted",
	}
	if err := db.Db.Create(deletedSyncPath).Error; err != nil {
		t.Fatalf("创建待删除同步目录失败: %v", err)
	}
	keptSyncPath := &SyncPath{
		AccountId:  3,
		SourceType: SourceType115,
		LocalPath:  "/strm/kept",
		RemotePath: "/remote/kept",
		BaseCid:    "remote-kept",
	}
	if err := db.Db.Create(keptSyncPath).Error; err != nil {
		t.Fatalf("创建保留同步目录失败: %v", err)
	}
	records := []*DirectoryUploadProcessedFile{
		{SyncPathId: deletedSyncPath.ID, SourceKey: "deleted-sync-path-1", SourceFingerprint: "v1:1:1", Result: DirectoryUploadProcessedResultUploaded},
		{SyncPathId: deletedSyncPath.ID, SourceKey: "deleted-sync-path-2", SourceFingerprint: "v1:2:2", Result: DirectoryUploadProcessedResultQueued},
		{SyncPathId: keptSyncPath.ID, SourceKey: "kept-sync-path-1", SourceFingerprint: "v1:3:3", Result: DirectoryUploadProcessedResultRemoteExists},
	}
	if err := db.Db.Create(&records).Error; err != nil {
		t.Fatalf("创建 processed 记录失败: %v", err)
	}

	if ok := DeleteSyncPathById(deletedSyncPath.ID); !ok {
		t.Fatal("删除同步目录应成功")
	}

	var deletedProcessedCount int64
	if err := db.Db.Model(&DirectoryUploadProcessedFile{}).Where("sync_path_id = ?", deletedSyncPath.ID).Count(&deletedProcessedCount).Error; err != nil {
		t.Fatalf("统计被删除同步目录的 processed 记录失败: %v", err)
	}
	if deletedProcessedCount != 0 {
		t.Fatalf("被删除同步目录的 processed 记录数量 = %d，期望 0", deletedProcessedCount)
	}
	var keptProcessedCount int64
	if err := db.Db.Model(&DirectoryUploadProcessedFile{}).Where("sync_path_id = ?", keptSyncPath.ID).Count(&keptProcessedCount).Error; err != nil {
		t.Fatalf("统计保留同步目录的 processed 记录失败: %v", err)
	}
	if keptProcessedCount != 1 {
		t.Fatalf("保留同步目录的 processed 记录数量 = %d，期望 1", keptProcessedCount)
	}
}

func TestDirectoryUploadRuleValidateWithSyncPath(t *testing.T) {
	tests := []struct {
		name     string
		rule     DirectoryUploadRule
		syncPath SyncPath
		wantErr  bool
	}{
		{
			name: "合法规则通过校验",
			rule: DirectoryUploadRule{
				MonitorPath:    "/watch",
				RemoteRootPath: "/remote/uploads",
				RemoteRootId:   "uploads",
			},
			syncPath: SyncPath{
				LocalPath:  "/strm",
				RemotePath: "/remote",
			},
		},
		{
			name: "监控目录等于 STRM 本地目录时拒绝",
			rule: DirectoryUploadRule{
				MonitorPath:    "/strm",
				RemoteRootPath: "/remote/uploads",
			},
			syncPath: SyncPath{
				LocalPath:  "/strm",
				RemotePath: "/remote",
			},
			wantErr: true,
		},
		{
			name: "远端上传根目录不在同步远端目录下时拒绝",
			rule: DirectoryUploadRule{
				MonitorPath:    "/watch",
				RemoteRootPath: "/other/uploads",
			},
			syncPath: SyncPath{
				LocalPath:  "/strm",
				RemotePath: "/remote",
			},
			wantErr: true,
		},
		{
			name: "远端上传根目录 ID 为空时拒绝",
			rule: DirectoryUploadRule{
				MonitorPath:    "/watch",
				RemoteRootPath: "/remote/uploads",
			},
			syncPath: SyncPath{
				LocalPath:  "/strm",
				RemotePath: "/remote",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.ValidateWithSyncPath(&tt.syncPath)
			if tt.wantErr && err == nil {
				t.Fatal("期望校验失败，实际返回 nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("期望校验通过，实际错误: %v", err)
			}
		})
	}
}
