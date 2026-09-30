package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func setupStrmWebhookControllerTest(t *testing.T) (*gin.Engine, string, *models.SyncPath) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	setupControllerTestDB(
		t,
		&models.User{},
		&models.ApiKey{},
		&models.Account{},
		&models.SyncPath{},
		&models.StrmGenerationTask{},
	)
	user := &models.User{Username: "admin", Password: "hashed"}
	if err := db.Db.Create(user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	_, rawKey, err := models.CreateAPIKey(user.ID, "strm webhook")
	if err != nil {
		t.Fatalf("创建 API Key 失败: %v", err)
	}
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	syncPath := &models.SyncPath{
		SourceType: models.SourceType115,
		AccountId:  account.ID,
		BaseCid:    "root",
		LocalPath:  t.TempDir(),
		RemotePath: "/remote",
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}
	router := gin.New()
	router.POST("/api/strm/webhook", StrmWebhook)
	return router, rawKey, syncPath
}

func setStrmWebhookFileDetailResolverForTesting(t *testing.T, resolver strmWebhookFileDetailResolver) {
	t.Helper()
	oldResolver := resolveStrmWebhookFileDetail
	resolveStrmWebhookFileDetail = resolver
	t.Cleanup(func() {
		resolveStrmWebhookFileDetail = oldResolver
	})
}

func setStrmWebhookFileIDDetailResolverForTesting(t *testing.T, resolver strmWebhookFileDetailByIDResolver) {
	t.Helper()
	oldResolver := resolveStrmWebhookFileDetailByID
	resolveStrmWebhookFileDetailByID = resolver
	t.Cleanup(func() {
		resolveStrmWebhookFileDetailByID = oldResolver
	})
}

