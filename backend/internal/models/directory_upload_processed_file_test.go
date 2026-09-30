package models

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupDirectoryUploadProcessedFileTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&DirectoryUploadProcessedFile{}, &DbUploadTask{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
}

func TestBuildDirectoryUploadSourceFingerprintUsesNanosecondMtime(t *testing.T) {
	got := BuildDirectoryUploadSourceFingerprint(1024, 123456789)
	expected := "v1:1024:123456789"
	if got != expected {
		t.Fatalf("BuildDirectoryUploadSourceFingerprint() = %q，期望 %q", got, expected)
	}
}

func TestBuildDirectoryUploadSourceFingerprintKeepsVersionOneLayout(t *testing.T) {
	got := BuildDirectoryUploadSourceFingerprint(2048, 987654321)
	parts := strings.Split(got, ":")
	if len(parts) != 3 {
		t.Fatalf("source fingerprint = %q，期望只包含 version、size、mtime_ns 三段", got)
	}
	if parts[0] != "v1" || parts[1] != "2048" || parts[2] != "987654321" {
		t.Fatalf("source fingerprint = %q，期望格式为 v1:size:mtime_ns", got)
	}
}

func TestBuildDirectoryUploadSourceKeyNormalizesRelativePath(t *testing.T) {
	left := BuildDirectoryUploadSourceKey("scope", "Season 01\\Episode.mkv")
	right := BuildDirectoryUploadSourceKey("scope", "Season 01/Episode.mkv")
	if left == "" {
		t.Fatal("BuildDirectoryUploadSourceKey() 不应返回空 key")
	}
	if left != right {
		t.Fatalf("反斜杠和斜杠相对路径生成的 key 不一致：left=%q right=%q", left, right)
	}
}

func TestBuildDirectoryUploadSourceKeyPreservesFilenameSpaces(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
	}{
		{
			name:  "保留文件名前导空格",
			left:  " movie.mkv",
			right: "movie.mkv",
		},
		{
			name:  "保留文件名尾随空格",
			left:  "movie.mkv ",
			right: "movie.mkv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := BuildDirectoryUploadSourceKey("scope", tt.left)
			right := BuildDirectoryUploadSourceKey("scope", tt.right)
			if left == right {
				t.Fatalf("不同空格语义的相对路径生成了相同 key：left_path=%q right_path=%q key=%q", tt.left, tt.right, left)
			}
		})
	}
}

func TestIsDirectoryUploadProcessedTerminal(t *testing.T) {
	tests := []struct {
		name     string
		result   DirectoryUploadProcessedResult
		expected bool
	}{
		{name: "queued 不是终态", result: DirectoryUploadProcessedResultQueued, expected: false},
		{name: "uploaded 是终态", result: DirectoryUploadProcessedResultUploaded, expected: true},
		{name: "remote_exists 是终态", result: DirectoryUploadProcessedResultRemoteExists, expected: true},
		{name: "skipped_existing 是终态", result: DirectoryUploadProcessedResultSkippedExisting, expected: true},
		{name: "failed 不是终态", result: DirectoryUploadProcessedResultFailed, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDirectoryUploadProcessedTerminal(tt.result)
			if got != tt.expected {
				t.Fatalf("IsDirectoryUploadProcessedTerminal(%q) = %v，期望 %v", tt.result, got, tt.expected)
			}
		})
	}
}

func TestDirectoryUploadProcessedFileUpsertAndFind(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)

	record := &DirectoryUploadProcessedFile{
		RuleId:            1,
		SyncPathId:        2,
		AccountId:         3,
		ScopeHash:         "scope-1",
		SourceKey:         "source-key-1",
		RelativePath:      "movie.mkv",
		LocalFullPath:     "/watch/movie.mkv",
		SourceFingerprint: "v1:5:100",
		FileSize:          5,
		LocalMtimeNs:      100,
		Result:            DirectoryUploadProcessedResultQueued,
		UploadTaskId:      10,
		ProcessedAt:       1000,
		LastSeenAt:        1000,
	}
	if err := UpsertDirectoryUploadProcessedFile(record); err != nil {
		t.Fatalf("首次 upsert processed 记录失败: %v", err)
	}

	updated := *record
	updated.FileSize = 8
	updated.LocalMtimeNs = 200
	updated.SourceFingerprint = "v1:8:200"
	updated.Result = DirectoryUploadProcessedResultUploaded
	updated.UploadTaskId = 11
	updated.ProcessedAt = 2000
	updated.LastSeenAt = 2000
	if err := UpsertDirectoryUploadProcessedFile(&updated); err != nil {
		t.Fatalf("更新 upsert processed 记录失败: %v", err)
	}

	got, err := FindDirectoryUploadProcessedBySourceKey("source-key-1")
	if err != nil {
		t.Fatalf("按 source_key 查询 processed 记录失败: %v", err)
	}
	if got.FileSize != 8 ||
		got.LocalMtimeNs != 200 ||
		got.SourceFingerprint != "v1:8:200" ||
		got.Result != DirectoryUploadProcessedResultUploaded ||
		got.UploadTaskId != 11 ||
		got.LastSeenAt != 2000 {
		t.Fatalf("upsert 后记录 = %+v，期望更新后的字段", got)
	}

	if err := UpsertDirectoryUploadProcessedFile(&DirectoryUploadProcessedFile{}); err == nil {
		t.Fatal("source_key 为空时 upsert 应返回错误")
	}
}

