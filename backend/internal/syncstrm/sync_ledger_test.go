package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/syncscope"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func ledgerTestFile(i int) models.SyncFile {
	return models.SyncFile{
		BaseModel:  models.BaseModel{CreatedAt: 100, UpdatedAt: 200},
		SourceType: models.SourceType115, AccountId: 7, SyncPathId: 1,
		FileId: fmt.Sprintf("file-%04d", i), ParentId: "old-parent", FileName: fmt.Sprintf("old-%d.mkv", i),
		FileSize: 8192, FileType: v115open.TypeFile, MTime: 123,
		PickCode: "old-pick", Sha1: "old-sha1", Path: "/old", LocalFilePath: fmt.Sprintf("/target/old-%d.strm", i),
		ThumbUrl: "old-thumb", OpenlistSign: "old-sign", OpenlistObjectId: "old-object",
		OpenlistSHA1: "old-open-sha1", OpenlistMD5: "old-md5",
		IsVideo: true, IsMeta: true, Uploaded: true, Processed: true,
	}
}

func ledgerTestCache(row models.SyncFile, changed bool) *SyncFileCache {
	file := &SyncFileCache{
		SourceType: row.SourceType, FileId: row.FileId, ParentId: row.ParentId, FileName: row.FileName,
		FileSize: row.FileSize, MTime: row.MTime, PickCode: row.PickCode, Sha1: row.Sha1,
		Path: row.Path, LocalFilePath: row.LocalFilePath, ThumbUrl: row.ThumbUrl,
		OpenlistSign: row.OpenlistSign, OpenlistObjectId: row.OpenlistObjectId,
		OpenlistSHA1: row.OpenlistSHA1, OpenlistMD5: row.OpenlistMD5,
	}
	if changed {
		file.ParentId, file.FileName, file.Path = "new-parent", "new-'?电影.mkv", "/new"
		file.FileSize, file.MTime, file.PickCode, file.Sha1 = 0, 0, "", ""
		file.LocalFilePath, file.ThumbUrl = "/target/new-"+row.FileId+".strm", ""
		file.OpenlistSign, file.OpenlistObjectId = "", "new-object"
		file.OpenlistSHA1, file.OpenlistMD5 = "new-sha1", ""
	}
	return file
}

func ledgerTestSync(t *testing.T, files []*SyncFileCache) *SyncStrm {
	t.Helper()
	cache := NewMemorySyncCache(1)
	for _, file := range files {
		if err := cache.Insert(file); err != nil {
			t.Fatal(err)
		}
	}
	return &SyncStrm{
		Context: t.Context(), SyncPathId: 1, SourcePath: "/remote", TargetPath: "/target",
		Account: &models.Account{BaseModel: models.BaseModel{ID: 7}, SourceType: models.SourceType115},
		Sync:    &models.Sync{Logger: helpers.AppLogger}, memSyncCache: cache,
	}
}

