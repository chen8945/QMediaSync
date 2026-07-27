package models

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"qmediasync/internal/db"
)

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (body *trackingReadCloser) Close() error {
	body.closed = true
	return nil
}

func TestCloseEmbyResponseBodyDrainsAndClosesResponse(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("response")}
	closeEmbyResponseBody(&http.Response{Body: body})

	if !body.closed {
		t.Fatal("Emby 响应体应被关闭")
	}
	remaining, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("读取已排空响应体失败: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("响应体未被排空: %q", remaining)
	}

	closeEmbyResponseBody(nil)
}

func TestAddDownloadTaskFromSyncFileSeparatesRemoteIdentity(t *testing.T) {
	tests := []struct {
		name  string
		file  SyncFile
		check func(*testing.T, DbDownloadTask)
	}{
		{
			name: "115 使用文件 ID、PickCode 与 SHA1",
			file: SyncFile{
				BaseModel:     BaseModel{ID: 1},
				SourceType:    SourceType115,
				FileId:        "115-file-id",
				PickCode:      "115-pick-code",
				Sha1:          "115-sha1",
				Path:          "/remote/115",
				FileName:      "movie.mkv",
				SyncPath:      &SyncPath{},
				LocalFilePath: "/library/movie.mkv",
			},
			check: func(t *testing.T, task DbDownloadTask) {
				t.Helper()
				if task.RemoteFileId != "115-file-id" || task.RemotePickCode != "115-pick-code" || task.RemoteSha1 != "115-sha1" || task.RemoteFullPath != "/remote/115/movie.mkv" {
					t.Fatalf("115 下载任务 = %+v", task)
				}
			},
		},
		{
			name: "百度使用 fs_id 与 MD5",
			file: SyncFile{
				BaseModel:     BaseModel{ID: 2},
				SourceType:    SourceTypeBaiduPan,
				FileId:        "/remote/baidu/movie.mkv",
				PickCode:      "baidu-fs-id",
				Sha1:          "baidu-md5",
				Path:          "/remote/baidu",
				FileName:      "movie.mkv",
				SyncPath:      &SyncPath{},
				LocalFilePath: "/library/movie.mkv",
			},
			check: func(t *testing.T, task DbDownloadTask) {
				t.Helper()
				if task.RemoteFileId != "baidu-fs-id" || task.RemotePickCode != "" || task.RemoteSha1 != "" || task.RemoteMd5 != "baidu-md5" {
					t.Fatalf("百度下载任务 = %+v", task)
				}
			},
		},
		{
			name: "OpenList 将直链隐藏并使用可选对象 ID 与哈希",
			file: SyncFile{
				BaseModel:        BaseModel{ID: 3},
				SourceType:       SourceTypeOpenList,
				FileId:           "/remote/openlist/movie.mkv",
				PickCode:         "https://openlist.example/d/remote/openlist/movie.mkv?sign=secret",
				OpenlistObjectId: "openlist-object-id",
				OpenlistSHA1:     "openlist-sha1",
				OpenlistMD5:      "openlist-md5",
				Path:             "/remote/openlist",
				FileName:         "movie.mkv",
				SyncPath:         &SyncPath{},
				LocalFilePath:    "/library/movie.mkv",
			},
			check: func(t *testing.T, task DbDownloadTask) {
				t.Helper()
				if task.RemoteFileId != "openlist-object-id" || task.RemotePickCode != "" || task.RemoteSha1 != "openlist-sha1" || task.RemoteMd5 != "openlist-md5" || task.RemoteDownloadUrl == "" {
					t.Fatalf("OpenList 下载任务 = %+v", task)
				}
				serialized, err := json.Marshal(task)
				if err != nil {
					t.Fatalf("序列化 OpenList 下载任务失败: %v", err)
				}
				for _, hiddenField := range []string{"remote_download_url", "emby_item_id", "local_source_path"} {
					if strings.Contains(string(serialized), hiddenField) {
						t.Fatalf("下载任务 JSON 不应包含 %s: %s", hiddenField, serialized)
					}
				}
			},
		},
		{
			name: "本地复制只使用隐藏源路径",
			file: SyncFile{
				BaseModel:     BaseModel{ID: 4},
				SourceType:    SourceTypeLocal,
				FileId:        "/source/movie.mkv",
				PickCode:      "/source/movie.mkv",
				Path:          "/source",
				FileName:      "movie.mkv",
				SyncPath:      &SyncPath{},
				LocalFilePath: "/library/movie.mkv",
			},
			check: func(t *testing.T, task DbDownloadTask) {
				t.Helper()
				if task.RemoteFileId != "" || task.RemotePath != "" || task.RemoteFullPath != "" || task.RemotePickCode != "" || task.LocalSourcePath != "/source/movie.mkv" {
					t.Fatalf("本地下载任务 = %+v", task)
				}
			},
		},
		{
			name: "不支持来源不伪造 115 PickCode 或 SHA1",
			file: SyncFile{
				BaseModel:     BaseModel{ID: 5},
				SourceType:    SourceType123,
				FileId:        "123-file-id",
				PickCode:      "not-a-115-pick-code",
				Sha1:          "unverified-hash",
				Path:          "/remote/123",
				FileName:      "movie.mkv",
				SyncPath:      &SyncPath{},
				LocalFilePath: "/library/movie.mkv",
			},
			check: func(t *testing.T, task DbDownloadTask) {
				t.Helper()
				if task.RemoteFileId != "123-file-id" || task.RemotePickCode != "" || task.RemoteSha1 != "" || task.RemoteMd5 != "" {
					t.Fatalf("不支持来源下载任务不应写入 115 身份或哈希字段: %+v", task)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			file := tt.file
			if err := AddDownloadTaskFromSyncFile(&file); err != nil {
				t.Fatalf("创建下载任务失败: %v", err)
			}
			var task DbDownloadTask
			if err := db.Db.Where("sync_file_id = ?", file.ID).First(&task).Error; err != nil {
				t.Fatalf("读取下载任务失败: %v", err)
			}
			tt.check(t, task)
		})
	}
}

func TestDownloadTasksWithoutStableIDDeduplicateByHiddenLocator(t *testing.T) {
	setupQueueStatusTestDB(t)
	openListFile := &SyncFile{
		BaseModel:     BaseModel{ID: 1},
		SourceType:    SourceTypeOpenList,
		FileId:        "/remote/movie.mkv",
		PickCode:      "https://openlist.example/d/remote/movie.mkv?sign=secret",
		Path:          "/remote",
		FileName:      "movie.mkv",
		SyncPath:      &SyncPath{},
		LocalFilePath: "/library/movie.mkv",
	}
	if err := AddDownloadTaskFromSyncFile(openListFile); err != nil {
		t.Fatalf("创建 OpenList 下载任务失败: %v", err)
	}
	if err := AddDownloadTaskFromSyncFile(openListFile); err == nil {
		t.Fatal("相同 OpenList 下载直链应被去重")
	}

	if err := AddDownloadTaskFromEmbyMedia("https://emby.example/extract", "emby-item-id", "movie.mkv"); err != nil {
		t.Fatalf("创建 Emby 下载任务失败: %v", err)
	}
	if err := AddDownloadTaskFromEmbyMedia("https://emby.example/extract", "emby-item-id", "movie.mkv"); err == nil {
		t.Fatal("相同 Emby 提取地址应被去重")
	}
}

func TestAddDownloadTaskFromSyncFileDeduplicatesWithinRemoteScope(t *testing.T) {
	setupQueueStatusTestDB(t)

	newFile := func(id uint, sourceType SourceType, accountID, syncPathID uint, localPath string) *SyncFile {
		file := &SyncFile{
			BaseModel:     BaseModel{ID: id},
			SourceType:    sourceType,
			AccountId:     accountID,
			SyncPathId:    syncPathID,
			FileId:        "shared-file-id",
			Path:          "/remote",
			FileName:      "movie.nfo",
			LocalFilePath: localPath,
			SyncPath:      &SyncPath{},
		}
		if sourceType == SourceTypeBaiduPan {
			file.FileId = "/remote/movie.nfo"
			file.PickCode = "shared-file-id"
		}
		return file
	}

	if err := AddDownloadTaskFromSyncFile(newFile(1, SourceType115, 1, 10, "/library-a/movie.nfo")); err != nil {
		t.Fatalf("创建基准下载任务失败: %v", err)
	}

	tests := []struct {
		name    string
		file    *SyncFile
		wantErr bool
	}{
		{
			name: "不同来源类型不冲突",
			file: newFile(2, SourceTypeBaiduPan, 1, 10, "/library-a/movie.nfo"),
		},
		{
			name: "不同账号不冲突",
			file: newFile(3, SourceType115, 2, 10, "/library-b/movie.nfo"),
		},
		{
			name: "不同同步目录不冲突",
			file: newFile(4, SourceType115, 1, 11, "/library-c/movie.nfo"),
		},
		{
			name:    "同一远端范围拒绝重复任务",
			file:    newFile(5, SourceType115, 1, 10, "/library-a/movie.nfo"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AddDownloadTaskFromSyncFile(tt.file)
			if (err != nil) != tt.wantErr {
				t.Fatalf("创建下载任务错误 = %v，wantErr = %t", err, tt.wantErr)
			}
		})
	}

	if err := db.Db.Model(&DbDownloadTask{}).Where("sync_file_id = ?", 1).Update("status", DownloadStatusCompleted).Error; err != nil {
		t.Fatalf("完成基准下载任务失败: %v", err)
	}
	if err := AddDownloadTaskFromSyncFile(newFile(9, SourceType115, 1, 10, "/library-a/movie.nfo")); err != nil {
		t.Fatalf("历史完成任务不应阻止重新入队: %v", err)
	}
	if err := AddDownloadTaskFromSyncFile(newFile(10, SourceType115, 1, 10, "/library-a/movie.nfo")); err == nil {
		t.Fatal("活跃下载任务不应被历史完成任务掩盖")
	}

	if err := AddDownloadTaskFromSyncFile(newFile(6, SourceType115, 1, 0, "/temporary-a/movie.nfo")); err != nil {
		t.Fatalf("创建零同步目录基准任务失败: %v", err)
	}
	if err := AddDownloadTaskFromSyncFile(newFile(7, SourceType115, 1, 0, "/temporary-b/movie.nfo")); err != nil {
		t.Fatalf("不同本地目标的零同步目录任务不应冲突: %v", err)
	}
	if err := AddDownloadTaskFromSyncFile(newFile(8, SourceType115, 1, 0, "/temporary-a/movie.nfo")); err == nil {
		t.Fatal("相同本地目标的零同步目录任务应去重")
	}
}

func TestCreateDownloadTaskWithDBRejectsActiveDuplicateAtInsert(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := ensureActiveDownloadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("创建活跃下载任务唯一索引失败: %v", err)
	}

	first := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusPending,
	}
	if err := createDownloadTaskWithDB(db.Db, first); err != nil {
		t.Fatalf("创建基准下载任务失败: %v", err)
	}

	duplicate := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusPending,
	}
	if err := createDownloadTaskWithDB(db.Db, duplicate); !errors.Is(err, errActiveDownloadTaskExists) {
		t.Fatalf("活跃下载目标冲突错误 = %v，期望 errActiveDownloadTaskExists", err)
	}

	if err := createDownloadTaskWithDB(db.Db, &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusCompleted,
	}); err != nil {
		t.Fatalf("已完成任务应不受活跃唯一约束影响: %v", err)
	}
}

