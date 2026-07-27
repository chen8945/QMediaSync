package models

import (
	"errors"
	"testing"

	"qmediasync/internal/db"
	openapiclient "qmediasync/openxpanapi"
)

func TestApplyBaiduUploadResponseOnlyUsesAvailableRemoteFields(t *testing.T) {
	fsID := int64(123456)
	md5 := "remote-md5"
	mtime := int32(1_700_000_000)
	task := &DbUploadTask{}
	gotMtime, hasMtime := task.applyBaiduUploadResponse(&openapiclient.Filecreateresponse{
		FsId:  &fsID,
		Md5:   &md5,
		Mtime: &mtime,
	})
	if task.RemoteFileId != "123456" || task.RemoteMd5 != "remote-md5" || !hasMtime || gotMtime != int64(mtime) {
		t.Fatalf("百度上传响应写入结果 = task=%+v mtime=%d hasMtime=%t", task, gotMtime, hasMtime)
	}

	zeroMtime := int32(0)
	zeroFsID := int64(654321)
	zeroMD5 := "zero-mtime-md5"
	task = &DbUploadTask{}
	gotMtime, hasMtime = task.applyBaiduUploadResponse(&openapiclient.Filecreateresponse{
		FsId:  &zeroFsID,
		Md5:   &zeroMD5,
		Mtime: &zeroMtime,
	})
	if task.RemoteFileId != "654321" || task.RemoteMd5 != "zero-mtime-md5" || hasMtime || gotMtime != 0 {
		t.Fatalf("零值修改时间响应 = task=%+v mtime=%d hasMtime=%t", task, gotMtime, hasMtime)
	}

	task = &DbUploadTask{RemoteFileId: "known-id", RemoteMd5: "known-md5"}
	gotMtime, hasMtime = task.applyBaiduUploadResponse(&openapiclient.Filecreateresponse{})
	if task.RemoteFileId != "known-id" || task.RemoteMd5 != "known-md5" || hasMtime || gotMtime != 0 {
		t.Fatalf("缺少可选字段时不应覆盖已有身份或伪造修改时间: task=%+v mtime=%d hasMtime=%t", task, gotMtime, hasMtime)
	}

	gotMtime, hasMtime = task.applyBaiduUploadResponse(nil)
	if hasMtime || gotMtime != 0 {
		t.Fatalf("空响应不应提供修改时间: mtime=%d hasMtime=%t", gotMtime, hasMtime)
	}
}

func TestAddUploadTaskFromSyncFileKeepsOnlyStableParentID(t *testing.T) {
	tests := []struct {
		name       string
		sourceType SourceType
		parentID   string
		want       string
	}{
		{
			name:       "115 保留父目录 ID",
			sourceType: SourceType115,
			parentID:   "115-parent-id",
			want:       "115-parent-id",
		},
		{
			name:       "OpenList 路径型父目录不写入 ID 字段",
			sourceType: SourceTypeOpenList,
			parentID:   "/remote/openlist",
			want:       "",
		},
		{
			name:       "百度路径型父目录不写入 ID 字段",
			sourceType: SourceTypeBaiduPan,
			parentID:   "/remote/baidu",
			want:       "",
		},
		{
			name:       "本地目标路径不写入 ID 字段",
			sourceType: SourceTypeLocal,
			parentID:   "/remote/local",
			want:       "",
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			file := &SyncFile{
				BaseModel:  BaseModel{ID: uint(index + 1)},
				SourceType: tt.sourceType,
				ParentId:   tt.parentID,
				Path:       "/remote/target",
				FileName:   "movie.mkv",
			}
			if err := AddUploadTaskFromSyncFile(file); err != nil {
				t.Fatalf("创建上传任务失败: %v", err)
			}

			var task DbUploadTask
			if err := db.Db.Where("sync_file_id = ?", file.ID).First(&task).Error; err != nil {
				t.Fatalf("读取上传任务失败: %v", err)
			}
			if task.RemotePathId != tt.want {
				t.Fatalf("remote_path_id = %q，期望 %q", task.RemotePathId, tt.want)
			}
		})
	}
}

func TestAddUploadTaskFromSyncFileKeepsOnlyStableReplacedFileID(t *testing.T) {
	tests := []struct {
		name       string
		file       SyncFile
		wantOldID  string
		wantFileID string
	}{
		{
			name: "115 使用文件 ID 作为旧文件 ID",
			file: SyncFile{
				SourceType: SourceType115,
				FileId:     "115-old-file-id",
			},
			wantOldID: "115-old-file-id",
		},
		{
			name: "百度使用 fs_id 作为旧文件 ID",
			file: SyncFile{
				SourceType: SourceTypeBaiduPan,
				FileId:     "/remote/baidu/movie.mkv",
				PickCode:   "baidu-old-fs-id",
			},
			wantOldID: "baidu-old-fs-id",
		},
		{
			name: "OpenList 使用对象 ID 作为旧文件 ID",
			file: SyncFile{
				SourceType:       SourceTypeOpenList,
				FileId:           "/remote/openlist/movie.mkv",
				OpenlistObjectId: "openlist-old-object-id",
			},
			wantOldID: "openlist-old-object-id",
		},
		{
			name: "本地路径不冒充旧文件 ID",
			file: SyncFile{
				SourceType: SourceTypeLocal,
				FileId:     "/remote/local/movie.mkv",
			},
			wantOldID: "",
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			file := tt.file
			file.BaseModel.ID = uint(index + 1)
			file.Path = "/remote/target"
			file.FileName = "movie.mkv"
			if err := AddUploadTaskFromSyncFile(&file); err != nil {
				t.Fatalf("创建上传任务失败: %v", err)
			}

			var task DbUploadTask
			if err := db.Db.Where("sync_file_id = ?", file.ID).First(&task).Error; err != nil {
				t.Fatalf("读取上传任务失败: %v", err)
			}
			if task.ReplacedRemoteFileId != tt.wantOldID {
				t.Fatalf("replaced_remote_file_id = %q，期望 %q", task.ReplacedRemoteFileId, tt.wantOldID)
			}
			if task.RemoteFileId != tt.wantFileID {
				t.Fatalf("remote_file_id = %q，期望 %q", task.RemoteFileId, tt.wantFileID)
			}
		})
	}
}

