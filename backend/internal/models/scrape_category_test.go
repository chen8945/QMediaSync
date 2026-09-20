package models

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// 分类表不存在时写入必然失败，Save 必须把数据库错误返回给调用方。
func TestCategorySaveReturnsDBError(t *testing.T) {
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败：%v", err)
	}
	oldDB := db.Db
	db.Db = testDB
	t.Cleanup(func() { db.Db = oldDB })

	if err := (&MovieCategory{}).Save("电影", []int{18}, []string{"zh"}); err == nil {
		t.Error("电影分类写入失败时 Save 应返回错误")
	}
	if err := (&TvShowCategory{}).Save("剧集", []int{18}, []string{"CN"}); err == nil {
		t.Error("剧集分类写入失败时 Save 应返回错误")
	}
}