func TestCreateDownloadTaskWithDBUsesSourceSpecificDeduplicationLocator(t *testing.T) {
	tests := []struct {
		name  string
		task  DbDownloadTask
		other DbDownloadTask
	}{
		{
			name: "OpenList 无对象 ID 使用隐藏直链",
			task: DbDownloadTask{
				Source:            DownloadSourceStrm,
				SourceType:        SourceTypeOpenList,
				AccountId:         1,
				SyncPathId:        10,
				RemoteDownloadUrl: "https://openlist.example/d/meta.nfo?sign=secret",
				Status:            DownloadStatusPending,
			},
			other: DbDownloadTask{
				Source:            DownloadSourceStrm,
				SourceType:        SourceTypeOpenList,
				AccountId:         1,
				SyncPathId:        10,
				RemoteDownloadUrl: "https://openlist.example/d/meta.nfo?sign=secret",
				Status:            DownloadStatusPending,
			},
		},
		{
			name: "本地复制使用隐藏源路径",
			task: DbDownloadTask{
				Source:          DownloadSourceLocalFile,
				SourceType:      SourceTypeLocal,
				AccountId:       1,
				SyncPathId:      10,
				LocalSourcePath: "/source/meta.nfo",
				Status:          DownloadStatusPending,
			},
			other: DbDownloadTask{
				Source:          DownloadSourceLocalFile,
				SourceType:      SourceTypeLocal,
				AccountId:       1,
				SyncPathId:      10,
				LocalSourcePath: "/source/meta.nfo",
				Status:          DownloadStatusPending,
			},
		},
		{
			name: "Emby 使用隐藏提取地址",
			task: DbDownloadTask{
				Source:            DownloadSourceEmbyMedia,
				SourceType:        SourceTypeEmbyMedia,
				RemoteDownloadUrl: "https://emby.example/extract",
				Status:            DownloadStatusPending,
			},
			other: DbDownloadTask{
				Source:            DownloadSourceEmbyMedia,
				SourceType:        SourceTypeEmbyMedia,
				RemoteDownloadUrl: "https://emby.example/extract",
				Status:            DownloadStatusPending,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			if err := ensureActiveDownloadTaskUniqueIndex(db.Db); err != nil {
				t.Fatalf("创建活跃下载任务唯一索引失败: %v", err)
			}
			if err := createDownloadTaskWithDB(db.Db, &tt.task); err != nil {
				t.Fatalf("创建基准下载任务失败: %v", err)
			}
			if err := createDownloadTaskWithDB(db.Db, &tt.other); !errors.Is(err, errActiveDownloadTaskExists) {
				t.Fatalf("活跃下载目标冲突错误 = %v，期望 errActiveDownloadTaskExists", err)
			}
		})
	}
}