func TestSyncLedgerBatchBoundariesAndFields(t *testing.T) {
	for _, mode := range []string{"new", "unchanged", "changed"} {
		for _, count := range []int{1, 31, 32, 33, 255, 256, 257} {
			t.Run(fmt.Sprintf("%s/%d", mode, count), func(t *testing.T) {
				setupSyncStateTestDB(t)
				const now = 900
				db.Db.NowFunc = func() time.Time { return time.Unix(now, 0) }
				rows := make([]models.SyncFile, count)
				files := make([]*SyncFileCache, count)
				for i := range rows {
					rows[i] = ledgerTestFile(i)
					files[i] = ledgerTestCache(rows[i], mode == "changed")
				}
				if mode != "new" {
					if err := db.Db.CreateInBatches(&rows, 32).Error; err != nil {
						t.Fatal(err)
					}
				}
				sentinel := ledgerTestFile(count)
				sentinel.SyncPathId = 2
				if err := db.Db.Create(&sentinel).Error; err != nil {
					t.Fatal(err)
				}

				var writes int
				var written int64
				batchSize, parameterLimit := 32, 999
				if db.Db.Dialector.Name() == "postgres" {
					parameterLimit = 65535
					if mode == "changed" {
						batchSize = 256
					}
				}
				if mode == "unchanged" {
					batchSize = 256
				}
				checkWrite := func(tx *gorm.DB) {
					writes++
					written += tx.RowsAffected
					if mode == "unchanged" {
						if update := tx.Statement.Dest.(map[string]any); len(update) != 1 || update["updated_at"] != int64(now) {
							t.Errorf("无变化记录重写了其他字段：%v", update)
						}
					}
					if tx.RowsAffected < 1 || tx.RowsAffected > int64(batchSize) || len(tx.Statement.Vars) > parameterLimit {
						t.Errorf("SQL 超出子批限制：rows=%d vars=%d", tx.RowsAffected, len(tx.Statement.Vars))
					}
				}
				if err := db.Db.Callback().Create().After("gorm:create").Register("ledger_test_create", checkWrite); err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Callback().Update().After("gorm:update").Register("ledger_test_update", checkWrite); err != nil {
					t.Fatal(err)
				}
				syncer := ledgerTestSync(t, files)
				if err := syncer.handleTempTableDiff(); err != nil {
					t.Fatal(err)
				}
				if want := (count + batchSize - 1) / batchSize; writes != want || written != int64(count) {
					t.Fatalf("写 SQL=%d rows=%d，期望 SQL=%d rows=%d", writes, written, want, count)
				}
				var got []models.SyncFile
				if err := db.Db.Where("sync_path_id = ?", 1).Order("file_id").Find(&got).Error; err != nil {
					t.Fatal(err)
				}
				if len(got) != count || syncer.memSyncCache.Count() != 0 || len(syncer.memSyncCache.parentIndex) != 0 {
					t.Fatalf("条目遗漏或缓存未释放：rows=%d cache=%d", len(got), syncer.memSyncCache.Count())
				}
				for i, actual := range got {
					want := rows[i]
					want.UpdatedAt = now
					if mode == "new" {
						want.ID, want.CreatedAt = actual.ID, now
						want.FileType, want.IsVideo, want.IsMeta, want.Uploaded, want.Processed = "", false, false, false, false
					}
					if mode == "changed" {
						want.ParentId, want.FileName, want.Path = "new-parent", "new-'?电影.mkv", "/new"
						want.FileSize, want.MTime, want.PickCode, want.Sha1 = 0, 0, "", ""
						want.LocalFilePath, want.ThumbUrl = "/target/new-"+want.FileId+".strm", ""
						want.OpenlistSign, want.OpenlistObjectId, want.OpenlistSHA1, want.OpenlistMD5 = "", "new-object", "new-sha1", ""
					}
					if !reflect.DeepEqual(actual, want) {
						t.Fatalf("第 %d 行字段不符：\ngot=%+v\nwant=%+v", i, actual, want)
					}
				}
				var other models.SyncFile
				if err := db.Db.First(&other, sentinel.ID).Error; err != nil || !reflect.DeepEqual(other, sentinel) {
					t.Fatalf("其他目录被修改：%+v err=%v", other, err)
				}
			})
		}
	}
}