func TestStrmWebhookRejectsExplicitNon115SyncPath(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	localSyncPath := &models.SyncPath{
		SourceType: models.SourceTypeLocal,
		AccountId:  syncPath.AccountId,
		BaseCid:    "local-root",
		LocalPath:  t.TempDir(),
		RemotePath: "/local",
	}
	if err := db.Db.Create(localSyncPath).Error; err != nil {
		t.Fatalf("创建非 115 同步目录失败: %v", err)
	}

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id":   localSyncPath.ID,
		"action":         "directory_scan",
		"directory_path": "/local",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "115") {
		t.Fatalf("非 115 同步目录应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookAuthSupportsHeaderAndQueryAPIKey(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:       fileID,
			PickCode:     "pick-" + fileID,
			FileName:     fileID + ".mkv",
			Path:         "/remote",
			FileSizeByte: 1024,
			Utime:        "123",
		}, nil
	})
	payload := map[string]any{
		"sync_path_id": syncPath.ID,
		"file_id":      "file-1",
		"pick_code":    "pick-1",
		"file_name":    "movie.mkv",
	}

	w := performStrmWebhookRequest(t, router, rawKey, "", payload)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("header API Key 响应异常: code=%d body=%s", w.Code, w.Body.String())
	}

	w = performStrmWebhookRequest(t, router, "", rawKey, map[string]any{
		"sync_path_id": syncPath.ID,
		"file_id":      "file-2",
		"pick_code":    "pick-2",
		"file_name":    "movie2.mkv",
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("query API Key 响应异常: code=%d body=%s", w.Code, w.Body.String())
	}

	w = performStrmWebhookRequest(t, router, "", "", payload)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"error_code":"AUTHENTICATION_REQUIRED"`) {
		t.Fatalf("缺少 API Key 状态码 = %d，期望 401，body=%s", w.Code, w.Body.String())
	}

	// 纯空白的 Key（URL 编码空格经查询参数传入）与缺失同等处理。
	w = performStrmWebhookRequest(t, router, "", "%20%20%20", payload)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"error_code":"AUTHENTICATION_REQUIRED"`) {
		t.Fatalf("空白 API Key 状态码 = %d，期望 401，body=%s", w.Code, w.Body.String())
	}

	w = performStrmWebhookRequest(t, router, "bad-key", "", payload)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"error_code":"AUTHENTICATION_INVALID"`) {
		t.Fatalf("无效 API Key 状态码 = %d，期望 401，body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookLogsAcceptedTask(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	var logBuf bytes.Buffer
	oldLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	t.Cleanup(func() {
		helpers.AppLogger = oldLogger
	})
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:       fileID,
			PickCode:     "pick-" + fileID,
			FileName:     "movie.mkv",
			Path:         "/remote",
			FileSizeByte: 1024,
			Utime:        "123",
		}, nil
	})

	resp := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id":  syncPath.ID,
		"file_id":       "file-log",
		"download_meta": true,
		"refresh_emby":  true,
	}))

	logOutput := logBuf.String()
	for _, want := range []string{
		"[STRM Webhook] 接收到 STRM 任务",
		"action=file",
		"sync_path_id=",
		"download_meta=true",
		"refresh_emby=true",
		"accepted=1",
		"failed=0",
		"task_ids=[",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s，响应：%+v", want, logOutput, resp)
		}
	}
	if strings.Contains(logOutput, rawKey) {
		t.Fatalf("日志不应包含 API Key，实际日志：%s", logOutput)
	}
}

func TestStrmWebhookValidatesFileLocator(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name:    "缺少同步目录",
			payload: map[string]any{"file_id": "file-1"},
			want:    "sync_path_id",
		},
		{
			name:    "缺少文件定位",
			payload: map[string]any{"sync_path_id": syncPath.ID, "file_name": "movie.mkv"},
			want:    "file_id 或 path + file_name",
		},
		{
			name:    "拒绝仅提供 pick_code",
			payload: map[string]any{"sync_path_id": syncPath.ID, "pick_code": "pick-1"},
			want:    "仅提供 pick_code 无法生成 STRM",
		},
		{
			name: "拒绝 local_path",
			payload: map[string]any{
				"sync_path_id": syncPath.ID,
				"file_id":      "file-1",
				"local_path":   "/tmp/evil.strm",
			},
			want: "local_path",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w := performStrmWebhookRequest(t, router, rawKey, "", tt.payload)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("响应异常: code=%d body=%s want=%s", w.Code, w.Body.String(), tt.want)
			}
		})
	}
}

func TestStrmWebhookEnqueuesFileTaskIdempotently(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:       fileID,
			PickCode:     "pick-1",
			FileName:     "movie.mkv",
			Path:         "/remote",
			FileSizeByte: 1024,
			Sha1:         "sha1",
			Utime:        "123",
			Paths:        []v115open.FileDetailPath{{FileId: "parent-1", Name: "remote"}},
		}, nil
	})
	payload := map[string]any{
		"sync_path_id": syncPath.ID,
		"file_id":      "file-1",
		"pick_code":    "pick-1",
		"parent_id":    "parent-1",
		"path":         "/remote",
		"file_name":    "movie.mkv",
		"file_size":    1024,
		"sha1":         "sha1",
		"mtime":        123,
	}
	w := performStrmWebhookRequest(t, router, rawKey, "", payload)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("首次入队响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	w = performStrmWebhookRequest(t, router, rawKey, "", payload)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("重复入队响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var total int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 STRM 任务失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("STRM 任务数量 = %d，期望 request_hash 去重为 1", total)
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if task.Source != models.StrmGenerationSourceWebhook ||
		task.TaskType != models.StrmGenerationTaskTypeFile ||
		task.SyncPathId != syncPath.ID ||
		task.FileId != "file-1" ||
		task.PickCode != "pick-1" {
		t.Fatalf("STRM 任务 = %+v，期望 webhook file 任务", task)
	}
}

func TestStrmWebhookRequestHashesUseShortV2Digest(t *testing.T) {
	longPath := "/remote/" + strings.Repeat("very-long-path-segment/", 20)
	longFileName := strings.Repeat("movie-", 60) + ".mkv"
	options := strmWebhookOptions{DownloadMeta: true, RefreshEmby: true}
	item := strmWebhookFileItem{
		FileID:   "file-long",
		PickCode: "pick-long",
		Path:     longPath,
		FileName: longFileName,
	}
	preparedFiles := []strmWebhookPreparedFile{{index: 0, item: item}}

	cases := []struct {
		name   string
		hash   string
		prefix string
	}{
		{
			name:   "单文件",
			hash:   strmWebhookFileRequestHash(10, 0, options, item),
			prefix: "webhook:file:v2:",
		},
		{
			name:   "批量父任务",
			hash:   strmWebhookBatchRequestHash(10, options, preparedFiles),
			prefix: "webhook:batch:v2:",
		},
		{
			name:   "批量子任务",
			hash:   strmWebhookBatchChildRequestHash(10, 20, options, 0, item),
			prefix: "webhook:batch_file:v2:",
		},
		{
			name:   "目录扫描",
			hash:   strmWebhookDirectoryRequestHash(10, options, "dir-long", longPath),
			prefix: "webhook:directory:v2:",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.hash) > 255 {
				t.Fatalf("request_hash 长度 = %d，期望不超过 255: %s", len(tt.hash), tt.hash)
			}
			if !strings.HasPrefix(tt.hash, tt.prefix) {
				t.Fatalf("request_hash = %s，期望前缀 %s", tt.hash, tt.prefix)
			}
			if strings.Contains(tt.hash, longPath) || strings.Contains(tt.hash, longFileName) {
				t.Fatalf("request_hash 不应包含明文长路径或文件名: %s", tt.hash)
			}
		})
	}
}

func TestStrmWebhookFileLongPathUsesShortRequestHashAndPreservesFields(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	longPath := "/remote/" + strings.Repeat("very-long-path-segment/", 20)
	longFileName := strings.Repeat("movie-", 60) + ".mkv"
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:       fileID,
			PickCode:     "pick-long",
			FileName:     longFileName,
			Path:         longPath,
			FileSizeByte: 1024,
			Utime:        "123",
		}, nil
	})

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"file_id":      "file-long",
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("长路径单文件入队响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取长路径 STRM 任务失败: %v", err)
	}
	if len(task.RequestHash) > 255 || !strings.HasPrefix(task.RequestHash, "webhook:file:v2:") {
		t.Fatalf("request_hash = %s，期望短 v2 哈希", task.RequestHash)
	}
	if task.Path != normalizeRemotePath(longPath) || task.FileName != longFileName {
		t.Fatalf("任务字段 path=%q file_name=%q，期望保存规范化路径并保留原始文件名", task.Path, task.FileName)
	}
}

func TestStrmWebhookFileRetryReusesLegacyActiveHash(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	item := strmWebhookFileItem{
		FileID:   "file-legacy-active",
		PickCode: "pick-legacy-active",
		Path:     "/remote/show",
		FileName: "legacy-active.mkv",
	}
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, _ string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   item.FileID,
			PickCode: item.PickCode,
			FileName: item.FileName,
			Path:     item.Path,
		}, nil
	})
	legacyTask := &models.StrmGenerationTask{
		Source:      models.StrmGenerationSourceWebhook,
		TaskType:    models.StrmGenerationTaskTypeFile,
		SyncPathId:  syncPath.ID,
		AccountId:   syncPath.AccountId,
		FileId:      item.FileID,
		PickCode:    item.PickCode,
		Path:        item.Path,
		FileName:    item.FileName,
		Status:      models.StrmGenerationStatusPending,
		RequestHash: legacyStrmWebhookFileRequestHash(syncPath.ID, 0, strmWebhookOptions{}, item),
	}
	if err := db.Db.Create(legacyTask).Error; err != nil {
		t.Fatalf("预置旧式单文件任务失败: %v", err)
	}

	resp := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"file_id":      item.FileID,
	}))
	if len(resp.Data.TaskIDs) != 1 || resp.Data.TaskIDs[0] != legacyTask.ID {
		t.Fatalf("旧式活跃单文件任务未被复用: task_ids=%v legacy_id=%d", resp.Data.TaskIDs, legacyTask.ID)
	}
	var total int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 STRM 任务失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("旧式活跃单文件任务复用后任务数量 = %d，期望 1", total)
	}
}

func TestStrmWebhookOptionsDefaultFalse(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{FileId: fileID, PickCode: "pick-1", FileName: "movie.mkv", Path: "/remote"}, nil
	})

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "file",
		"file_id":      "file-1",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if task.DownloadMeta || task.RefreshEmby {
		t.Fatalf("默认开关 = download_meta:%v refresh_emby:%v，期望 false/false", task.DownloadMeta, task.RefreshEmby)
	}
}

func TestStrmWebhookBatchCreatesParentAndChildrenWithOptions(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   fileID,
			PickCode: "pick-" + fileID,
			FileName: fileID + ".mkv",
			Path:     "/remote/show",
		}, nil
	})

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id":  syncPath.ID,
		"action":        "batch_files",
		"download_meta": true,
		"refresh_emby":  true,
		"items": []map[string]any{
			{"file_id": "file-1"},
			{"file_id": "file-2"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var parent models.StrmGenerationTask
	if err := db.Db.Where("task_type = ?", models.StrmGenerationTaskTypeBatchFiles).First(&parent).Error; err != nil {
		t.Fatalf("读取批量父任务失败: %v", err)
	}
	if parent.TotalItems != 2 || !parent.DownloadMeta || !parent.RefreshEmby {
		t.Fatalf("父任务 = %+v，期望 total=2 且开关为 true", parent)
	}
	var children []models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).Find(&children).Error; err != nil {
		t.Fatalf("读取子任务失败: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("子任务数量 = %d，期望 2", len(children))
	}
	for _, child := range children {
		if !child.DownloadMeta || !child.RefreshEmby {
			t.Fatalf("子任务未继承开关: %+v", child)
		}
	}
}

func TestStrmWebhookBatchFilesRetryReusesActiveParentAndChildren(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   fileID,
			PickCode: "pick-" + fileID,
			FileName: fileID + ".mkv",
			Path:     "/remote/show",
		}, nil
	})

	payload := map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{"file_id": "file-1"},
			{"file_id": "file-2"},
		},
	}
	first := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", payload))
	second := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", payload))
	if first.Data.AcceptedCount != 2 || second.Data.AcceptedCount != 2 {
		t.Fatalf("批量 accepted_count = first:%d second:%d，期望均为 2", first.Data.AcceptedCount, second.Data.AcceptedCount)
	}
	if !reflect.DeepEqual(second.Data.TaskIDs, first.Data.TaskIDs) {
		t.Fatalf("重复批量 task_ids = %v，期望稳定为 %v", second.Data.TaskIDs, first.Data.TaskIDs)
	}
	if len(first.Data.Results) != 2 || len(second.Data.Results) != 2 {
		t.Fatalf("批量 results 数量 = first:%d second:%d，期望均为 2", len(first.Data.Results), len(second.Data.Results))
	}
	for index := range first.Data.Results {
		if second.Data.Results[index].Index != first.Data.Results[index].Index ||
			second.Data.Results[index].TaskID != first.Data.Results[index].TaskID ||
			second.Data.Results[index].Accepted != first.Data.Results[index].Accepted {
			t.Fatalf("第 %d 项重复响应结果 = %+v，期望稳定为 %+v", index, second.Data.Results[index], first.Data.Results[index])
		}
	}

	var parentCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ?", models.StrmGenerationTaskTypeBatchFiles).
		Count(&parentCount).Error; err != nil {
		t.Fatalf("统计批量父任务失败: %v", err)
	}
	if parentCount != 1 {
		t.Fatalf("批量父任务数量 = %d，期望 1", parentCount)
	}

	var childCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ? AND parent_task_id > 0", models.StrmGenerationTaskTypeFile).
		Count(&childCount).Error; err != nil {
		t.Fatalf("统计批量子任务失败: %v", err)
	}
	if childCount != 2 {
		t.Fatalf("批量子任务数量 = %d，期望 2", childCount)
	}
}

type sqliteLockTestError int

func (e sqliteLockTestError) Error() string { return fmt.Sprintf("sqlite error %d", e) }
func (e sqliteLockTestError) Code() int     { return int(e) }

func TestIsRetryableSQLiteLockError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "无错误"},
		{name: "错误文本不能代替错误码", err: errors.New("database is locked")},
		{name: "零错误码", err: sqliteLockTestError(0)},
		{name: "busy", err: sqliteLockTestError(5), want: true},
		{name: "locked", err: sqliteLockTestError(6), want: true},
		{name: "busy 扩展码", err: sqliteLockTestError(5 | 2<<8), want: true},
		{name: "locked 扩展码", err: sqliteLockTestError(6 | 1<<8), want: true},
		{name: "其他扩展码", err: sqliteLockTestError(19 | 5<<8)},
		{name: "包装错误", err: fmt.Errorf("transaction: %w", sqliteLockTestError(5)), want: true},
		{name: "组合错误", err: errors.Join(errors.New("other"), sqliteLockTestError(6)), want: true},
		{name: "首个匹配错误优先", err: errors.Join(sqliteLockTestError(19), sqliteLockTestError(5))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableSQLiteLockError(tt.err); got != tt.want {
				t.Fatalf("isRetryableSQLiteLockError(%v) = %v，期望 %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestEnqueueStrmWebhookBatchConcurrentDuplicateRetriesSQLiteLock(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	setupConcurrentControllerTestDB(t, &models.StrmGenerationTask{})

	const callers = 2
	syncPath := &models.SyncPath{ID: 10, AccountId: 2}
	options := strmWebhookOptions{}
	preparedFiles := []strmWebhookPreparedFile{
		{index: 0, item: strmWebhookFileItem{FileID: "file-concurrent-1", FileName: "one.mkv", Path: "/remote"}},
		{index: 1, item: strmWebhookFileItem{FileID: "file-concurrent-2", FileName: "two.mkv", Path: "/remote"}},
	}

	ready := make(chan struct{}, callers)
	release := make(chan struct{})
	var createCount atomic.Int32
	callbackName := "qms:test_block_concurrent_batch_parent_create"
	if err := db.Db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		task, ok := tx.Statement.Dest.(*models.StrmGenerationTask)
		if !ok || task.TaskType != models.StrmGenerationTaskTypeBatchFiles || createCount.Add(1) > callers {
			return
		}
		ready <- struct{}{}
		<-release
	}); err != nil {
		t.Fatalf("注册并发测试 callback 失败: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = db.Db.Callback().Create().Remove(callbackName)
	})

	type outcome struct {
		results []strmWebhookItemResult
		err     error
	}
	outcomes := make(chan outcome, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			results := make([]strmWebhookItemResult, len(preparedFiles))
			err := enqueueStrmWebhookBatch(syncPath, options, preparedFiles, results)
			outcomes <- outcome{results: results, err: err}
		})
	}
	close(start)
	for i := range callers {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatalf("等待第 %d 个并发父任务创建进入 callback 超时", i+1)
		}
	}
	close(release)
	wg.Wait()
	close(outcomes)

	var firstResults []strmWebhookItemResult
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("并发批量入队失败: %v", result.err)
		}
		if len(result.results) != len(preparedFiles) {
			t.Fatalf("批量结果数量 = %d，期望 %d", len(result.results), len(preparedFiles))
		}
		if firstResults == nil {
			firstResults = result.results
			continue
		}
		if !reflect.DeepEqual(result.results, firstResults) {
			t.Fatalf("并发批量结果不一致: got %+v want %+v", result.results, firstResults)
		}
	}

	var parentCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ?", models.StrmGenerationTaskTypeBatchFiles).
		Count(&parentCount).Error; err != nil {
		t.Fatalf("统计批量父任务失败: %v", err)
	}
	if parentCount != 1 {
		t.Fatalf("批量父任务数量 = %d，期望 1", parentCount)
	}
	var childCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("parent_task_id > ?", 0).
		Count(&childCount).Error; err != nil {
		t.Fatalf("统计批量子任务失败: %v", err)
	}
	if childCount != int64(len(preparedFiles)) {
		t.Fatalf("批量子任务数量 = %d，期望 %d", childCount, len(preparedFiles))
	}
}

func TestStrmWebhookBatchFilesCreatesChildrenForDuplicateItems(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   fileID,
			PickCode: "pick-dup",
			FileName: "movie.mkv",
			Path:     "/remote/show",
		}, nil
	})

	resp := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{"file_id": "file-dup"},
			{"file_id": "file-dup"},
		},
	}))
	if resp.Data.AcceptedCount != 2 || len(resp.Data.TaskIDs) != 2 {
		t.Fatalf("重复 item 响应 = %+v，期望接受两个子任务", resp.Data)
	}
	if resp.Data.TaskIDs[0] == resp.Data.TaskIDs[1] {
		t.Fatalf("重复 item 不应复用同一个子任务 ID: %v", resp.Data.TaskIDs)
	}

	var parent models.StrmGenerationTask
	if err := db.Db.Where("task_type = ?", models.StrmGenerationTaskTypeBatchFiles).First(&parent).Error; err != nil {
		t.Fatalf("读取批量父任务失败: %v", err)
	}
	if parent.TotalItems != 2 {
		t.Fatalf("父任务 total_items = %d，期望 2", parent.TotalItems)
	}
	var children []models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).Order("id ASC").Find(&children).Error; err != nil {
		t.Fatalf("读取重复 item 子任务失败: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("重复 item 子任务数量 = %d，期望 2", len(children))
	}
	if children[0].RequestHash == children[1].RequestHash {
		t.Fatalf("重复 item 子任务 request_hash 不应相同: %+v", children)
	}

	if _, err := models.UpdateStrmGenerationParentProgress(parent.ID, models.StrmGenerationParentProgress{Accepted: 1}); err != nil {
		t.Fatalf("累计第一个子任务进度失败: %v", err)
	}
	updated, err := models.UpdateStrmGenerationParentProgress(parent.ID, models.StrmGenerationParentProgress{Accepted: 1})
	if err != nil {
		t.Fatalf("累计第二个子任务进度失败: %v", err)
	}
	if updated.Status != models.StrmGenerationStatusCompleted {
		t.Fatalf("重复 item 两个子任务完成后父任务 status = %s，期望 completed", updated.Status)
	}
}

func TestStrmWebhookBatchFilesRetryCompletesPartialParentAndKeepsInvalidResults(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   fileID,
			PickCode: "pick-" + fileID,
			FileName: fileID + ".mkv",
			Path:     "/remote/show",
		}, nil
	})
	payload := map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{"file_id": "file-1"},
			{"file_name": "missing.mkv"},
			{"file_id": "file-2"},
		},
	}
	first := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", payload))
	if first.Data.AcceptedCount != 2 || first.Data.FailedCount != 1 || len(first.Data.Results) != 3 {
		t.Fatalf("首次批量响应 = %+v，期望 2 个成功和 1 个失败", first.Data)
	}

	var removed models.StrmGenerationTask
	if err := db.Db.Where("file_id = ?", "file-2").First(&removed).Error; err != nil {
		t.Fatalf("读取待删除子任务失败: %v", err)
	}
	if err := db.Db.Delete(&removed).Error; err != nil {
		t.Fatalf("模拟缺失子任务失败: %v", err)
	}

	second := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", payload))
	if second.Data.AcceptedCount != 2 || second.Data.FailedCount != 1 || len(second.Data.Results) != 3 {
		t.Fatalf("补建缺失子任务后的响应 = %+v，期望保持原始批量响应形状", second.Data)
	}
	if !second.Data.Results[0].Accepted || second.Data.Results[0].Index != 0 || second.Data.Results[0].TaskID != first.Data.Results[0].TaskID {
		t.Fatalf("第 0 项结果 = %+v，期望复用首次子任务 %+v", second.Data.Results[0], first.Data.Results[0])
	}
	if second.Data.Results[1].Accepted || second.Data.Results[1].Index != 1 || !strings.Contains(second.Data.Results[1].Error, "file_id 或 path + file_name") {
		t.Fatalf("第 1 项非法结果 = %+v，期望保留原始非法项 index 和错误", second.Data.Results[1])
	}
	if !second.Data.Results[2].Accepted || second.Data.Results[2].Index != 2 || second.Data.Results[2].TaskID == 0 || second.Data.Results[2].TaskID == removed.ID {
		t.Fatalf("第 2 项补建结果 = %+v，期望创建新的缺失子任务", second.Data.Results[2])
	}

	var childCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ? AND parent_task_id > 0", models.StrmGenerationTaskTypeFile).
		Count(&childCount).Error; err != nil {
		t.Fatalf("统计补建后子任务失败: %v", err)
	}
	if childCount != 2 {
		t.Fatalf("补建后子任务数量 = %d，期望 2", childCount)
	}
}

func TestStrmWebhookBatchFilesChildCreateFailureRollsBackParentAndRetryCreatesAll(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   fileID,
			PickCode: "pick-" + fileID,
			FileName: fileID + ".mkv",
			Path:     "/remote/show",
		}, nil
	})

	options := strmWebhookOptions{}
	item := strmWebhookFileItem{
		FileID:   "file-blocked",
		PickCode: "pick-file-blocked",
		FileName: "file-blocked.mkv",
		Path:     "/remote/show",
	}
	preparedFiles := []strmWebhookPreparedFile{{index: 0, item: item}}
	failChildCreate := true
	callbackName := "qms:test_fail_batch_child_create"
	if err := db.Db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if !failChildCreate {
			return
		}
		task, ok := tx.Statement.Dest.(*models.StrmGenerationTask)
		if ok && task.TaskType == models.StrmGenerationTaskTypeFile && task.ParentTaskId > 0 {
			tx.AddError(errors.New("inject batch child create failure"))
		}
	}); err != nil {
		t.Fatalf("注册测试 callback 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Db.Callback().Create().Remove(callbackName)
	})

	payload := map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{"file_id": "file-blocked"},
		},
	}
	w := performStrmWebhookRequest(t, router, rawKey, "", payload)
	if w.Code == http.StatusOK {
		t.Fatalf("子任务创建失败不应返回 200: body=%s", w.Body.String())
	}

	var parentCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("request_hash = ?", strmWebhookBatchRequestHash(syncPath.ID, options, preparedFiles)).
		Count(&parentCount).Error; err != nil {
		t.Fatalf("统计失败后的父任务失败: %v", err)
	}
	if parentCount != 0 {
		t.Fatalf("子任务创建失败后父任务数量 = %d，期望事务回滚不落父任务", parentCount)
	}

	failChildCreate = false
	resp := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", payload))
	if resp.Data.AcceptedCount != 1 || len(resp.Data.TaskIDs) != 1 || len(resp.Data.Results) != 1 {
		t.Fatalf("子任务创建恢复后重试响应 = %+v，期望创建 1 个父子事务任务", resp.Data)
	}
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ?", models.StrmGenerationTaskTypeBatchFiles).
		Count(&parentCount).Error; err != nil {
		t.Fatalf("统计重试父任务失败: %v", err)
	}
	if parentCount != 1 {
		t.Fatalf("重试后父任务数量 = %d，期望 1", parentCount)
	}
}

func TestStrmWebhookBatchFilesRetryReusesLegacyChildHashOnceForDuplicateItems(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:   fileID,
			PickCode: "pick-legacy",
			FileName: "legacy.mkv",
			Path:     "/remote/show",
		}, nil
	})

	options := strmWebhookOptions{}
	item := strmWebhookFileItem{
		FileID:   "file-legacy",
		PickCode: "pick-legacy",
		FileName: "legacy.mkv",
		Path:     "/remote/show",
	}
	preparedFiles := []strmWebhookPreparedFile{
		{index: 0, item: item},
		{index: 1, item: item},
	}
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:      models.StrmGenerationSourceWebhook,
		TaskType:    models.StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  syncPath.ID,
		AccountId:   syncPath.AccountId,
		TotalItems:  2,
		Status:      models.StrmGenerationStatusWaitingChildren,
		RequestHash: strmWebhookBatchRequestHash(syncPath.ID, options, preparedFiles),
	})
	if err != nil {
		t.Fatalf("预置批量父任务失败: %v", err)
	}
	legacyChild := &models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceWebhook,
		TaskType:     models.StrmGenerationTaskTypeFile,
		ParentTaskId: parent.ID,
		SyncPathId:   syncPath.ID,
		AccountId:    syncPath.AccountId,
		FileId:       item.FileID,
		PickCode:     item.PickCode,
		Path:         item.Path,
		FileName:     item.FileName,
		Status:       models.StrmGenerationStatusPending,
		RequestHash:  legacyStrmWebhookFileRequestHash(syncPath.ID, parent.ID, options, item),
	}
	if err := db.Db.Create(legacyChild).Error; err != nil {
		t.Fatalf("预置旧式哈希子任务失败: %v", err)
	}

	resp := decodeStrmWebhookResponse(t, performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{"file_id": "file-legacy"},
			{"file_id": "file-legacy"},
		},
	}))
	if resp.Data.AcceptedCount != 2 || len(resp.Data.Results) != 2 {
		t.Fatalf("旧式哈希重复 item 响应 = %+v，期望 2 个成功结果", resp.Data)
	}
	if resp.Data.Results[0].TaskID != legacyChild.ID {
		t.Fatalf("第 0 项 task_id = %d，期望复用旧式子任务 %d", resp.Data.Results[0].TaskID, legacyChild.ID)
	}
	if resp.Data.Results[1].TaskID == 0 || resp.Data.Results[1].TaskID == legacyChild.ID {
		t.Fatalf("第 1 项 task_id = %d，期望创建新的批量子任务", resp.Data.Results[1].TaskID)
	}

	var childCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("parent_task_id = ?", parent.ID).
		Count(&childCount).Error; err != nil {
		t.Fatalf("统计旧式兼容子任务失败: %v", err)
	}
	if childCount != 2 {
		t.Fatalf("旧式兼容后子任务数量 = %d，期望 2", childCount)
	}
}

func TestStrmWebhookRejectsItemLevelOptions(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{
				"file_id":       "file-1",
				"download_meta": true,
				"refresh_emby":  false,
			},
		},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "items[] 不允许设置 download_meta 或 refresh_emby") {
		t.Fatalf("item 级开关应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookResolvesPathAndFileNameBeforeEnqueue(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileDetailResolverForTesting(t, func(_ context.Context, account *models.Account, fullPath string) (*v115open.FileDetail, error) {
		if account.ID != syncPath.AccountId {
			t.Fatalf("解析账号 ID = %d，期望 %d", account.ID, syncPath.AccountId)
		}
		if fullPath != "/remote/show/movie.mkv" {
			t.Fatalf("解析路径 = %s，期望 /remote/show/movie.mkv", fullPath)
		}
		return &v115open.FileDetail{
			FileId:       "file-path-1",
			PickCode:     "pick-path-1",
			FileName:     "movie.mkv",
			Path:         "/remote/show",
			FileSizeByte: 2048,
			Sha1:         "sha1-path",
			Utime:        "123456",
			Paths:        []v115open.FileDetailPath{{FileId: "parent-1", Name: "show"}},
		}, nil
	})

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"path":         "/remote/show",
		"file_name":    "movie.mkv",
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("path + file_name 入队响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if task.FileId != "file-path-1" ||
		task.PickCode != "pick-path-1" ||
		task.ParentId != "parent-1" ||
		task.Path != "/remote/show" ||
		task.FileName != "movie.mkv" ||
		task.FileSize != 2048 ||
		task.Sha1 != "sha1-path" ||
		task.Mtime != 123456 {
		t.Fatalf("STRM 任务 = %+v，期望 path + file_name 已解析为完整远端详情", task)
	}
}

func TestStrmWebhookAutoMatchesMostSpecificSyncPathByFilePath(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	nestedSyncPath := &models.SyncPath{
		SourceType: models.SourceType115,
		AccountId:  syncPath.AccountId,
		BaseCid:    "nested-root",
		LocalPath:  t.TempDir(),
		RemotePath: "/remote/show",
	}
	if err := db.Db.Create(nestedSyncPath).Error; err != nil {
		t.Fatalf("创建嵌套同步目录失败: %v", err)
	}
	setStrmWebhookFileDetailResolverForTesting(t, func(_ context.Context, account *models.Account, fullPath string) (*v115open.FileDetail, error) {
		if account.ID != nestedSyncPath.AccountId {
			t.Fatalf("解析账号 ID = %d，期望 %d", account.ID, nestedSyncPath.AccountId)
		}
		if fullPath != "/remote/show/S01/movie.mkv" {
			t.Fatalf("解析路径 = %s，期望 /remote/show/S01/movie.mkv", fullPath)
		}
		return &v115open.FileDetail{
			FileId:       "file-auto",
			PickCode:     "pick-auto",
			FileName:     "movie.mkv",
			Path:         "/remote/show/S01",
			FileSizeByte: 2048,
			Utime:        "123456",
			Paths:        []v115open.FileDetailPath{{FileId: "parent-auto", Name: "S01"}},
		}, nil
	})

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"action":    "file",
		"path":      "/remote/show/S01",
		"file_name": "movie.mkv",
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("自动匹配同步目录响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if task.SyncPathId != nestedSyncPath.ID || task.Path != "/remote/show/S01" {
		t.Fatalf("STRM 任务 = %+v，期望使用最具体同步目录 %d", task, nestedSyncPath.ID)
	}
}

func TestStrmWebhookRejectsFileIDOnlyWhenSyncPathIDMissing(t *testing.T) {
	router, rawKey, _ := setupStrmWebhookControllerTest(t)
	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"action":  "file",
		"file_id": "file-auto",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "path + file_name") {
		t.Fatalf("未提供 sync_path_id 时仅 file_id 应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookRejectsAmbiguousAutoMatchedSyncPath(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	duplicateSyncPath := &models.SyncPath{
		SourceType: models.SourceType115,
		AccountId:  syncPath.AccountId,
		BaseCid:    "duplicate-root",
		LocalPath:  t.TempDir(),
		RemotePath: "/remote",
	}
	if err := db.Db.Create(duplicateSyncPath).Error; err != nil {
		t.Fatalf("创建重复远端同步目录失败: %v", err)
	}

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"action":    "file",
		"path":      "/remote",
		"file_name": "movie.mkv",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "多个同步目录") {
		t.Fatalf("自动匹配到多个同步目录应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookRejectsFileIDOutsideSyncPath(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:       fileID,
			PickCode:     "pick-outside",
			FileName:     "outside.mkv",
			Path:         "/outside",
			FileSizeByte: 1024,
		}, nil
	})
	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id": syncPath.ID,
		"file_id":      "file-outside",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不在同步远端目录") {
		t.Fatalf("同步目录外 file_id 应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookBatchFilesReturnsItemResults(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		return &v115open.FileDetail{
			FileId:       fileID,
			PickCode:     "pick-1",
			FileName:     "movie.mkv",
			Path:         "/remote",
			FileSizeByte: 1024,
			Utime:        "123",
		}, nil
	})
	payload := map[string]any{
		"sync_path_id": syncPath.ID,
		"action":       "batch_files",
		"items": []map[string]any{
			{"file_id": "file-1", "pick_code": "pick-1", "file_name": "movie.mkv"},
			{"file_name": "missing.mkv"},
		},
	}
	w := performStrmWebhookRequest(t, router, rawKey, "", payload)
	body := w.Body.String()
	if w.Code != http.StatusOK ||
		!strings.Contains(body, `"accepted_count":1`) ||
		!strings.Contains(body, `"failed_count":1`) ||
		!strings.Contains(body, `"accepted":true`) ||
		!strings.Contains(body, `"accepted":false`) {
		t.Fatalf("批量响应异常: code=%d body=%s", w.Code, body)
	}
	var total int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 STRM 任务失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("STRM 任务数量 = %d，期望批量父任务 + 1 个合法子任务", total)
	}
	var childCount int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ? AND parent_task_id > 0", models.StrmGenerationTaskTypeFile).
		Count(&childCount).Error; err != nil {
		t.Fatalf("统计 STRM 子任务失败: %v", err)
	}
	if childCount != 1 {
		t.Fatalf("STRM 合法子任务数量 = %d，期望只入队 1 个合法项", childCount)
	}
}

func TestStrmWebhookRejectsAutoMatchedBatchAcrossSyncPaths(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	otherSyncPath := &models.SyncPath{
		SourceType: models.SourceType115,
		AccountId:  syncPath.AccountId,
		BaseCid:    "other-root",
		LocalPath:  t.TempDir(),
		RemotePath: "/other",
	}
	if err := db.Db.Create(otherSyncPath).Error; err != nil {
		t.Fatalf("创建其他同步目录失败: %v", err)
	}

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"action": "batch_files",
		"items": []map[string]any{
			{"path": "/remote", "file_name": "movie-a.mkv"},
			{"path": "/other", "file_name": "movie-b.mkv"},
		},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "多个同步目录") {
		t.Fatalf("跨同步目录批量请求应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookDirectoryScan(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		if fileID != "dir-1" {
			t.Fatalf("解析目录 ID = %s，期望 dir-1", fileID)
		}
		return &v115open.FileDetail{
			FileId:       "dir-1",
			FileName:     "show",
			FileCategory: v115open.TypeDir,
			Path:         "/remote",
		}, nil
	})
	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id":   syncPath.ID,
		"action":         "directory_scan",
		"download_meta":  true,
		"refresh_emby":   true,
		"directory_id":   "dir-1",
		"directory_path": "/remote/show",
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("目录扫描响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取目录扫描任务失败: %v", err)
	}
	if task.TaskType != models.StrmGenerationTaskTypeDirectoryScan ||
		task.DirectoryId != "dir-1" ||
		task.DirectoryPath != "/remote/show" {
		t.Fatalf("目录扫描任务 = %+v，期望 directory_scan", task)
	}
	if !task.DownloadMeta || !task.RefreshEmby {
		t.Fatalf("目录扫描任务开关 = download_meta:%v refresh_emby:%v，期望 true/true", task.DownloadMeta, task.RefreshEmby)
	}

	w = performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id":   syncPath.ID,
		"action":         "directory_scan",
		"directory_path": "/other/show",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不在同步远端目录") {
		t.Fatalf("非法目录响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookDirectoryScanRejectsMismatchedDirectoryIDAndPath(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	setStrmWebhookFileIDDetailResolverForTesting(t, func(_ context.Context, _ *models.Account, fileID string) (*v115open.FileDetail, error) {
		if fileID != "dir-actual" {
			t.Fatalf("解析目录 ID = %s，期望 dir-actual", fileID)
		}
		return &v115open.FileDetail{
			FileId:       "dir-actual",
			FileName:     "actual",
			FileCategory: v115open.TypeDir,
			Path:         "/remote",
		}, nil
	})

	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"sync_path_id":   syncPath.ID,
		"action":         "directory_scan",
		"directory_id":   "dir-actual",
		"directory_path": "/remote/show",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "directory_id") {
		t.Fatalf("目录 ID 和路径不一致应被拒绝: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrmWebhookAutoMatchesSyncPathByDirectoryPath(t *testing.T) {
	router, rawKey, syncPath := setupStrmWebhookControllerTest(t)
	w := performStrmWebhookRequest(t, router, rawKey, "", map[string]any{
		"action":         "directory_scan",
		"directory_path": "/remote/show",
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted_count":1`) {
		t.Fatalf("目录扫描自动匹配响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var task models.StrmGenerationTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取目录扫描任务失败: %v", err)
	}
	if task.SyncPathId != syncPath.ID || task.DirectoryPath != "/remote/show" {
		t.Fatalf("目录扫描任务 = %+v，期望自动匹配同步目录 %d", task, syncPath.ID)
	}
}

func performStrmWebhookRequest(t *testing.T, router *gin.Engine, headerKey string, queryKey string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	path := "/api/strm/webhook"
	if queryKey != "" {
		path += "?api_key=" + queryKey
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if headerKey != "" {
		req.Header.Set(apiKeyHeaderName, headerKey)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func decodeStrmWebhookResponse(t *testing.T, w *httptest.ResponseRecorder) APIResponse[strmWebhookResponse] {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("响应状态码 = %d，期望 200，body=%s", w.Code, w.Body.String())
	}
	var resp APIResponse[strmWebhookResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 STRM Webhook 响应失败: %v，body=%s", err, w.Body.String())
	}
	return resp
}
