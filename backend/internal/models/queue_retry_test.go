package models

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/v115open"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

func TestCanRetryFailedDownloadTask(t *testing.T) {
	task := &DbDownloadTask{Status: DownloadStatusFailed, Error: "network", RetryCount: 0}
	task.PrepareDownloadRetry(1)
	if task.Status != DownloadStatusPending {
		t.Fatal("下载失败任务应回到等待中")
	}
	if task.Error != "" {
		t.Fatal("重试时应清空错误信息")
	}
	if task.RetryCount != 1 {
		t.Fatal("重试次数应递增")
	}
}

func TestDownloadTaskDoesNotRetryBeyondLimit(t *testing.T) {
	task := &DbDownloadTask{Status: DownloadStatusFailed, RetryCount: 1}
	if task.CanRetry(1) {
		t.Fatal("达到最大重试次数后不应继续重试")
	}
}

func TestCanRetryFailedUploadTask(t *testing.T) {
	task := &DbUploadTask{Status: UploadStatusFailed, Error: "network", RetryCount: 0}
	task.PrepareUploadRetry(1)
	if task.Status != UploadStatusPending {
		t.Fatal("上传失败任务应回到等待中")
	}
	if task.Error != "" {
		t.Fatal("重试时应清空错误信息")
	}
	if task.RetryCount != 1 {
		t.Fatal("重试次数应递增")
	}
}

func TestUploadTaskDoesNotRetryBeyondLimit(t *testing.T) {
	task := &DbUploadTask{Status: UploadStatusFailed, RetryCount: 1}
	if task.CanRetry(1) {
		t.Fatal("达到最大重试次数后不应继续重试")
	}
}

func TestPrepare115UploadSessionResumesValidSession(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		AccountId:      1,
		Status:         UploadStatusFailed,
		Source:         UploadSourceDirectoryMonitor,
		SourceType:     SourceType115,
		LocalFullPath:  "/watch/movie.mkv",
		FileName:       "movie.mkv",
		FileSize:       1024,
		RemotePathId:   "100",
		UploadedBytes:  512,
		UploadResult:   UploadResultUnknown,
		ResumeState:    UploadResumeStateNone,
		RapidWaitUntil: 0,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	session := &UploadSession{
		UploadTaskId:   task.ID,
		AccountId:      task.AccountId,
		LocalFullPath:  task.LocalFullPath,
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     100,
		LocalSignature: "sig",
		FileSha1:       "sha1",
		Preid:          "preid",
		ParentFileId:   task.RemotePathId,
		PickCode:       "pick-1",
		Bucket:         "bucket-1",
		Object:         "object-1",
		UploadId:       "upload-1",
		PartSize:       256,
		UploadedBytes:  512,
		UploadedParts:  2,
		Status:         UploadSessionStatusMultipart,
		ResumeState:    UploadResumeStateNewSession,
	}
	if err := session.Save(); err != nil {
		t.Fatalf("保存上传会话失败: %v", err)
	}

	got, err := task.prepare115UploadSession(upload115LocalFileInfo{
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     100,
		LocalSignature: "sig",
		FileSha1:       "sha1",
		Preid:          "preid",
	})
	if err != nil {
		t.Fatalf("准备上传会话失败: %v", err)
	}

	if got.ID != session.ID {
		t.Fatalf("会话 ID = %d，期望复用已有会话 %d", got.ID, session.ID)
	}
	if got.ResumeState != UploadResumeStateResumedSession || got.LastResumeAt == 0 {
		t.Fatalf("会话恢复状态 = %+v，期望 resumed_session 并记录恢复时间", got)
	}
	if task.ResumeState != UploadResumeStateResumedSession || task.UploadedBytes != 512 {
		t.Fatalf("任务续传字段 = %+v，期望同步 session 进度", task)
	}
}