func TestSyncLedgerChangedPageRollbackAndFreshRetry(t *testing.T) {
	setupSyncStateTestDB(t)
	if err := db.Db.AutoMigrate(&models.SyncPath{}); err != nil {
		t.Fatal(err)
	}
	sp := &models.SyncPath{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115, AccountId: 7, BaseCid: "cache-test", LocalPath: t.TempDir(), RemotePath: "/remote"}
	if err := db.Db.Create(sp).Error; err != nil {
		t.Fatal(err)
	}

	rows := make([]models.SyncFile, 600)
	newFiles := func() []*SyncFileCache {
		files := make([]*SyncFileCache, len(rows))
		for i, row := range rows {
			files[i] = ledgerTestCache(row, true)
		}
		return files
	}
	for i := range rows {
		rows[i] = ledgerTestFile(i)
	}
	if err := db.Db.CreateInBatches(&rows, 32).Error; err != nil {
		t.Fatal(err)
	}
	_, release, err := models.AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	wantErr := errors.New("changed sub-batch failed")
	calls := 0
	failAt := 10
	if db.Db.Dialector.Name() == "postgres" {
		failAt = 2
	}
	if err := db.Db.Callback().Update().Before("gorm:update").Register("ledger_test_failure", func(tx *gorm.DB) {
		calls++
		if calls == failAt {
			tx.AddError(wantErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	syncer := ledgerTestSync(t, newFiles())
	if err := syncer.handleTempTableDiff(); !errors.Is(err, wantErr) {
		t.Fatalf("错误=%v，期望 %v", err, wantErr)
	}

	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{SourceType: "115", AccountID: 7, RemotePath: "/new"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, release, err = models.AcquireSyncPathScope(ctx, sp.ID)
	cancel()
	busy()
	if release != nil {
		release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partial commit reused old positions: %v", err)
	}
	var got []models.SyncFile
	if err := db.Db.Order("id").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	for i, actual := range got {
		if i < 256 {
			if actual.Path != "/new" || actual.ID != rows[i].ID || !actual.Processed || !actual.Uploaded {
				t.Fatalf("已提交页内容错误：%+v", actual)
			}
		} else if !reflect.DeepEqual(actual, rows[i]) {
			t.Fatalf("失败页或未开始页发生变化：%+v", actual)
		}
		_, err := syncer.memSyncCache.GetByFileId(actual.FileId)
		if (err == nil) != (i >= 256) {
			t.Fatalf("缓存消费早于提交：i=%d err=%v", i, err)
		}
	}
	if err := db.Db.Callback().Update().Remove("ledger_test_failure"); err != nil {
		t.Fatal(err)
	}
	// 下一次任务重建扫描缓存，保留已提交行的 ID，不复用旧快照。
	if err := ledgerTestSync(t, newFiles()).handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := db.Db.Order("id").Find(&got).Error; err != nil || len(got) != len(rows) {
		t.Fatalf("重试行数=%d err=%v", len(got), err)
	}
	for i, actual := range got {
		if actual.ID != rows[i].ID || actual.Path != "/new" || !actual.Processed || !actual.Uploaded {
			t.Fatalf("重试结果错误：%+v", actual)
		}
	}
}

func TestSyncLedgerMixedPageRollback(t *testing.T) {
	setupSyncStateTestDB(t)
	rows := []models.SyncFile{ledgerTestFile(0), ledgerTestFile(1), ledgerTestFile(2)}
	if err := db.Db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	files := []*SyncFileCache{ledgerTestCache(rows[0], false), ledgerTestCache(rows[1], true)}
	wantErr := errors.New("delete after updates failed")
	if err := db.Db.Callback().Delete().Before("gorm:delete").Register("ledger_test_delete", func(tx *gorm.DB) { tx.AddError(wantErr) }); err != nil {
		t.Fatal(err)
	}
	syncer := ledgerTestSync(t, files)
	if err := syncer.handleTempTableDiff(); !errors.Is(err, wantErr) {
		t.Fatalf("错误=%v，期望 %v", err, wantErr)
	}
	var got []models.SyncFile
	if err := db.Db.Order("id").Find(&got).Error; err != nil || !reflect.DeepEqual(got, rows) || syncer.memSyncCache.Count() != 2 {
		t.Fatalf("同页无变化更新时间/变化字段未回滚：%+v cache=%d err=%v", got, syncer.memSyncCache.Count(), err)
	}
}

func TestSyncLedgerUsesEarliestStoredIdentity(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		t.Run(fmt.Sprint(incremental), func(t *testing.T) {
			setupSyncStateTestDB(t)
			rows := make([]models.SyncFile, 257)
			for i := range rows {
				rows[i] = ledgerTestFile(0)
			}
			if err := db.Db.CreateInBatches(&rows, 32).Error; err != nil {
				t.Fatal(err)
			}
			syncer := ledgerTestSync(t, []*SyncFileCache{ledgerTestCache(rows[0], true)})
			if incremental {
				syncer.missingCleanupReason = "增量不按缺项删除"
			}
			if err := syncer.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			var got []models.SyncFile
			if err := db.Db.Order("id").Find(&got).Error; err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if incremental {
				wantCount = 257
			}
			if len(got) != wantCount || got[0].ID != rows[0].ID || got[0].Path != "/new" {
				t.Fatalf("未保留最早记录：%+v", got)
			}
			if incremental && !reflect.DeepEqual(got[1:], rows[1:]) {
				t.Fatal("增量改写了后续重复身份")
			}
		})
	}
}

func TestSyncLedgerNormalizesLegacyNullSyncFields(t *testing.T) {
	setupSyncStateTestDB(t)
	row := ledgerTestFile(0)
	row.Sha1, row.FileSize = "", 0
	if err := db.Db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Exec("UPDATE sync_files SET sha1 = NULL, file_size = NULL, uploaded = NULL WHERE id = ?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ledgerTestSync(t, []*SyncFileCache{ledgerTestCache(row, false)}).handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Db.Model(&models.SyncFile{}).Where("id = ? AND sha1 = '' AND file_size = 0 AND uploaded IS NULL", row.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("未按原语义写零值，或覆盖其他状态：count=%d err=%v", count, err)
	}
}

func TestMemoryCacheDeletionStillMaintainsParentIndex(t *testing.T) {
	cache := NewMemorySyncCache(1)
	for i := range 3 {
		if err := cache.Insert(ledgerTestCache(ledgerTestFile(i), false)); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.DeleteByFileId("file-0001"); err != nil {
		t.Fatal(err)
	}
	children, err := cache.GetByParentId("old-parent")
	if err != nil || len(children) != 2 || strings.Join([]string{children[0].FileId, children[1].FileId}, ",") != "file-0000,file-0002" {
		t.Fatalf("前台父目录索引未更新：%v err=%v", children, err)
	}
}
