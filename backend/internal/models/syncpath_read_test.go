package models

import (
	"context"
	"errors"
	"testing"

	"qmediasync/internal/db"

	"gorm.io/gorm"
)

func TestSyncPathReadsKeepPathsAfterAccountLookupFailure(t *testing.T) {
	setupOpenListAccountTest(t)
	if err := db.Db.AutoMigrate(&SyncPath{}); err != nil {
		t.Fatal(err)
	}
	accounts := []*Account{
		{BaseModel: BaseModel{ID: 1}, Name: "valid-account"},
		{BaseModel: BaseModel{ID: 2}, Name: "unavailable-account"},
	}
	if err := db.Db.Create(accounts).Error; err != nil {
		t.Fatal(err)
	}
	paths := []*SyncPath{
		{BaseModel: BaseModel{ID: 1}, BaseCid: "valid-path", AccountId: 1, EnableCron: true},
		{BaseModel: BaseModel{ID: 2}, BaseCid: "unavailable-path", AccountId: 2, EnableCron: true},
	}
	if err := db.Db.Create(paths).Error; err != nil {
		t.Fatal(err)
	}
	lookupErr := errors.New("fixture account lookup failure")
	if err := db.Db.Callback().Query().After("gorm:query").Register("fixture_account_read_failure", func(tx *gorm.DB) {
		if account, ok := tx.Statement.Dest.(*Account); ok && account.ID == 2 {
			tx.AddError(lookupErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	strict, total, err := GetSyncPathListWithContext(context.Background(), 1, 50, true, "")
	if !errors.Is(err, lookupErr) || total != 2 || len(strict) != 2 {
		t.Fatalf("查询失败应保留目录与原始原因：paths=%+v total=%d err=%v", strict, total, err)
	}
	legacy, legacyTotal := GetSyncPathList(1, 50, true, "")
	for _, got := range [][]*SyncPath{strict, legacy} {
		if len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 || got[1].AccountName != "valid-account" {
			t.Fatalf("一账号失败不应阻断其他同步目录读取：%+v", got)
		}
	}
	if legacyTotal != 2 {
		t.Fatalf("后台查询丢失总数：%d", legacyTotal)
	}
}
