package syncstrm

import (
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestHandleTempTableDiffDeletesExactIDs(t *testing.T) {
	for _, count := range []int{0, 1, 255, 256, 257, 999, 1000, 1001, 2000, 2001, 2501} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			setupSyncStateTestDB(t)
			rows := make([]models.SyncFile, count+1)
			for i := range rows {
				rows[i] = models.SyncFile{SyncPathId: 1, FileId: fmt.Sprint(i)}
			}
			rows[count].SyncPathId = 2
			if err := db.Db.CreateInBatches(rows, 100).Error; err != nil {
				t.Fatal(err)
			}
			s := &SyncStrm{Context: t.Context(), Account: &models.Account{}, SyncPathId: 1, Sync: &models.Sync{Logger: helpers.AppLogger}, memSyncCache: NewMemorySyncCache(1)}
			if err := s.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			var got []uint
			if err := db.Db.Model(&models.SyncFile{}).Order("id").Pluck("id", &got).Error; err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []uint{rows[count].ID}) {
				t.Fatalf("剩余 ID = %v，期望仅保留其他目录的 %d", got, rows[count].ID)
			}
		})
	}
}

func TestHandleTempTableDiffPreservesPartialDeleteOnFailure(t *testing.T) {
	setupSyncStateTestDB(t)
	rows := make([]models.SyncFile, 2501)
	for i := range rows {
		rows[i] = models.SyncFile{SyncPathId: 1, FileId: fmt.Sprint(i)}
	}
	if err := db.Db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("second delete failed")
	calls := 0
	if err := db.Db.Callback().Delete().Before("gorm:delete").Register("fail_second_delete", func(tx *gorm.DB) {
		calls++
		if calls == 2 {
			tx.AddError(wantErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	s := &SyncStrm{Context: t.Context(), Account: &models.Account{}, SyncPathId: 1, Sync: &models.Sync{Logger: helpers.AppLogger}, memSyncCache: NewMemorySyncCache(1)}
	if err := s.handleTempTableDiff(); !errors.Is(err, wantErr) {
		t.Fatalf("错误 = %v，期望 %v", err, wantErr)
	}
	var got []uint
	if err := db.Db.Model(&models.SyncFile{}).Order("id").Pluck("id", &got).Error; err != nil {
		t.Fatal(err)
	}
	want := make([]uint, 0, len(rows)-256)
	for _, row := range rows[256:] {
		want = append(want, row.ID)
	}
	if calls != 2 || !slices.Equal(got, want) {
		t.Fatalf("失败后已提交/未开始批次不符：calls=%d，剩余 ID=%v", calls, got)
	}
	if err := db.Db.Callback().Delete().Remove("fail_second_delete"); err != nil {
		t.Fatal(err)
	}
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := db.Db.Model(&models.SyncFile{}).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("重跑未收敛：remaining=%d err=%v", remaining, err)
	}
}

func TestHandleTempTableDiffRefreshesOpenListRemoteIdentity(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := db.Db.AutoMigrate(&models.SyncFile{}); err != nil {
		t.Fatalf("迁移同步文件表失败: %v", err)
	}

	existing := &models.SyncFile{
		SourceType:       models.SourceTypeOpenList,
		SyncPathId:       1,
		FileId:           "/remote/movie.mkv",
		ParentId:         "/remote",
		FileName:         "movie.mkv",
		OpenlistObjectId: "old-object-id",
		OpenlistSHA1:     "old-sha1",
		OpenlistMD5:      "old-md5",
	}
	if err := db.Db.Create(existing).Error; err != nil {
		t.Fatalf("创建已有同步文件失败: %v", err)
	}

	cache := NewMemorySyncCache(1)
	if err := cache.Insert(&SyncFileCache{
		ParentId:         "/remote",
		FileName:         "movie.mkv",
		SourceType:       models.SourceTypeOpenList,
		OpenlistObjectId: "new-object-id",
		OpenlistSHA1:     "new-sha1",
		OpenlistMD5:      "new-md5",
	}); err != nil {
		t.Fatalf("写入同步缓存失败: %v", err)
	}

	syncer := &SyncStrm{
		Context:      t.Context(),
		Account:      &models.Account{},
		Sync:         &models.Sync{Logger: helpers.AppLogger},
		SyncPathId:   1,
		memSyncCache: cache,
	}
	if err := syncer.handleTempTableDiff(); err != nil {
		t.Fatalf("同步缓存差异处理失败: %v", err)
	}

	var got models.SyncFile
	if err := db.Db.First(&got, existing.ID).Error; err != nil {
		t.Fatalf("读取同步文件失败: %v", err)
	}
	if got.OpenlistObjectId != "new-object-id" || got.OpenlistSHA1 != "new-sha1" || got.OpenlistMD5 != "new-md5" {
		t.Fatalf("OpenList 远端身份未刷新: %+v", got)
	}
}

func TestHandleTempTableDiffKeepsBaiduPathShapedSyncFile(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := db.Db.AutoMigrate(&models.SyncFile{}); err != nil {
		t.Fatalf("迁移同步文件表失败: %v", err)
	}

	existing := &models.SyncFile{
		SourceType: models.SourceTypeBaiduPan,
		SyncPathId: 1,
		FileId:     "/remote/movie.mkv",
		ParentId:   "/remote",
		Path:       "/remote",
		FileName:   "movie.mkv",
		PickCode:   "baidu-fs-id",
	}
	if err := db.Db.Create(existing).Error; err != nil {
		t.Fatalf("创建已有百度 SyncFile 失败: %v", err)
	}

	cache := NewMemorySyncCache(1)
	if err := cache.Insert(&SyncFileCache{
		SourceType: models.SourceTypeBaiduPan,
		FileId:     "/remote/movie.mkv",
		ParentId:   "/remote",
		Path:       "/remote",
		FileName:   "movie.mkv",
		PickCode:   "baidu-fs-id",
	}); err != nil {
		t.Fatalf("写入百度扫描缓存失败: %v", err)
	}

	syncer := &SyncStrm{
		Context:      t.Context(),
		Account:      &models.Account{},
		Sync:         &models.Sync{Logger: helpers.AppLogger},
		SyncPathId:   1,
		memSyncCache: cache,
	}
	if err := syncer.handleTempTableDiff(); err != nil {
		t.Fatalf("同步缓存差异处理失败: %v", err)
	}

	var files []models.SyncFile
	if err := db.Db.Where("sync_path_id = ?", 1).Find(&files).Error; err != nil {
		t.Fatalf("读取百度 SyncFile 失败: %v", err)
	}
	if len(files) != 1 || files[0].ID != existing.ID {
		t.Fatalf("百度 SyncFile 被删除或重复插入: %+v，期望保留 ID=%d 的单条记录", files, existing.ID)
	}
}

func TestLocalUploadParentPathKeepsConfiguredSourceRoot(t *testing.T) {
	const sourceRoot = "/source/media"
	tests := []struct {
		name       string
		remotePath string
		want       string
	}{
		{
			name:       "同步根目录",
			remotePath: sourceRoot,
			want:       sourceRoot,
		},
		{
			name:       "缓存目录只有名称时仍保留根目录",
			remotePath: "/Season 1",
			want:       "/source/media/Season 1",
		},
		{
			name:       "递归创建目录返回相对路径",
			remotePath: "Season 1/Extras",
			want:       "/source/media/Season 1/Extras",
		},
		{
			name:       "已有完整子路径不重复拼接",
			remotePath: "/source/media/Season 1",
			want:       "/source/media/Season 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localUploadParentPath(sourceRoot, tt.remotePath); got != tt.want {
				t.Fatalf("localUploadParentPath(%q, %q) = %q，期望 %q", sourceRoot, tt.remotePath, got, tt.want)
			}
		})
	}
}