func TestCreateDownloadTaskWithDBSeparatesTemporaryLocalTargets(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := ensureActiveDownloadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("创建活跃下载任务唯一索引失败: %v", err)
	}

	newTask := func(localFullPath string) *DbDownloadTask {
		return &DbDownloadTask{
			Source:        DownloadSourceStrm,
			SourceType:    SourceType115,
			AccountId:     1,
			RemoteFileId:  "115-file-id",
			LocalFullPath: localFullPath,
			Status:        DownloadStatusPending,
		}
	}
	if err := createDownloadTaskWithDB(db.Db, newTask("/temporary-a/meta.nfo")); err != nil {
		t.Fatalf("创建临时下载任务失败: %v", err)
	}
	if err := createDownloadTaskWithDB(db.Db, newTask("/temporary-b/meta.nfo")); err != nil {
		t.Fatalf("不同本地目标的临时下载任务不应冲突: %v", err)
	}
	if err := createDownloadTaskWithDB(db.Db, newTask("/temporary-a/meta.nfo")); !errors.Is(err, errActiveDownloadTaskExists) {
		t.Fatalf("相同本地目标的临时下载任务冲突错误 = %v，期望 errActiveDownloadTaskExists", err)
	}
}

func TestCreateDownloadTaskWithDBAllowsTasksWithoutReliableLocator(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := ensureActiveDownloadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("创建活跃下载任务唯一索引失败: %v", err)
	}

	for i := 0; i < 2; i++ {
		task := &DbDownloadTask{
			Source:     DownloadSourceStrm,
			SourceType: SourceType115,
			AccountId:  1,
			SyncPathId: 10,
			Status:     DownloadStatusPending,
		}
		if err := createDownloadTaskWithDB(db.Db, task); err != nil {
			t.Fatalf("定位不足的历史下载任务不应被误合并: %v", err)
		}
		if task.DedupScopeHash != "" || task.DedupLocatorHash != "" {
			t.Fatalf("定位不足的下载任务不应写入去重键: %+v", task)
		}
	}
}

