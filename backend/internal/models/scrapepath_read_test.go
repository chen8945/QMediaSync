package models

import (
	"context"
	"slices"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestScrapePathReadsKeepOtherPathsAfterDecodeFailure(t *testing.T) {
	for _, field := range []string{"video_ext", "deleted_keyword"} {
		t.Run(field, func(t *testing.T) {
			conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			oldDB, oldLogger := db.Db, helpers.AppLogger
			db.Db, helpers.AppLogger = conn, nil
			t.Cleanup(func() { db.Db, helpers.AppLogger = oldDB, oldLogger })
			if err := conn.AutoMigrate(&ScrapePath{}); err != nil {
				t.Fatal(err)
			}
			paths := []*ScrapePath{
				{BaseModel: BaseModel{ID: 1}, VideoExt: `[".mkv"]`, DeletedKeyword: `["sample"]`},
				{BaseModel: BaseModel{ID: 2}, VideoExt: `[".mp4"]`, DeletedKeyword: `["preview"]`},
			}
			if err := conn.Create(paths).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.Model(&ScrapePath{}).Where("id = ?", 2).UpdateColumn(field, "{invalid").Error; err != nil {
				t.Fatal(err)
			}
			strict, err := ReadScrapePaths(context.Background(), "")
			if err == nil {
				t.Fatal("存储解码失败必须返回错误")
			}
			for _, got := range [][]*ScrapePath{strict, GetScrapePathes("")} {
				if len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
					t.Fatalf("一个异常目录不应丢弃其他目录：%+v", got)
				}
				if !slices.Equal(got[1].VideoExtList, []string{".mkv"}) || !slices.Equal(got[1].DeleteKeyword, []string{"sample"}) {
					t.Fatalf("有效目录仍须完成解码：%+v", got[1])
				}
			}
		})
	}
}