func TestMarkDirectoryUploadProcessedUploaded(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)

	record := &DirectoryUploadProcessedFile{
		SourceKey:         "source-key-mark",
		SourceFingerprint: "v1:5:100",
		Result:            DirectoryUploadProcessedResultQueued,
		UploadTaskId:      23,
		ProcessedAt:       100,
		LastSeenAt:        100,
	}
	if err := db.Db.Create(record).Error; err != nil {
		t.Fatalf("创建 processed 记录失败: %v", err)
	}

	if err := MarkDirectoryUploadProcessedUploaded(23, DirectoryUploadProcessedResultUploaded); err != nil {
		t.Fatalf("标记 processed 上传完成失败: %v", err)
	}

	var got DirectoryUploadProcessedFile
	if err := db.Db.Where("upload_task_id = ?", 23).First(&got).Error; err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if got.Result != DirectoryUploadProcessedResultUploaded || got.ProcessedAt == 100 || got.LastSeenAt == 100 {
		t.Fatalf("processed 记录 = %+v，期望 result 和时间已更新", got)
	}
}

func TestCleanupDirectoryUploadProcessedFilesKeepsExistingSuccessfulSource(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)
	now := time.Unix(1_000, 0)
	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	record := &DirectoryUploadProcessedFile{
		SourceKey:         "source-key-existing",
		LocalFullPath:     filePath,
		SourceFingerprint: "v1:5:100",
		Result:            DirectoryUploadProcessedResultUploaded,
		ProcessedAt:       now.Add(-48 * time.Hour).Unix(),
		LastSeenAt:        now.Add(-48 * time.Hour).Unix(),
	}
	if err := db.Db.Create(record).Error; err != nil {
		t.Fatalf("创建 processed 记录失败: %v", err)
	}

	deleted, err := CleanupDirectoryUploadProcessedFiles(now, 24*time.Hour)
	if err != nil {
		t.Fatalf("清理 processed 记录失败: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("删除数量 = %d，期望保留仍存在的成功源文件", deleted)
	}

	var total int64
	if err := db.Db.Model(&DirectoryUploadProcessedFile{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 processed 记录失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("processed 记录数 = %d，期望 1", total)
	}
}

func TestCleanupDirectoryUploadProcessedFilesDeletesMissingExpiredSource(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)
	now := time.Unix(1_000, 0)
	filePath := filepath.Join(t.TempDir(), "missing.mkv")

	record := &DirectoryUploadProcessedFile{
		SourceKey:         "source-key-missing",
		LocalFullPath:     filePath,
		SourceFingerprint: "v1:5:100",
		Result:            DirectoryUploadProcessedResultRemoteExists,
		ProcessedAt:       now.Add(-48 * time.Hour).Unix(),
		LastSeenAt:        now.Add(-48 * time.Hour).Unix(),
	}
	if err := db.Db.Create(record).Error; err != nil {
		t.Fatalf("创建 processed 记录失败: %v", err)
	}

	deleted, err := CleanupDirectoryUploadProcessedFiles(now, 24*time.Hour)
	if err != nil {
		t.Fatalf("清理 processed 记录失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("删除数量 = %d，期望删除长期未见且源文件缺失的成功记录", deleted)
	}
}

func TestCleanupDirectoryUploadProcessedFilesDeletesExpiredFailedRecord(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)
	now := time.Unix(1_000, 0)

	record := &DirectoryUploadProcessedFile{
		SourceKey:         "source-key-failed",
		LocalFullPath:     filepath.Join(t.TempDir(), "movie.mkv"),
		SourceFingerprint: "v1:5:100",
		Result:            DirectoryUploadProcessedResultFailed,
		ProcessedAt:       now.Add(-48 * time.Hour).Unix(),
		LastSeenAt:        now.Add(-48 * time.Hour).Unix(),
	}
	if err := db.Db.Create(record).Error; err != nil {
		t.Fatalf("创建 processed 记录失败: %v", err)
	}

	deleted, err := CleanupDirectoryUploadProcessedFiles(now, 24*time.Hour)
	if err != nil {
		t.Fatalf("清理 processed 记录失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("删除数量 = %d，期望删除过期 failed 记录", deleted)
	}
}

func TestCleanupDirectoryUploadProcessedFilesDeletesQueuedWithoutActiveTaskBeforeTTL(t *testing.T) {
	tests := []struct {
		name        string
		task        *DbUploadTask
		wantDeleted int64
	}{
		{name: "关联任务不存在", wantDeleted: 1},
		{name: "关联任务失败", task: &DbUploadTask{Status: UploadStatusFailed}, wantDeleted: 1},
		{name: "关联任务已取消", task: &DbUploadTask{Status: UploadStatusCancelled}, wantDeleted: 1},
		{name: "关联任务已完成", task: &DbUploadTask{Status: UploadStatusCompleted}, wantDeleted: 1},
		{name: "关联任务等待中", task: &DbUploadTask{Status: UploadStatusPending}, wantDeleted: 0},
		{name: "关联任务上传中", task: &DbUploadTask{Status: UploadStatusUploading}, wantDeleted: 0},
		{name: "关联任务待收尾", task: &DbUploadTask{Status: UploadStatusRemoteCompletedPendingFinalize}, wantDeleted: 0},
		{name: "关联任务收尾中", task: &DbUploadTask{Status: UploadStatusRemoteCompletedFinalizing}, wantDeleted: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadProcessedFileTestDB(t)
			now := time.Unix(1_000, 0)
			var taskID uint = 99
			if tt.task != nil {
				if err := db.Db.Create(tt.task).Error; err != nil {
					t.Fatalf("创建上传任务失败: %v", err)
				}
				taskID = tt.task.ID
			}
			record := &DirectoryUploadProcessedFile{
				SourceKey:         "source-key-queued",
				SourceFingerprint: "v1:5:100",
				Result:            DirectoryUploadProcessedResultQueued,
				UploadTaskId:      taskID,
				ProcessedAt:       now.Unix(),
				LastSeenAt:        now.Unix(),
			}
			if err := db.Db.Create(record).Error; err != nil {
				t.Fatalf("创建 processed 记录失败: %v", err)
			}

			deleted, err := CleanupDirectoryUploadProcessedFiles(now, 24*time.Hour)
			if err != nil {
				t.Fatalf("清理 processed 记录失败: %v", err)
			}
			if deleted != tt.wantDeleted {
				t.Fatalf("删除数量 = %d，期望 %d", deleted, tt.wantDeleted)
			}
		})
	}
}

