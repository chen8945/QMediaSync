package syncconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
)

func TestSaveSyncPathNotifiesPreloadOriginsAfterCommit(t *testing.T) {
	database := setupSyncConfigServiceTest(t)
	command := SaveSyncPathCommand{
		IdempotencyKey: "preload-origin-create",
		SyncPath: SyncPathInput{
			SourceType: models.SourceTypeLocal, BaseCid: "local-preload-origin",
			LocalPath: t.TempDir(), RemotePath: t.TempDir(), CustomConfig: true,
			Setting: models.SettingStrm{StrmBaseUrl: "http://old.invalid:12333"},
		},
	}
	calls := 0
	models.SetStrmOriginsChangeHook(func() {
		calls++
		var saved models.SyncPath
		if err := database.Where("base_cid = ?", command.SyncPath.BaseCid).First(&saved).Error; err != nil || saved.StrmBaseUrl != command.SyncPath.Setting.StrmBaseUrl {
			t.Errorf("来源通知早于事务提交：%+v %v", saved, err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release, err := syncscope.Acquire(ctx, saved.Scope())
		if err != nil {
			t.Errorf("来源通知未释放目录范围：%v", err)
			return
		}
		release()
	})
	t.Cleanup(func() { models.SetStrmOriginsChangeHook(nil) })
	service := NewService(ServiceOptions{DB: database})
	created, err := service.Save(t.Context(), command)
	if err != nil || calls != 1 {
		t.Fatalf("新建来源应通知一次：calls=%d err=%v", calls, err)
	}
	if _, err := service.Save(t.Context(), command); err != nil || calls != 1 {
		t.Fatalf("幂等重放不应再次通知：calls=%d err=%v", calls, err)
	}
	command.ID = created.SyncPath.ID
	command.SyncPath.EnableCron = true
	if _, err := service.Save(t.Context(), command); err != nil || calls != 1 {
		t.Fatalf("未改变来源不应通知：calls=%d err=%v", calls, err)
	}
	t.Run("normalized-paths", func(t *testing.T) {
		command.SyncPath.LocalPath, command.SyncPath.RemotePath = created.SyncPath.LocalPath, created.SyncPath.RemotePath
		if _, err := service.Save(t.Context(), command); err != nil || calls != 1 {
			t.Fatalf("规范化路径未变不应通知：calls=%d err=%v", calls, err)
		}
	})
	command.SyncPath.Setting.StrmBaseUrl = "https://new.invalid/qms"
	if _, err := service.Save(t.Context(), command); err != nil || calls != 2 {
		t.Fatalf("来源改变未通知：calls=%d err=%v", calls, err)
	}
	for index, field := range []string{"local_path", "remote_path"} {
		nextRoot := t.TempDir()
		t.Run(field, func(t *testing.T) {
			if field == "local_path" {
				command.SyncPath.LocalPath = nextRoot
			} else {
				command.SyncPath.RemotePath = nextRoot
			}
			if _, err := service.Save(t.Context(), command); err != nil || calls != index+3 {
				t.Fatalf("根路径改变未通知：field=%s calls=%d err=%v", field, calls, err)
			}
		})
	}
	failed := NewService(ServiceOptions{DB: database, RunTransaction: func(ctx context.Context, handle *gorm.DB, run func(*gorm.DB) error) error {
		return handle.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := run(tx); err != nil {
				return err
			}
			return errors.New("test commit failure")
		})
	}})
	command.SyncPath.Setting.StrmBaseUrl = "https://failed.invalid"
	if _, err := failed.Save(t.Context(), command); err == nil || calls != 4 {
		t.Fatalf("回滚不应通知：calls=%d err=%v", calls, err)
	}
}