func TestPrepare115UploadSessionRestartsChangedSignature(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		AccountId:     1,
		Status:        UploadStatusPending,
		Source:        UploadSourceDirectoryMonitor,
		SourceType:    SourceType115,
		LocalFullPath: "/watch/movie.mkv",
		FileName:      "movie.mkv",
		FileSize:      1024,
		RemotePathId:  "100",
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	session := &UploadSession{
		UploadTaskId:      task.ID,
		AccountId:         task.AccountId,
		LocalFullPath:     task.LocalFullPath,
		FileName:          task.FileName,
		FileSize:          task.FileSize,
		LocalMtime:        100,
		LocalSignature:    "old-sig",
		FileSha1:          "old-sha1",
		Preid:             "old-preid",
		ParentFileId:      task.RemotePathId,
		PickCode:          "pick-1",
		SignKey:           "sign-key",
		SignRangeStart:    1,
		SignRangeEnd:      2,
		SignValSha1:       "sign-sha1",
		Callback:          "callback",
		CallbackVar:       "callback-var",
		Bucket:            "bucket-1",
		Object:            "object-1",
		Endpoint:          "endpoint-1",
		Region:            "region-1",
		UploadId:          "upload-1",
		PartSize:          1024,
		TotalParts:        2,
		UploadedBytes:     512,
		UploadedParts:     1,
		LastPartNumber:    1,
		LastPartEtag:      "etag-1",
		Status:            UploadSessionStatusMultipart,
		ResumeState:       UploadResumeStateResumedSession,
		RapidWaitUntil:    123,
		RapidWaitAttempts: 2,
	}
	if err := session.Save(); err != nil {
		t.Fatalf("保存上传会话失败: %v", err)
	}

	got, err := task.prepare115UploadSession(upload115LocalFileInfo{
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     time.Now().Unix(),
		LocalSignature: "new-sig",
		FileSha1:       "new-sha1",
		Preid:          "new-preid",
	})
	if err != nil {
		t.Fatalf("本地文件签名变化时应立即重建 session：%v", err)
	}
	if got.ID != session.ID || got.Status != UploadSessionStatusInit || got.ResumeState != UploadResumeStateSessionExpiredRestarted {
		t.Fatalf("重建后的会话 = %+v，期望复用当前记录并等待新的 init", got)
	}
	if got.FileSha1 != "new-sha1" || got.Preid != "new-preid" || got.LocalSignature != "new-sig" || got.LocalMtime == 100 {
		t.Fatalf("重建后的本地签名 = %+v，期望保存当前文件签名", got)
	}
	if got.UploadId != "" || got.PickCode != "" || got.Bucket != "" || got.Object != "" || got.Endpoint != "" || got.Region != "" ||
		got.SignKey != "" || got.SignValSha1 != "" || got.PartSize != 0 || got.TotalParts != 0 || got.UploadedBytes != 0 || got.UploadedParts != 0 || got.LastPartNumber != 0 || got.LastPartEtag != "" {
		t.Fatalf("重建后的 checkpoint 未清空：%+v", got)
	}
	if got.LastError == "" || task.ResumeState != UploadResumeStateSessionExpiredRestarted || task.UploadedBytes != 0 || task.RapidWaitAttempts != 0 || task.RapidWaitUntil != 0 {
		t.Fatalf("重建后的任务状态 = %+v，期望记录废弃原因并清空进度", task)
	}

	persisted, readErr := GetUploadSessionByUploadTaskId(task.ID)
	if readErr != nil {
		t.Fatalf("读取上传会话失败: %v", readErr)
	}
	if persisted.Status != UploadSessionStatusInit || persisted.ResumeState != UploadResumeStateSessionExpiredRestarted || persisted.UploadId != "" {
		t.Fatalf("持久化会话状态 = %+v，期望 init、重启状态和空 checkpoint", persisted)
	}
}

func TestPrepare115UploadSessionKeepsRestartedSessionForInit(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		AccountId:     1,
		Status:        UploadStatusPending,
		Source:        UploadSourceDirectoryMonitor,
		SourceType:    SourceType115,
		LocalFullPath: "/watch/movie.mkv",
		FileName:      "movie.mkv",
		FileSize:      1024,
		RemotePathId:  "100",
		ResumeState:   UploadResumeStateSessionExpiredRestarted,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	session := &UploadSession{
		UploadTaskId:   task.ID,
		AccountId:      task.AccountId,
		LocalFullPath:  task.LocalFullPath,
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     100,
		LocalSignature: "sig",
		FileSha1:       "sha1",
		Preid:          "preid",
		ParentFileId:   task.RemotePathId,
		Status:         UploadSessionStatusInit,
		ResumeState:    UploadResumeStateSessionExpiredRestarted,
	}
	if err := session.Save(); err != nil {
		t.Fatalf("保存上传会话失败: %v", err)
	}

	got, err := task.prepare115UploadSession(upload115LocalFileInfo{
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     100,
		LocalSignature: "sig",
		FileSha1:       "sha1",
		Preid:          "preid",
	})
	if err != nil {
		t.Fatalf("准备上传会话失败: %v", err)
	}
	if got.ResumeState != UploadResumeStateSessionExpiredRestarted || got.UploadId != "" {
		t.Fatalf("会话 = %+v，期望保持 session_expired_restarted 并等待重新 init", got)
	}
	if task.ResumeState != UploadResumeStateSessionExpiredRestarted || task.UploadedBytes != 0 {
		t.Fatalf("任务 = %+v，期望保持 session_expired_restarted 并清空进度", task)
	}
}