func TestCleanupDirectoryUploadProcessedFilesDeletesAwaitingStrmWithMissingUploadTask(t *testing.T) {
	tests := []struct {
		name        string
		result      DirectoryUploadProcessedResult
		task        *DbUploadTask
		uploadID    uint
		wantDeleted int64
	}{
		{
			name:        "上传完成等待 STRM 但任务不存在",
			result:      DirectoryUploadProcessedResultUploadedPendingStrm,
			uploadID:    99,
			wantDeleted: 1,
		},
		{
			name:        "远端已存在等待 STRM 但任务不存在",
			result:      DirectoryUploadProcessedResultRemoteExistsPendingStrm,
			uploadID:    99,
			wantDeleted: 1,
		},
		{
			name:        "STRM 入队失败但任务不存在",
			result:      DirectoryUploadProcessedResultStrmEnqueueFailed,
			uploadID:    99,
			wantDeleted: 1,
		},
		{
			name:        "STRM 入队失败且完成任务仍存在",
			result:      DirectoryUploadProcessedResultStrmEnqueueFailed,
			task:        &DbUploadTask{Status: UploadStatusCompleted},
			wantDeleted: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadProcessedFileTestDB(t)
			now := time.Unix(1_000, 0)
			uploadTaskID := tt.uploadID
			if tt.task != nil {
				if err := db.Db.Create(tt.task).Error; err != nil {
					t.Fatalf("创建上传任务失败: %v", err)
				}
				uploadTaskID = tt.task.ID
			}
			record := &DirectoryUploadProcessedFile{
				SourceKey:         "source-key-awaiting-strm",
				SourceFingerprint: "v1:5:100",
				Result:            tt.result,
				UploadTaskId:      uploadTaskID,
				ProcessedAt:       now.Unix(),
				LastSeenAt:        now.Unix(),
			}
			if err := db.Db.Create(record).Error; err != nil {
				t.Fatalf("创建 processed 记录失败: %v", err)
			}

			deleted, err := CleanupDirectoryUploadProcessedFiles(now, 24*time.Hour)
			if err != nil {
				t.Fatalf("清理 processed 记录失败: %v", err)
			}
			if deleted != tt.wantDeleted {
				t.Fatalf("删除数量 = %d，期望 %d", deleted, tt.wantDeleted)
			}
			var total int64
			if err := db.Db.Model(&DirectoryUploadProcessedFile{}).Count(&total).Error; err != nil {
				t.Fatalf("统计 processed 记录失败: %v", err)
			}
			if total != 1-tt.wantDeleted {
				t.Fatalf("processed 记录数 = %d，期望 %d", total, 1-tt.wantDeleted)
			}
		})
	}
}