func TestAddUploadTaskFromSyncFileDeduplicatesActiveTasksWithinStorageScope(t *testing.T) {
	setupQueueStatusTestDB(t)

	newFile := func(id uint, sourceType SourceType, accountID, syncPathID uint) *SyncFile {
		return &SyncFile{
			BaseModel:  BaseModel{ID: id},
			SourceType: sourceType,
			AccountId:  accountID,
			SyncPathId: syncPathID,
			Path:       "/remote/target",
			FileName:   "movie.mkv",
		}
	}

	if err := AddUploadTaskFromSyncFile(newFile(1, SourceType115, 1, 1)); err != nil {
		t.Fatalf("创建基准上传任务失败: %v", err)
	}
	for _, file := range []*SyncFile{
		newFile(2, SourceTypeBaiduPan, 1, 1),
		newFile(3, SourceType115, 2, 1),
	} {
		if err := AddUploadTaskFromSyncFile(file); err != nil {
			t.Fatalf("不同存储范围不应互相去重: %v", err)
		}
	}

	if err := AddUploadTaskFromSyncFile(newFile(4, SourceType115, 1, 2)); err == nil {
		t.Fatal("同一实际远端目标不应因同步目录不同而重复入队")
	}

	if err := db.Db.Model(&DbUploadTask{}).Where("sync_file_id = ?", 1).Update("status", UploadStatusCompleted).Error; err != nil {
		t.Fatalf("完成基准上传任务失败: %v", err)
	}
	if err := AddUploadTaskFromSyncFile(newFile(5, SourceType115, 1, 2)); err != nil {
		t.Fatalf("历史完成任务不应阻止重新入队: %v", err)
	}
	if err := AddUploadTaskFromSyncFile(newFile(6, SourceType115, 1, 3)); err == nil {
		t.Fatal("活跃上传任务不应被历史完成任务掩盖")
	}
}

func TestCreateUploadTaskWithDBRejectsActiveDuplicateAtInsert(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := ensureActiveUploadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("创建活跃上传任务唯一索引失败: %v", err)
	}

	first := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusPending,
	}
	if err := createUploadTaskWithDB(db.Db, first); err != nil {
		t.Fatalf("创建基准上传任务失败: %v", err)
	}

	duplicate := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusPending,
	}
	if err := createUploadTaskWithDB(db.Db, duplicate); !errors.Is(err, errActiveUploadTaskExists) {
		t.Fatalf("活跃目标冲突错误 = %v，期望 errActiveUploadTaskExists", err)
	}

	if err := createUploadTaskWithDB(db.Db, &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusCompleted,
	}); err != nil {
		t.Fatalf("已完成任务应不受活跃唯一约束影响: %v", err)
	}
}

func TestRetryFailedUploadTasksSkipsTaskWithActiveTarget(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := ensureActiveUploadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("创建活跃上传任务唯一索引失败: %v", err)
	}

	failed := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusFailed,
		Error:          "保留失败原因",
	}
	active := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusPending,
	}
	unrelated := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/other.mkv",
		Status:         UploadStatusFailed,
		Error:          "应重试",
	}
	if err := db.Db.Create([]*DbUploadTask{failed, active, unrelated}).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}

	if err := RetryFailedUploadTasks(3); err != nil {
		t.Fatalf("重试失败上传任务失败: %v", err)
	}

	var gotFailed, gotUnrelated DbUploadTask
	if err := db.Db.First(&gotFailed, failed.ID).Error; err != nil {
		t.Fatalf("读取被跳过的失败任务失败: %v", err)
	}
	if gotFailed.Status != UploadStatusFailed || gotFailed.RetryCount != 0 || gotFailed.Error != "保留失败原因" {
		t.Fatalf("存在同目标活跃任务时失败记录不应被重试: %+v", gotFailed)
	}
	if err := db.Db.First(&gotUnrelated, unrelated.ID).Error; err != nil {
		t.Fatalf("读取无冲突失败任务失败: %v", err)
	}
	if gotUnrelated.Status != UploadStatusPending || gotUnrelated.RetryCount != 1 || gotUnrelated.Error != "" {
		t.Fatalf("无冲突失败任务应被重试: %+v", gotUnrelated)
	}
}
