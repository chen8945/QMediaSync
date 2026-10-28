package models

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/syncscope"
)

func TestDeleteSyncPathNotifiesPreloadOriginsAfterCommit(t *testing.T) {
	path := setupSyncScopeTest(t)
	calls := 0
	SetStrmOriginsChangeHook(func() {
		calls++
		var count int64
		if err := db.Db.Model(&SyncPath{}).Where("id = ?", path.ID).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("来源通知早于删除提交：count=%d err=%v", count, err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release, err := syncscope.Acquire(ctx, path.Scope())
		if err != nil {
			t.Errorf("来源通知仍持有目录范围：%v", err)
			return
		}
		release()
	})
	t.Cleanup(func() { SetStrmOriginsChangeHook(nil) })
	if err := db.Db.Callback().Delete().Before("gorm:delete").Register("test:preload_failed_delete", func(tx *gorm.DB) { tx.AddError(errors.New("test failed delete")) }); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSyncPathByID(t.Context(), path.ID); err == nil || calls != 0 {
		t.Fatalf("失败删除不应通知：calls=%d err=%v", calls, err)
	}
	if err := db.Db.Callback().Delete().Remove("test:preload_failed_delete"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSyncPathByID(t.Context(), path.ID); err != nil || calls != 1 {
		t.Fatalf("删除提交后应通知一次：calls=%d err=%v", calls, err)
	}
}