func TestCleanupDirectoryUploadProcessedFilesContinuesAfterActiveQueuedBatch(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)
	now := time.Unix(1_000, 0)
	expiredAt := now.Add(-48 * time.Hour).Unix()

	for i := range 500 {
		status := UploadStatusPending
		if i%2 == 1 {
			status = UploadStatusUploading
		}
		task := &DbUploadTask{Status: status}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建第 %d 个 active 上传任务失败: %v", i, err)
		}
		record := &DirectoryUploadProcessedFile{
			SourceKey:         "source-key-active-" + time.Unix(int64(i), 0).Format("150405"),
			SourceFingerprint: "v1:5:100",
			Result:            DirectoryUploadProcessedResultQueued,
			UploadTaskId:      task.ID,
			ProcessedAt:       expiredAt,
			LastSeenAt:        expiredAt,
		}
		if err := db.Db.Create(record).Error; err != nil {
			t.Fatalf("创建第 %d 个 active queued processed 记录失败: %v", i, err)
		}
	}
	deletable := &DirectoryUploadProcessedFile{
		SourceKey:         "source-key-deletable-501",
		SourceFingerprint: "v1:5:100",
		Result:            DirectoryUploadProcessedResultQueued,
		ProcessedAt:       expiredAt,
		LastSeenAt:        expiredAt,
	}
	if err := db.Db.Create(deletable).Error; err != nil {
		t.Fatalf("创建第 501 个可删除 queued processed 记录失败: %v", err)
	}

	deleted, err := CleanupDirectoryUploadProcessedFiles(now, 24*time.Hour)
	if err != nil {
		t.Fatalf("清理 processed 记录失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("删除数量 = %d，期望跳过前 500 条 active queued 后继续删除第 501 条", deleted)
	}
	var total int64
	if err := db.Db.Model(&DirectoryUploadProcessedFile{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 processed 记录失败: %v", err)
	}
	if total != 500 {
		t.Fatalf("processed 记录数 = %d，期望保留 500 条 active queued", total)
	}
	var deletedRecord DirectoryUploadProcessedFile
	err = db.Db.Where("source_key = ?", "source-key-deletable-501").First(&deletedRecord).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("第 501 条 queued 查询错误 = %v，期望已删除", err)
	}
}

func TestFindDirectoryUploadProcessedBySourceKeyReturnsRecordNotFound(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)

	_, err := FindDirectoryUploadProcessedBySourceKey("missing-key")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("查询缺失 source_key 错误 = %v，期望 gorm.ErrRecordNotFound", err)
	}
}

func TestFindDirectoryUploadProcessedBySourceKeyRejectsEmptyKey(t *testing.T) {
	setupDirectoryUploadProcessedFileTestDB(t)

	for _, sourceKey := range []string{"", "   "} {
		t.Run("source_key 为空白", func(t *testing.T) {
			_, err := FindDirectoryUploadProcessedBySourceKey(sourceKey)
			if err == nil {
				t.Fatal("source_key 为空白时查询应返回错误")
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("source_key 为空白错误 = %v，期望参数校验错误而不是查库未命中", err)
			}
		})
	}
}