func TestUploadMultipartRetriesInvalidOSSCheckpointWithNewMultipart(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		AccountId:     1,
		Status:        UploadStatusPending,
		Source:        UploadSourceDirectoryMonitor,
		SourceType:    SourceType115,
		LocalFullPath: "/watch/movie.mkv",
		FileName:      "movie.mkv",
		FileSize:      1024,
		RemotePathId:  "100",
		UploadedBytes: 512,
		ResumeState:   UploadResumeStateResumedSession,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	session := &UploadSession{
		UploadTaskId:   task.ID,
		AccountId:      task.AccountId,
		LocalFullPath:  task.LocalFullPath,
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     100,
		LocalSignature: "sig",
		FileSha1:       "sha1",
		Preid:          "preid",
		ParentFileId:   task.RemotePathId,
		PickCode:       "pick-1",
		Bucket:         "bucket-1",
		Object:         "object-1",
		Callback:       `{"callbackUrl":"https://callback.example/upload"}`,
		CallbackVar:    `{"x:keep":"yes"}`,
		UploadId:       "bad-upload",
		PartSize:       256,
		UploadedBytes:  512,
		UploadedParts:  2,
		Status:         UploadSessionStatusMultipart,
		ResumeState:    UploadResumeStateResumedSession,
	}
	if err := session.Save(); err != nil {
		t.Fatalf("保存上传会话失败: %v", err)
	}
	originalUpload115MultipartWithResult := upload115MultipartWithResult
	calls := 0
	upload115MultipartWithResult = func(_ context.Context, _ *v115open.OpenClient, input v115open.UploadMultipartInput) (v115open.OSSMultipartUploadResult, error) {
		calls++
		switch calls {
		case 1:
			if input.UploadId != "bad-upload" {
				t.Fatalf("第一次 multipart upload_id = %q，期望 bad-upload", input.UploadId)
			}
			return v115open.OSSMultipartUploadResult{}, fmt.Errorf("查询 OSS 已上传分片失败：%w", &oss.ServiceError{
				Code:       "NoSuchUpload",
				Message:    "The specified upload does not exist. The upload ID may be invalid, or the upload may have been aborted or completed.",
				StatusCode: 404,
			})
		case 2:
			if input.UploadId != "" {
				t.Fatalf("重新开始 multipart 时 upload_id = %q，期望为空", input.UploadId)
			}
			return v115open.OSSMultipartUploadResult{
				CallbackResult: map[string]any{
					"state":   true,
					"message": "",
					"data": map[string]any{
						"file_id":   "file-1",
						"pick_code": "pick-1",
						"parent_id": "100",
						"sha1":      "sha1",
						"size":      float64(1024),
						"mtime":     float64(123456),
					},
				},
				UploadId:      "new-upload",
				PartSize:      256,
				TotalParts:    4,
				UploadedBytes: 1024,
				UploadedParts: 4,
			}, nil
		default:
			t.Fatalf("multipart 调用次数过多：%d", calls)
			return v115open.OSSMultipartUploadResult{}, nil
		}
	}
	t.Cleanup(func() {
		upload115MultipartWithResult = originalUpload115MultipartWithResult
	})

	result, err := open115UploadRunner{}.uploadMultipart(context.Background(), task, nil, session, upload115LocalFileInfo{
		FileName:       task.FileName,
		FileSize:       task.FileSize,
		LocalMtime:     100,
		LocalSignature: "sig",
		FileSha1:       "sha1",
		Preid:          "preid",
	})
	if err != nil {
		t.Fatalf("OSS checkpoint 失效后应在同一任务重新开始 multipart：%v", err)
	}
	if calls != 2 {
		t.Fatalf("multipart 调用次数 = %d，期望 2", calls)
	}
	if result.UploadResult != UploadResultMultipartUploaded ||
		result.CompletedRemoteFileId != "file-1" ||
		result.CompletedPickCode != "pick-1" ||
		result.UploadedBytes != 1024 ||
		result.TotalParts != 4 ||
		result.UploadedParts != 4 {
		t.Fatalf("上传结果 = %+v，期望第二次 multipart 成功结果", result)
	}

	gotSession, readErr := GetUploadSessionByUploadTaskId(task.ID)
	if readErr != nil {
		t.Fatalf("读取上传会话失败: %v", readErr)
	}
	if gotSession.Status != UploadSessionStatusCompleted ||
		gotSession.ResumeState != UploadResumeStateSessionExpiredRestarted ||
		gotSession.UploadId != "new-upload" ||
		gotSession.UploadedBytes != 1024 ||
		gotSession.UploadedParts != 4 ||
		gotSession.CompletedFileId != "file-1" ||
		gotSession.CompletedPickCode != "pick-1" {
		t.Fatalf("上传会话 = %+v，期望清空坏 checkpoint 后重新 multipart 并完成", gotSession)
	}
	var gotTask DbUploadTask
	if err := db.Db.First(&gotTask, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if gotTask.ResumeState != UploadResumeStateSessionExpiredRestarted || gotTask.UploadedBytes != 1024 {
		t.Fatalf("上传任务 = %+v，期望记录 session_expired_restarted 并同步新 multipart 进度", gotTask)
	}
}

func TestUpload115FilePersistsResultAndEnqueuesStrmTask(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := db.Db.AutoMigrate(&DirectoryUploadProcessedFile{}, &StrmGenerationTask{}); err != nil {
		t.Fatalf("迁移目录监控账本和 STRM 生成任务表失败: %v", err)
	}

	localPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(localPath, []byte("movie-content"), 0o644); err != nil {
		t.Fatalf("创建本地测试文件失败: %v", err)
	}
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("读取本地测试文件失败: %v", err)
	}
	task := &DbUploadTask{
		AccountId:         1,
		SyncPathId:        10,
		Status:            UploadStatusPending,
		Source:            UploadSourceDirectoryMonitor,
		SourceType:        SourceType115,
		LocalFullPath:     localPath,
		FileName:          "movie.mkv",
		FileSize:          13,
		LocalMtimeNs:      info.ModTime().UnixNano(),
		SourceFingerprint: BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano()),
		RemoteFullPath:    "/remote/movie.mkv",
		RemotePathId:      "100",
		Account:           &Account{BaseModel: BaseModel{ID: 1}, SourceType: SourceType115, Name: "115"},
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}

	setUpload115RunnerForTesting(t, fakeUpload115Runner{
		result: upload115TaskResult{
			UploadResult:          UploadResultMultipartUploaded,
			ResumeState:           UploadResumeStateResumedSession,
			UploadedBytes:         13,
			TotalParts:            2,
			UploadedParts:         2,
			CompletedRemoteFileId: "file-1",
			CompletedPickCode:     "pick-1",
			CompletedParentId:     "100",
			CompletedSha1:         "sha1",
			CompletedSize:         13,
			CompletedMtime:        123456,
		},
	})

	task.Upload()

	var gotTask DbUploadTask
	if err := db.Db.First(&gotTask, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if gotTask.Status != UploadStatusCompleted {
		t.Fatalf("任务状态 = %s，期望 completed，错误：%s", gotTask.Status.String(), gotTask.Error)
	}
	if gotTask.UploadResult != UploadResultMultipartUploaded ||
		gotTask.ResumeState != UploadResumeStateResumedSession ||
		gotTask.UploadedBytes != 13 ||
		gotTask.RemoteFileId != "file-1" ||
		gotTask.RemotePickCode != "pick-1" {
		t.Fatalf("上传结果字段 = %+v，期望保存完成结果和续传状态", gotTask)
	}

	var strmTask StrmGenerationTask
	if err := db.Db.Where("upload_task_id = ?", task.ID).First(&strmTask).Error; err != nil {
		t.Fatalf("上传成功后应创建 STRM 生成任务: %v", err)
	}
	if strmTask.Source != StrmGenerationSourceUploadCompleted ||
		strmTask.SyncPathId != task.SyncPathId ||
		strmTask.FileId != "file-1" ||
		strmTask.PickCode != "pick-1" {
		t.Fatalf("STRM 生成任务 = %+v，期望写入上传完成的远端定位信息", strmTask)
	}
	if strmTask.Path != "/remote" {
		t.Fatalf("STRM 生成任务 Path = %q，期望远端父目录 /remote", strmTask.Path)
	}
}

type fakeUpload115Runner struct {
	result upload115TaskResult
	err    error
}

func (runner fakeUpload115Runner) Upload(_ context.Context, _ *DbUploadTask, _ *v115open.OpenClient) (upload115TaskResult, error) {
	return runner.result, runner.err
}
