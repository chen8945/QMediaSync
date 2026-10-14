package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"qmediasync/internal/db"

	"gorm.io/gorm"
)

func TestCheckCanUploadByLocalPathMatchesActiveSourceAndPath(t *testing.T) {
	const localPath = "/media/movie/poster.jpg"
	tests := []struct {
		name      string
		status    UploadStatus
		source    UploadSource
		path      string
		canUpload bool
	}{
		{"pending", UploadStatusPending, UploadSourceStrm, localPath, false},
		{"uploading", UploadStatusUploading, UploadSourceStrm, localPath, false},
		{"pending_finalize", UploadStatusRemoteCompletedPendingFinalize, UploadSourceStrm, localPath, false},
		{"finalizing", UploadStatusRemoteCompletedFinalizing, UploadSourceStrm, localPath, false},
		{"completed", UploadStatusCompleted, UploadSourceStrm, localPath, true},
		{"failed", UploadStatusFailed, UploadSourceStrm, localPath, true},
		{"cancelled", UploadStatusCancelled, UploadSourceStrm, localPath, true},
		{"different_source", UploadStatusPending, UploadSourceDirectoryMonitor, localPath, true},
		{"different_path", UploadStatusPending, UploadSourceStrm, localPath + ".old", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			// 同路径终态历史不能掩盖其他账号、存储类型或同步目录的活跃任务。
			tasks := []DbUploadTask{
				{
					Source: UploadSourceStrm, LocalFullPath: localPath, Status: UploadStatusCompleted,
					AccountId: 1, SourceType: SourceType115, SyncPathId: 1,
				},
				{
					Source: tt.source, LocalFullPath: tt.path, Status: tt.status,
					AccountId: 2, SourceType: SourceTypeBaiduPan, SyncPathId: 2,
				},
			}
			if err := db.Db.Create(&tasks).Error; err != nil {
				t.Fatal(err)
			}
			canUpload, err := CheckCanUploadByLocalPath(t.Context(), UploadSourceStrm, localPath)
			if err != nil || canUpload != tt.canUpload {
				t.Fatalf("canUpload = %t, err = %v，期望 %t, nil", canUpload, err, tt.canUpload)
			}
		})
	}
}

func TestCheckCanUploadByLocalPathReadsAtMostOneID(t *testing.T) {
	setupQueueStatusTestDB(t)
	const localPath = "/media/movie/poster.jpg"
	tasks := []DbUploadTask{
		{Source: UploadSourceStrm, LocalFullPath: localPath, Status: UploadStatusPending},
		{Source: UploadSourceStrm, LocalFullPath: localPath, Status: UploadStatusUploading},
		{Source: UploadSourceStrm, LocalFullPath: localPath, Status: UploadStatusRemoteCompletedFinalizing},
	}
	if err := db.Db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	var queries []string
	var rows int64
	const callback = "test:upload_local_path_query"
	if err := db.Db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		queries = append(queries, tx.Statement.SQL.String())
		rows += tx.RowsAffected
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })

	canUpload, err := CheckCanUploadByLocalPath(t.Context(), UploadSourceStrm, localPath)
	if err != nil || canUpload {
		t.Fatalf("活跃任务应阻止上传: canUpload=%t, err=%v", canUpload, err)
	}
	if len(queries) != 1 || rows != 1 {
		t.Fatalf("queries=%v, rows=%d，期望只查询一次并读取一行", queries, rows)
	}
	query := strings.ToLower(queries[0])
	if !strings.HasPrefix(query, "select `id` from") || !strings.Contains(query, "limit 1") || strings.Contains(query, "count(") {
		t.Fatalf("应只投影一个 ID 并限制一行: %s", queries[0])
	}

	canUpload, err = CheckCanUploadByLocalPath(t.Context(), UploadSourceStrm, localPath+".missing")
	if err != nil || !canUpload {
		t.Fatalf("没有匹配任务时应允许上传: canUpload=%t, err=%v", canUpload, err)
	}
}

func TestCheckCanUploadByLocalPathReturnsQueryErrors(t *testing.T) {
	for _, mode := range []string{"query_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("upload task query failed")
			if mode == "cancelled" {
				cancel()
				cause = context.Canceled
			} else {
				const callback = "test:upload_local_path_failure"
				if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					tx.AddError(cause)
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
			}
			canUpload, err := CheckCanUploadByLocalPath(ctx, UploadSourceStrm, "/media/movie/poster.jpg")
			if canUpload || !errors.Is(err, cause) {
				t.Fatalf("查询失败不能放行且应保留错误链: canUpload=%t, err=%v", canUpload, err)
			}
		})
	}
}