func TestRetryFailedDownloadTasksSkipsTaskWithActiveTarget(t *testing.T) {
	setupQueueStatusTestDB(t)
	if err := ensureActiveDownloadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("创建活跃下载任务唯一索引失败: %v", err)
	}

	failed := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusFailed,
		Error:        "保留失败原因",
	}
	active := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusPending,
	}
	unrelated := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "other-file-id",
		Status:       DownloadStatusFailed,
		Error:        "应重试",
	}
	for _, task := range []*DbDownloadTask{failed, active, unrelated} {
		if err := createDownloadTaskWithDB(db.Db, task); err != nil {
			t.Fatalf("创建下载任务失败: %v", err)
		}
	}

	if err := RetryFailedDownloadTasks(3); err != nil {
		t.Fatalf("重试失败下载任务失败: %v", err)
	}

	var gotFailed, gotUnrelated DbDownloadTask
	if err := db.Db.First(&gotFailed, failed.ID).Error; err != nil {
		t.Fatalf("读取被跳过的失败任务失败: %v", err)
	}
	if gotFailed.Status != DownloadStatusFailed || gotFailed.RetryCount != 0 || gotFailed.Error != "保留失败原因" {
		t.Fatalf("存在同目标活跃任务时失败记录不应被重试: %+v", gotFailed)
	}
	if err := db.Db.First(&gotUnrelated, unrelated.ID).Error; err != nil {
		t.Fatalf("读取无冲突失败任务失败: %v", err)
	}
	if gotUnrelated.Status != DownloadStatusPending || gotUnrelated.RetryCount != 1 || gotUnrelated.Error != "" {
		t.Fatalf("无冲突失败任务应被重试: %+v", gotUnrelated)
	}
}
