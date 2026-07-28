package models

import (
	"bytes"
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

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/taskgate"
)

func setupEmbyRefreshTestDB(t *testing.T) {
	t.Helper()
	resetEmbyRefreshTimerStateForTest()
	drainEmbyRefreshCheckChan()
	t.Cleanup(resetEmbyRefreshTimerStateForTest)
	t.Cleanup(drainEmbyRefreshCheckChan)
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(
		&EmbyConfig{},
		&EmbyMediaItem{},
		&EmbyMediaSyncFile{},
		&EmbyLibrarySyncPath{},
		&EmbyLibraryRefreshTask{},
		&SyncFile{},
		&DbDownloadTask{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	GlobalEmbyConfig = &EmbyConfig{
		EmbyUrl:              "http://emby.local:8096",
		EmbyApiKey:           "test-key",
		EnableRefreshLibrary: 1,
	}
	IsStrmSyncTaskActiveFunc = nil
	embyRefreshDownloadEventBatch.mutex.Lock()
	embyRefreshDownloadEventBatch.syncPathIds = make(map[uint]struct{})
	embyRefreshDownloadEventBatch.syncFileIds = make(map[uint]struct{})
	embyRefreshDownloadEventBatch.mutex.Unlock()
}

func TestCreateEmbyLibraryRefreshTaskIfAbsentDoesNotAbortAfterDuplicate(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	created, err := createEmbyLibraryRefreshTaskIfAbsent(db.Db, &EmbyLibraryRefreshTask{TaskKey: "item:1", LibraryId: "lib-movie"})
	if err != nil {
		t.Fatalf("首次创建刷新任务失败: %v", err)
	}
	if !created {
		t.Fatal("首次创建应返回 created")
	}
	created, err = createEmbyLibraryRefreshTaskIfAbsent(db.Db, &EmbyLibraryRefreshTask{TaskKey: "item:1", LibraryId: "lib-movie"})
	if err != nil {
		t.Fatalf("重复创建刷新任务失败: %v", err)
	}
	if created {
		t.Fatal("重复创建不应新增刷新任务")
	}
	var total int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("task_key = ?", "item:1").Count(&total).Error; err != nil {
		t.Fatalf("统计刷新任务失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("刷新任务数量 = %d，期望 1", total)
	}
}

func TestRequestEmbyLibraryRefreshBySyncPathIdUsesResolvedItemTargets(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	const syncPathID = 10
	syncFile := &SyncFile{SyncPathId: syncPathID, FileId: "remote-file", PickCode: "pick-code", IsVideo: true}
	if err := db.Db.Create(syncFile).Error; err != nil {
		t.Fatalf("创建同步文件失败: %v", err)
	}
	item := &EmbyMediaItem{ItemId: "emby-movie", ItemIdInt: 1001, Type: "Movie", Name: "电影", LibraryId: "lib-movie"}
	if err := db.Db.Create(item).Error; err != nil {
		t.Fatalf("创建 Emby 条目失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: syncPathID, SyncFileId: syncFile.ID, EmbyItemId: uint(item.ItemIdInt), PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建 Emby 文件关联失败: %v", err)
	}
	if err := db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影库", SyncPathId: syncPathID}).Error; err != nil {
		t.Fatalf("创建媒体库关联失败: %v", err)
	}

	if err := RequestEmbyLibraryRefreshBySyncPathId(syncPathID); err != nil {
		t.Fatalf("提交 Emby 刷新任务失败: %v", err)
	}

	var itemTask EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("emby-movie")).First(&itemTask).Error; err != nil {
		t.Fatalf("应创建 item 定向刷新任务: %v", err)
	}
	if itemTask.TargetType != EmbyLibraryRefreshTargetTypeItem || itemTask.FallbackLibraryId != "lib-movie" {
		t.Fatalf("item 刷新任务 = %+v，期望使用真实媒体库", itemTask)
	}
	var libraryTaskCount int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("library_id = ? AND target_type = ?", "lib-movie", EmbyLibraryRefreshTargetTypeLibrary).
		Count(&libraryTaskCount).Error; err != nil {
		t.Fatalf("统计纯媒体库任务失败: %v", err)
	}
	if libraryTaskCount != 0 {
		t.Fatalf("已有 item 目标时不应退化为纯媒体库任务，数量=%d", libraryTaskCount)
	}
}

func drainEmbyRefreshCheckChan() {
	for {
		select {
		case <-embyRefreshCheckChan:
		default:
			return
		}
	}
}

func assertScheduledEmbyRefreshCheckAt(t *testing.T, want int64) {
	t.Helper()
	embyRefreshTimerState.Lock()
	defer embyRefreshTimerState.Unlock()

	if embyRefreshTimerState.timer == nil {
		t.Fatalf("Emby 刷新调度 timer 未设置，期望 nextCheckAt=%d", want)
	}
	if embyRefreshTimerState.nextCheckAt != want {
		t.Fatalf("Emby 刷新 nextCheckAt=%d，期望 %d", embyRefreshTimerState.nextCheckAt, want)
	}
}

func assertNoScheduledEmbyRefreshCheck(t *testing.T) {
	t.Helper()
	embyRefreshTimerState.Lock()
	defer embyRefreshTimerState.Unlock()

	if embyRefreshTimerState.timer != nil || embyRefreshTimerState.nextCheckAt != 0 {
		t.Fatalf("不应设置 Emby 刷新调度 timer，timer=%v nextCheckAt=%d", embyRefreshTimerState.timer, embyRefreshTimerState.nextCheckAt)
	}
}

func TestMergeSyncPathIds(t *testing.T) {
	got := mergeSyncPathIds([]uint{3, 1, 3}, []uint{2, 1})
	want := []uint{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("合并后的 sync_path_id = %v，期望 %v", got, want)
	}
}

func TestEmbyLibraryRefreshTaskSyncPathIds(t *testing.T) {
	task := &EmbyLibraryRefreshTask{}
	task.SetSyncPathIds([]uint{5, 2})
	got := task.GetSyncPathIds()
	want := []uint{2, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("解析 sync_path_ids = %v，期望 %v", got, want)
	}
	if task.SyncPathIdsStr != "[2,5]" {
		t.Fatalf("sync_path_ids 字符串 = %s，期望 [2,5]", task.SyncPathIdsStr)
	}
}

func TestRefreshTaskDefaultTimings(t *testing.T) {
	now := time.Now().Unix()
	task := newPendingEmbyLibraryRefreshTask("lib-1", "电影", []uint{1}, now)
	if task.RefreshAfterAt != now+DefaultEmbyRefreshDebounceSeconds {
		t.Fatalf("refresh_after_at = %d，期望 %d", task.RefreshAfterAt, now+DefaultEmbyRefreshDebounceSeconds)
	}
	if task.DeadlineAt != now+DefaultEmbyRefreshMaxWaitSeconds {
		t.Fatalf("deadline_at = %d，期望 %d", task.DeadlineAt, now+DefaultEmbyRefreshMaxWaitSeconds)
	}
	if DefaultEmbyRefreshDownloadEventBatchSeconds != 5 {
		t.Fatalf("下载事件批量处理间隔 = %d，期望 5", DefaultEmbyRefreshDownloadEventBatchSeconds)
	}
}

func TestRequestEmbyLibraryRefreshBySyncPathCreatesTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})

	if err := RequestEmbyLibraryRefreshBySyncPathId(10); err != nil {
		t.Fatalf("提交刷新任务失败: %v", err)
	}

	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-movie").First(&task).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if task.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("刷新任务状态 = %s，期望 pending", task.Status)
	}
	if !reflect.DeepEqual(task.GetSyncPathIds(), []uint{10}) {
		t.Fatalf("sync_path_ids = %v，期望 [10]", task.GetSyncPathIds())
	}
}

func TestRequestEmbyLibraryRefreshMergesSameLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 11})

	if err := RequestEmbyLibraryRefreshBySyncPathId(10); err != nil {
		t.Fatalf("第一次提交刷新任务失败: %v", err)
	}
	if err := RequestEmbyLibraryRefreshBySyncPathId(11); err != nil {
		t.Fatalf("第二次提交刷新任务失败: %v", err)
	}

	var tasks []EmbyLibraryRefreshTask
	db.Db.Find(&tasks)
	if len(tasks) != 1 {
		t.Fatalf("同一媒体库应合并为 1 条任务，实际 %d", len(tasks))
	}
	if !reflect.DeepEqual(tasks[0].GetSyncPathIds(), []uint{10, 11}) {
		t.Fatalf("sync_path_ids = %v，期望 [10 11]", tasks[0].GetSyncPathIds())
	}
}

func TestRequestEmbyLibraryRefreshSkipsDisabledConfig(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	GlobalEmbyConfig.EnableRefreshLibrary = 0
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})

	if err := RequestEmbyLibraryRefreshBySyncPathId(10); err != nil {
		t.Fatalf("关闭刷新时应安静跳过，实际错误: %v", err)
	}

	var total int64
	db.Db.Model(&EmbyLibraryRefreshTask{}).Count(&total)
	if total != 0 {
		t.Fatalf("关闭刷新时不应创建任务，实际 %d", total)
	}
}

func setEmbyRefreshTestLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{
		Logger: log.New(&buf, "", 0),
	}
	return &buf
}

func TestCheckPendingEmbyLibraryRefreshTasksLogsDisabledTransitionOnce(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	buf := setEmbyRefreshTestLogger(t)
	resetEmbyRefreshScannerConfigStateForTest()

	GlobalEmbyConfig.EnableRefreshLibrary = 0
	CheckPendingEmbyLibraryRefreshTasks()
	CheckPendingEmbyLibraryRefreshTasks()
	if strings.Contains(buf.String(), "暂停待刷新任务扫描") {
		t.Fatalf("启动时未启用刷新不应写扫描暂停日志，实际日志：%s", buf.String())
	}

	GlobalEmbyConfig.EnableRefreshLibrary = 1
	CheckPendingEmbyLibraryRefreshTasks()
	GlobalEmbyConfig.EnableRefreshLibrary = 0
	CheckPendingEmbyLibraryRefreshTasks()
	CheckPendingEmbyLibraryRefreshTasks()
	if got := strings.Count(buf.String(), "暂停待刷新任务扫描"); got != 1 {
		t.Fatalf("启用变为未启用时应只写 1 条暂停日志，实际 %d，日志：%s", got, buf.String())
	}

	GlobalEmbyConfig.EnableRefreshLibrary = 1
	CheckPendingEmbyLibraryRefreshTasks()
	GlobalEmbyConfig.EnableRefreshLibrary = 0
	CheckPendingEmbyLibraryRefreshTasks()
	if got := strings.Count(buf.String(), "暂停待刷新任务扫描"); got != 2 {
		t.Fatalf("重新启用后再次关闭应再写 1 条暂停日志，实际 %d，日志：%s", got, buf.String())
	}
}

func TestRequestEmbyLibraryRefreshSkipsUnlinkedSyncPath(t *testing.T) {
	setupEmbyRefreshTestDB(t)

	if err := RequestEmbyLibraryRefreshBySyncPathId(99); err != nil {
		t.Fatalf("无媒体库关联时应安静跳过，实际错误: %v", err)
	}

	var total int64
	db.Db.Model(&EmbyLibraryRefreshTask{}).Count(&total)
	if total != 0 {
		t.Fatalf("无关联时不应创建任务，实际 %d", total)
	}
}

func TestTaskAdmissionGatePreventsEmbyRefreshSubmissionAndExecution(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	if err := RequestEmbyLibraryRefreshBySyncPathId(10); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("RequestEmbyLibraryRefreshBySyncPathId() error = %v，期望任务准入被拒绝", err)
	}
	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{{TargetType: EmbyRefreshTargetTypeLibrary}}); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("RequestEmbyRefreshTargets() error = %v，期望任务准入被拒绝", err)
	}
	if err := RequestEmbyRefreshBySyncFile(&SyncFile{SyncPathId: 10}); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("RequestEmbyRefreshBySyncFile() error = %v，期望任务准入被拒绝", err)
	}

	var total int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 Emby 刷新任务失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("任务准入关闭时不应创建 Emby 刷新任务，实际数量 = %d", total)
	}

	taskgate.AllowNewTasks()
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("library-1", "媒体库", []uint{10}, now-DefaultEmbyRefreshDebounceSeconds-1)
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建待刷新任务失败: %v", err)
	}
	taskgate.BlockNewTasks()
	CheckPendingEmbyLibraryRefreshTasks()

	var stored EmbyLibraryRefreshTask
	if err := db.Db.First(&stored, task.ID).Error; err != nil {
		t.Fatalf("读取待刷新任务失败: %v", err)
	}
	if stored.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("任务准入关闭时不应领取 Emby 刷新任务，status = %s", stored.Status)
	}
}

func TestTaskAdmissionGateWaitsForEmbyRefreshTaskCommit(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	if err := db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "library-1", LibraryName: "媒体库", SyncPathId: 10}).Error; err != nil {
		t.Fatalf("创建媒体库关联失败: %v", err)
	}

	assertTaskAdmissionWaitsForCreateCommit(t, func() error {
		return RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{{TargetType: EmbyRefreshTargetTypeLibrary}})
	})
}

func TestCountRunningEmbyLibraryRefreshTasks(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	for index, status := range []string{
		EmbyLibraryRefreshStatusPending,
		EmbyLibraryRefreshStatusRefreshing,
		EmbyLibraryRefreshStatusCompleted,
	} {
		task := &EmbyLibraryRefreshTask{
			TaskKey: fmt.Sprintf("task-%d", index),
			Status:  status,
		}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建 %s 刷新任务失败: %v", status, err)
		}
	}

	if got := CountRunningEmbyLibraryRefreshTasks(); got != 1 {
		t.Fatalf("CountRunningEmbyLibraryRefreshTasks() = %d，期望 1", got)
	}
}

func TestRefreshTaskWaitsForRelatedDownloadsOnly(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&SyncFile{BaseModel: BaseModel{ID: 1}, SyncPathId: 10})
	db.Db.Create(&SyncFile{BaseModel: BaseModel{ID: 2}, SyncPathId: 20})
	db.Db.Create(&DbDownloadTask{SyncFileId: 1, Status: DownloadStatusPending})
	db.Db.Create(&DbDownloadTask{SyncFileId: 2, Status: DownloadStatusPending})

	count, err := CountActiveDownloadTasksBySyncPathIds([]uint{10})
	if err != nil {
		t.Fatalf("统计下载任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("只应统计当前媒体库相关下载任务，实际 %d", count)
	}
}

func TestRefreshTaskWaitsForDownloadTaskWithSyncPathId(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&DbDownloadTask{SyncPathId: 10, Status: DownloadStatusPending})
	db.Db.Create(&DbDownloadTask{SyncPathId: 20, Status: DownloadStatusPending})

	count, err := CountActiveDownloadTasksBySyncPathIds([]uint{10})
	if err != nil {
		t.Fatalf("统计下载任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("应按 sync_path_id 统计当前同步目录下载任务，实际 %d", count)
	}
}

func TestRefreshTaskKeepsSyncFileFallbackForOldDownloadTasks(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&SyncFile{BaseModel: BaseModel{ID: 1}, SyncPathId: 10})
	db.Db.Create(&DbDownloadTask{SyncFileId: 1, Status: DownloadStatusPending})

	count, err := CountActiveDownloadTasksBySyncPathIds([]uint{10})
	if err != nil {
		t.Fatalf("统计旧下载任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("旧下载任务应继续通过 sync_file_id 兼容统计，实际 %d", count)
	}
}

func TestRefreshTaskKeepsSyncFileFallbackForNullSyncPathIDOldDownloadTasks(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&SyncFile{BaseModel: BaseModel{ID: 1}, SyncPathId: 10})
	if err := db.Db.Exec("INSERT INTO db_download_tasks (sync_file_id, sync_path_id, status) VALUES (?, NULL, ?)", 1, DownloadStatusPending).Error; err != nil {
		t.Fatalf("插入 NULL sync_path_id 旧下载任务失败: %v", err)
	}

	count, err := CountActiveDownloadTasksBySyncPathIds([]uint{10})
	if err != nil {
		t.Fatalf("统计 NULL sync_path_id 旧下载任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("NULL sync_path_id 旧下载任务应继续通过 sync_file_id 兼容统计，实际 %d", count)
	}
}

func TestRefreshTaskWaitsForActiveSyncTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	IsStrmSyncTaskActiveFunc = func(syncPathId uint) bool {
		return syncPathId == 10
	}

	if !HasActiveStrmSyncTask([]uint{10, 11}) {
		t.Fatal("存在活跃同步任务时应等待")
	}
	if HasActiveStrmSyncTask([]uint{11}) {
		t.Fatal("无活跃同步任务时不应等待")
	}
}

func TestRefreshTaskWaitsForSameLibraryQueuedSyncPath(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 11})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	db.Db.Create(task)
	IsStrmSyncTaskActiveFunc = func(syncPathId uint) bool {
		return syncPathId == 11
	}

	ready, reason, err := IsEmbyLibraryRefreshTaskReady(task, now)
	if err != nil {
		t.Fatalf("判断刷新任务失败: %v", err)
	}
	if ready {
		t.Fatalf("同一媒体库还有同步目录在队列中时不应刷新")
	}
	if reason != "sync_running" {
		t.Fatalf("等待原因 = %s，期望 sync_running", reason)
	}
}

func TestRefreshTaskWaitsForSameLibraryDownloadOutsideTaskSyncPaths(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 11})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	db.Db.Create(task)
	db.Db.Create(&DbDownloadTask{SyncPathId: 11, Status: DownloadStatusPending})

	ready, reason, err := IsEmbyLibraryRefreshTaskReady(task, now)
	if err != nil {
		t.Fatalf("判断刷新任务失败: %v", err)
	}
	if ready {
		t.Fatalf("同一媒体库其他同步目录还有下载任务时不应刷新")
	}
	if reason != "download_running" {
		t.Fatalf("等待原因 = %s，期望 download_running", reason)
	}
}

func TestRefreshTaskItemUsesFallbackLibraryForWaitSyncPaths(t *testing.T) {
	tests := []struct {
		name       string
		arrange    func()
		wantReason string
	}{
		{
			name: "同媒体库其他同步目录有活跃同步任务",
			arrange: func() {
				IsStrmSyncTaskActiveFunc = func(syncPathId uint) bool {
					return syncPathId == 11
				}
			},
			wantReason: "sync_running",
		},
		{
			name: "同媒体库其他同步目录有下载任务",
			arrange: func() {
				db.Db.Create(&DbDownloadTask{SyncPathId: 11, Status: DownloadStatusPending})
			},
			wantReason: "download_running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
			db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 11})
			task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
				TargetType:          EmbyRefreshTargetTypeItem,
				ItemID:              "301",
				ItemName:            "第一季",
				ItemType:            "Season",
				Recursive:           true,
				FallbackLibraryId:   "lib-tv",
				FallbackLibraryName: "剧集",
			}, 10, now-100)
			task.RefreshAfterAt = now - 1
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatalf("创建 item 刷新任务失败: %v", err)
			}
			tt.arrange()

			ready, reason, err := IsEmbyLibraryRefreshTaskReady(task, now)
			if err != nil {
				t.Fatalf("判断刷新任务失败: %v", err)
			}
			if ready {
				t.Fatalf("item 任务所属媒体库还有未完成工作时不应刷新")
			}
			if reason != tt.wantReason {
				t.Fatalf("等待原因 = %s，期望 %s", reason, tt.wantReason)
			}
		})
	}
}

func TestRefreshTaskItemUsesIndexedLibraryForWaitSyncPathsWhenFallbackMissing(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 11})
	db.Db.Create(&EmbyMediaItem{ItemId: "301", ItemIdInt: 301, LibraryId: "lib-tv", Name: "第一季", Type: "Season"})
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     "301",
		ItemName:   "第一季",
		ItemType:   "Season",
		Recursive:  true,
	}, 10, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建缺少 fallback 的 item 刷新任务失败: %v", err)
	}
	db.Db.Create(&DbDownloadTask{SyncPathId: 11, Status: DownloadStatusPending})

	ready, reason, err := IsEmbyLibraryRefreshTaskReady(task, now)
	if err != nil {
		t.Fatalf("判断刷新任务失败: %v", err)
	}
	if ready {
		t.Fatal("fallback 为空时应按 item 所属媒体库等待其他同步目录下载完成")
	}
	if reason != "download_running" {
		t.Fatalf("等待原因 = %s，期望 download_running", reason)
	}
}

func TestRefreshTaskItemPrefersIndexedLibraryOverIncorrectFallbackForWaitSyncPaths(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 11})
	db.Db.Create(&EmbyMediaItem{ItemId: "301", ItemIdInt: 301, LibraryId: "lib-tv", Name: "第 1 集", Type: "Episode"})
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "301",
		ItemName:            "第 1 集",
		ItemType:            "Episode",
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	}, 10, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建旧错误 fallback 任务失败: %v", err)
	}
	db.Db.Create(&DbDownloadTask{SyncPathId: 11, Status: DownloadStatusPending})

	ready, reason, err := IsEmbyLibraryRefreshTaskReady(task, now)
	if err != nil {
		t.Fatalf("判断刷新任务失败: %v", err)
	}
	if ready || reason != "download_running" {
		t.Fatalf("ready=%v reason=%s，期望按本地真实 lib-tv 等待下载", ready, reason)
	}
}

func TestRefreshTaskDownloadEventPrefersIndexedLibraryOverIncorrectFallback(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 11})
	db.Db.Create(&EmbyMediaItem{ItemId: "301", ItemIdInt: 301, LibraryId: "lib-tv", Name: "第 1 集", Type: "Episode"})
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "301",
		ItemName:            "第 1 集",
		ItemType:            "Episode",
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	}, 10, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建旧错误 fallback 任务失败: %v", err)
	}

	if err := NotifyEmbyRefreshDownloadTasksChangedBySyncPathIds([]uint{11}); err != nil {
		t.Fatalf("处理下载事件失败: %v", err)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if updated.RefreshAfterAt <= now {
		t.Fatalf("真实 lib-tv 的下载事件应延长稳定窗口，实际 refresh_after_at=%d", updated.RefreshAfterAt)
	}
}

func TestRefreshItemFailureRepairsIncorrectFallbackBeforeLibraryRefresh(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	db.Db.Create(&EmbyMediaItem{ItemId: "301", ItemIdInt: 301, LibraryId: "lib-tv", Name: "第 1 集", Type: "Episode"})
	requestedLibrary := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/Items/301/Refresh") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(r.URL.Path, "/Items/lib-") && strings.HasSuffix(r.URL.Path, "/Refresh") {
			parts := strings.Split(r.URL.Path, "/")
			requestedLibrary = parts[len(parts)-2]
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "301",
		ItemName:            "第 1 集",
		ItemType:            "Episode",
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	}, 10, nowUnix()-100)
	task.RefreshAfterAt = nowUnix() - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建刷新任务失败: %v", err)
	}

	if err := refreshEmbyLibraryTask(task); err != nil {
		t.Fatalf("item 失败后刷新真实媒体库失败: %v", err)
	}
	if requestedLibrary != "lib-tv" {
		t.Fatalf("fallback 刷新媒体库 = %s，期望 lib-tv", requestedLibrary)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if updated.FallbackLibraryId != "lib-tv" || updated.FallbackLibraryName != "剧集" {
		t.Fatalf("持久化 fallback = %s/%s，期望 lib-tv/剧集", updated.FallbackLibraryId, updated.FallbackLibraryName)
	}
}

func TestExecuteEmbyRefreshTaskDoesNotUseFallbackWithoutItemIDs(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	refreshRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	client := embyclientrestgo.NewClient(server.URL, GlobalEmbyConfig.EmbyApiKey)
	err := executeEmbyRefreshTask(client, &EmbyLibraryRefreshTask{
		TargetType:          EmbyLibraryRefreshTargetTypeItem,
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	})
	if err == nil {
		t.Fatal("缺少 item ID 的历史任务应失败，不能直接刷新 fallback 媒体库")
	}
	if refreshRequests != 0 {
		t.Fatalf("缺少 item ID 时刷新请求次数 = %d，期望 0", refreshRequests)
	}
}

func TestRefreshTaskWaitsForRetryableFailedDownload(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&SyncFile{BaseModel: BaseModel{ID: 1}, SyncPathId: 10})
	db.Db.Create(&DbDownloadTask{SyncFileId: 1, Status: DownloadStatusFailed, RetryCount: 0})

	count, err := CountActiveDownloadTasksBySyncPathIds([]uint{10})
	if err != nil {
		t.Fatalf("统计下载任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("可自动重试的失败任务应继续阻塞刷新，实际 %d", count)
	}
}

func TestRefreshTaskWaitsForDownloadingDownload(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&DbDownloadTask{SyncPathId: 10, Status: DownloadStatusDownloading})

	count, err := CountActiveDownloadTasksBySyncPathIds([]uint{10})
	if err != nil {
		t.Fatalf("统计下载中任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("下载中任务应继续阻塞刷新，实际 %d", count)
	}
}

func TestScheduleNextEmbyLibraryRefreshCheckUsesEarliestFuturePendingTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	dueTask := newPendingEmbyLibraryRefreshTask("lib-due", "已到期", []uint{10}, now)
	dueTask.RefreshAfterAt = now - 1
	firstFutureTask := newPendingEmbyLibraryRefreshTask("lib-first", "最早未来任务", []uint{11}, now)
	firstFutureTask.RefreshAfterAt = now + 10
	secondFutureTask := newPendingEmbyLibraryRefreshTask("lib-second", "较晚未来任务", []uint{12}, now)
	secondFutureTask.RefreshAfterAt = now + 30
	completedTask := newPendingEmbyLibraryRefreshTask("lib-completed", "已完成任务", []uint{13}, now)
	completedTask.Status = EmbyLibraryRefreshStatusCompleted
	completedTask.RefreshAfterAt = now + 5
	if err := db.Db.Create([]*EmbyLibraryRefreshTask{dueTask, firstFutureTask, secondFutureTask, completedTask}).Error; err != nil {
		t.Fatalf("创建测试刷新任务失败: %v", err)
	}

	ScheduleNextEmbyLibraryRefreshCheck()

	assertScheduledEmbyRefreshCheckAt(t, firstFutureTask.RefreshAfterAt)
}

func TestScheduleNextEmbyLibraryRefreshCheckDoesNotTriggerFuturePendingTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	drainEmbyRefreshCheckChan()
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-future", "未来任务", []uint{10}, now)
	task.RefreshAfterAt = now + 10
	task.LastCheckedAt = 0
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建测试刷新任务失败: %v", err)
	}

	ScheduleNextEmbyLibraryRefreshCheck()

	select {
	case <-embyRefreshCheckChan:
		t.Fatal("未到 refresh_after_at 的 pending 任务不应立即触发检查")
	default:
	}
	assertScheduledEmbyRefreshCheckAt(t, task.RefreshAfterAt)
}

func TestSetNextEmbyLibraryRefreshCheckTimerKeepsEarlierScheduleWhenLaterResultArrives(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	earlierCheckAt := now + 10

	setNextEmbyLibraryRefreshCheckTimer(earlierCheckAt, true)
	setNextEmbyLibraryRefreshCheckTimer(now+30, true)

	assertScheduledEmbyRefreshCheckAt(t, earlierCheckAt)
}

func TestSetNextEmbyLibraryRefreshCheckTimerKeepsEarlierScheduleWhenNoTaskResultArrives(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	earlierCheckAt := now + 10

	setNextEmbyLibraryRefreshCheckTimer(earlierCheckAt, true)
	setNextEmbyLibraryRefreshCheckTimer(0, false)

	assertScheduledEmbyRefreshCheckAt(t, earlierCheckAt)
}

func TestScheduleNextEmbyLibraryRefreshCheckIgnoresAlreadyCheckedDuePendingTasks(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-due", "已到期", []uint{10}, now)
	task.RefreshAfterAt = now - 1
	task.LastCheckedAt = now
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建测试刷新任务失败: %v", err)
	}

	ScheduleNextEmbyLibraryRefreshCheck()

	assertNoScheduledEmbyRefreshCheck(t)
}

func TestScheduleNextEmbyLibraryRefreshCheckTriggersDueUncheckedTaskAndKeepsFutureTimer(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	drainEmbyRefreshCheckChan()
	now := nowUnix()
	dueTask := newPendingEmbyLibraryRefreshTask("lib-due", "已到期未检查", []uint{10}, now)
	dueTask.RefreshAfterAt = now - 1
	dueTask.LastCheckedAt = 0
	futureTask := newPendingEmbyLibraryRefreshTask("lib-future", "未来任务", []uint{11}, now)
	futureTask.RefreshAfterAt = now + 10
	if err := db.Db.Create([]*EmbyLibraryRefreshTask{dueTask, futureTask}).Error; err != nil {
		t.Fatalf("创建测试刷新任务失败: %v", err)
	}

	ScheduleNextEmbyLibraryRefreshCheck()

	select {
	case <-embyRefreshCheckChan:
	default:
		t.Fatal("已到期且未按 refresh_after_at 检查过的 pending 任务应立即触发检查")
	}
	assertScheduledEmbyRefreshCheckAt(t, futureTask.RefreshAfterAt)
}

func TestDownloadTaskChangedSchedulesNextEmbyRefreshCheck(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}

	if err := NotifyEmbyRefreshDownloadTasksChangedBySyncPathIds([]uint{10}); err != nil {
		t.Fatalf("处理下载任务变化失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-movie").First(&updated).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if updated.RefreshAfterAt <= now {
		t.Fatalf("下载事件应延长稳定窗口，实际 refresh_after_at=%d now=%d", updated.RefreshAfterAt, now)
	}
	assertScheduledEmbyRefreshCheckAt(t, updated.RefreshAfterAt)
}

func TestRefreshTaskDownloadEventDelaysItemByFallbackLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 11})
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "301",
		ItemName:            "第一季",
		ItemType:            "Season",
		Recursive:           true,
		FallbackLibraryId:   "lib-tv",
		FallbackLibraryName: "剧集",
	}, 10, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建 item 刷新任务失败: %v", err)
	}

	if err := NotifyEmbyRefreshDownloadTasksChangedBySyncPathIds([]uint{11}); err != nil {
		t.Fatalf("处理下载任务变化失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("301")).First(&updated).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if updated.RefreshAfterAt <= now {
		t.Fatalf("来自同媒体库其他同步目录的下载事件应延长 item 任务稳定窗口，实际 refresh_after_at=%d now=%d", updated.RefreshAfterAt, now)
	}
	assertScheduledEmbyRefreshCheckAt(t, updated.RefreshAfterAt)
}

func TestRefreshTaskDownloadEventMatchesItemOwnSyncPathWhenLibraryUnresolved(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     "unresolved-item",
		ItemName:   "未知条目",
	}, 10, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建 unresolved item 任务失败: %v", err)
	}

	if err := NotifyEmbyRefreshDownloadTasksChangedBySyncPathIds([]uint{10}); err != nil {
		t.Fatalf("处理下载事件失败: %v", err)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if updated.RefreshAfterAt <= now {
		t.Fatalf("任务自身同步目录事件应延长稳定窗口，实际 refresh_after_at=%d", updated.RefreshAfterAt)
	}
}

func TestCheckPendingEmbyLibraryRefreshTasksReschedulesFutureTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-future", "未来任务", []uint{10}, now)
	task.RefreshAfterAt = now + 10
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}

	CheckPendingEmbyLibraryRefreshTasks()

	assertScheduledEmbyRefreshCheckAt(t, task.RefreshAfterAt)
}

func TestCheckPendingEmbyLibraryRefreshTasksRefreshesOnlyDueLibraries(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	calledLibraries := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 5 {
			calledLibraries <- parts[3]
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	now := nowUnix()
	dueTask := newPendingEmbyLibraryRefreshTask("lib-due", "已到期媒体库", []uint{10}, now-100)
	dueTask.RefreshAfterAt = now - 1
	futureTask := newPendingEmbyLibraryRefreshTask("lib-future", "未来媒体库", []uint{11}, now)
	futureTask.RefreshAfterAt = now + 10
	if err := db.Db.Create([]*EmbyLibraryRefreshTask{dueTask, futureTask}).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}

	CheckPendingEmbyLibraryRefreshTasks()

	var updatedDue EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-due").First(&updatedDue).Error; err != nil {
		t.Fatalf("查询已到期媒体库任务失败: %v", err)
	}
	if updatedDue.Status != EmbyLibraryRefreshStatusCompleted {
		t.Fatalf("已到期媒体库状态 = %s，期望 completed", updatedDue.Status)
	}
	var updatedFuture EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-future").First(&updatedFuture).Error; err != nil {
		t.Fatalf("查询未来媒体库任务失败: %v", err)
	}
	if updatedFuture.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("未来媒体库状态 = %s，期望 pending", updatedFuture.Status)
	}

	select {
	case got := <-calledLibraries:
		if got != "lib-due" {
			t.Fatalf("刷新媒体库 ID = %s，期望 lib-due", got)
		}
	default:
		t.Fatal("已到期媒体库应触发刷新")
	}
	select {
	case got := <-calledLibraries:
		t.Fatalf("不应刷新未到期媒体库，实际刷新 %s", got)
	default:
	}
	assertScheduledEmbyRefreshCheckAt(t, updatedFuture.RefreshAfterAt)
}

func TestEmbyRefreshTimerTriggersCheckChannel(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	drainEmbyRefreshCheckChan()
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-timer", "定时唤醒", []uint{10}, now)
	task.RefreshAfterAt = now + 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}

	ScheduleNextEmbyLibraryRefreshCheck()

	timeout := time.Until(time.Unix(task.RefreshAfterAt, 0)) + 500*time.Millisecond
	if timeout < 500*time.Millisecond {
		timeout = 500 * time.Millisecond
	}
	select {
	case <-embyRefreshCheckChan:
	case <-time.After(timeout):
		t.Fatalf("Emby 刷新 timer 未在 %s 内触发检查", timeout)
	}
}

func TestClearDownloadPendingTasksCancelsPendingEmbyRefreshTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now)
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}
	if err := db.Db.Create(&DbDownloadTask{
		SyncPathId: 10,
		Status:     DownloadStatusPending,
		Source:     DownloadSourceStrm,
	}).Error; err != nil {
		t.Fatalf("创建等待下载任务失败: %v", err)
	}

	if err := ClearDownloadPendingTasks(); err != nil {
		t.Fatalf("清空等待下载任务失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-movie").First(&updated).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusCancelled {
		t.Fatalf("媒体库刷新任务状态 = %s，期望 cancelled", updated.Status)
	}
	if !strings.Contains(updated.Error, "清空等待下载任务") {
		t.Fatalf("媒体库刷新任务取消原因 = %s，期望包含 清空等待下载任务", updated.Error)
	}
}

func TestRefreshTaskClearDownloadPendingCancelsItemByFallbackLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 11})
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "301",
		ItemName:            "第一季",
		ItemType:            "Season",
		Recursive:           true,
		FallbackLibraryId:   "lib-tv",
		FallbackLibraryName: "剧集",
	}, 10, now)
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建 item 刷新任务失败: %v", err)
	}
	if err := db.Db.Create(&DbDownloadTask{
		SyncPathId: 11,
		Status:     DownloadStatusPending,
		Source:     DownloadSourceStrm,
	}).Error; err != nil {
		t.Fatalf("创建等待下载任务失败: %v", err)
	}

	if err := ClearDownloadPendingTasks(); err != nil {
		t.Fatalf("清空等待下载任务失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("301")).First(&updated).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusCancelled {
		t.Fatalf("item 刷新任务状态 = %s，期望 cancelled", updated.Status)
	}
	if !strings.Contains(updated.Error, "清空等待下载任务") {
		t.Fatalf("item 刷新任务取消原因 = %s，期望包含 清空等待下载任务", updated.Error)
	}
}

func TestRefreshTaskClearDownloadPendingCancelsUnresolvedItemByOwnSyncPath(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     "unresolved-item",
		ItemName:   "未知条目",
	}, 10, now)
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建 unresolved item 任务失败: %v", err)
	}
	if err := db.Db.Create(&DbDownloadTask{SyncPathId: 10, Status: DownloadStatusPending, Source: DownloadSourceStrm}).Error; err != nil {
		t.Fatalf("创建等待下载任务失败: %v", err)
	}

	if err := ClearDownloadPendingTasks(); err != nil {
		t.Fatalf("清空等待下载任务失败: %v", err)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusCancelled {
		t.Fatalf("item 刷新任务状态 = %s，期望 cancelled", updated.Status)
	}
}

func TestRetryFailedDownloadTasksKeepsPendingEmbyRefreshTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now)
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}
	if err := db.Db.Create(&DbDownloadTask{
		SyncPathId: 10,
		Status:     DownloadStatusFailed,
		RetryCount: 0,
		Source:     DownloadSourceStrm,
	}).Error; err != nil {
		t.Fatalf("创建失败下载任务失败: %v", err)
	}

	if err := RetryFailedDownloadTasks(DefaultQueueRetryMax); err != nil {
		t.Fatalf("重试失败下载任务失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-movie").First(&updated).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("媒体库刷新任务状态 = %s，期望 pending", updated.Status)
	}
}

func TestRefreshTaskCancelsAfterDeadline(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	var refreshCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now-DefaultEmbyRefreshMaxWaitSeconds-1)
	task.RefreshAfterAt = now - 1
	task.DeadlineAt = now - 1
	db.Db.Create(task)

	CheckPendingEmbyLibraryRefreshTasks()

	var updated EmbyLibraryRefreshTask
	db.Db.First(&updated, task.ID)
	if updated.Status != EmbyLibraryRefreshStatusCancelled {
		t.Fatalf("deadline 到期后状态 = %s，期望 cancelled", updated.Status)
	}
	if updated.LastRefreshAt != 0 {
		t.Fatalf("取消刷新后不应记录刷新时间，实际 last_refresh_at=%d", updated.LastRefreshAt)
	}
	if refreshCalled.Load() {
		t.Fatal("deadline 到期取消刷新时不应调用 Emby 刷新接口")
	}
}

func TestDownloadTaskChangedEventIsBatched(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	db.Db.Create(task)
	db.Db.Create(&SyncFile{BaseModel: BaseModel{ID: 1}, SyncPathId: 10})

	HandleDownloadTaskStatusChanged(helpers.Event{Data: DownloadTaskStatusChangedPayload{SyncFileId: 1}})

	var updated EmbyLibraryRefreshTask
	db.Db.Where("library_id = ?", "lib-movie").First(&updated)
	if updated.RefreshAfterAt > now {
		t.Fatalf("下载事件应先进入批量队列，不应立即更新DB，实际 refresh_after_at=%d now=%d", updated.RefreshAfterAt, now)
	}

	if err := flushPendingEmbyRefreshDownloadTaskChanges(); err != nil {
		t.Fatalf("批量处理下载事件失败: %v", err)
	}

	db.Db.Where("library_id = ?", "lib-movie").First(&updated)
	if updated.RefreshAfterAt <= now {
		t.Fatalf("批量处理下载事件后应延长稳定窗口，实际 refresh_after_at=%d now=%d", updated.RefreshAfterAt, now)
	}
}

func TestDownloadTaskChangedEventUsesSyncPathId(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	db.Db.Create(task)

	HandleDownloadTaskStatusChanged(helpers.Event{Data: DownloadTaskStatusChangedPayload{SyncPathId: 10}})

	if err := flushPendingEmbyRefreshDownloadTaskChanges(); err != nil {
		t.Fatalf("批量处理下载事件失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	db.Db.Where("library_id = ?", "lib-movie").First(&updated)
	if updated.RefreshAfterAt <= now {
		t.Fatalf("sync_path_id 下载事件应延长稳定窗口，实际 refresh_after_at=%d now=%d", updated.RefreshAfterAt, now)
	}
}

func TestMarkEmbyRefreshTaskCompleted(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, nowUnix())
	task.Status = EmbyLibraryRefreshStatusRefreshing
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建刷新任务失败: %v", err)
	}

	if err := markEmbyRefreshTaskCompleted(task); err != nil {
		t.Fatalf("标记完成失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	db.Db.First(&updated, task.ID)
	if updated.Status != EmbyLibraryRefreshStatusCompleted {
		t.Fatalf("完成后状态 = %s，期望 completed", updated.Status)
	}
	if updated.LastRefreshAt == 0 {
		t.Fatal("完成后应记录 last_refresh_at")
	}
}

func TestReconcilePendingEmbyRefreshTasksPromotesTenItems(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	for i := 0; i < EmbyRefreshItemAggregationThreshold; i++ {
		task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("item-%02d", i),
			ItemName:            "电影",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}, 10, now-int64(i))
		task.RefreshAfterAt = now - 1
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建 item 刷新任务失败: %v", err)
		}
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("聚合刷新任务失败: %v", err)
	}

	var libraryTask EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyLibraryRefreshTaskKey("lib-movie")).First(&libraryTask).Error; err != nil {
		t.Fatalf("应创建媒体库刷新任务: %v", err)
	}
	if libraryTask.TargetType != EmbyLibraryRefreshTargetTypeLibrary || libraryTask.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("媒体库任务 = %+v，期望 pending library", libraryTask)
	}
	if libraryTask.RefreshAfterAt > now {
		t.Fatalf("阈值转换后不应重新延长防抖窗口，refresh_after_at=%d now=%d", libraryTask.RefreshAfterAt, now)
	}
	var cancelled int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusCancelled).
		Count(&cancelled).Error; err != nil {
		t.Fatalf("统计被吸收的 item 任务失败: %v", err)
	}
	if cancelled != EmbyRefreshItemAggregationThreshold {
		t.Fatalf("被吸收的 item 任务数量 = %d，期望 %d", cancelled, EmbyRefreshItemAggregationThreshold)
	}
}

func TestReconcilePendingEmbyRefreshTasksHandlesCreateConflict(t *testing.T) {
	tests := []struct {
		name    string
		saveErr error
	}{
		{name: "冲突后二次合并持久化"},
		{name: "二次合并保存失败时回滚", saveErr: errors.New("保存冲突合并结果失败")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			expectedItemIDs := []string{"existing-item"}
			expectedSyncPathIDs := make([]uint, 0, EmbyRefreshItemAggregationThreshold+1)
			for i := 0; i < EmbyRefreshItemAggregationThreshold; i++ {
				itemID := fmt.Sprintf("item-%02d", i)
				syncPathID := uint(10 + i)
				task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
					TargetType:          EmbyRefreshTargetTypeItem,
					ItemID:              itemID,
					ItemName:            "电影",
					FallbackLibraryId:   "lib-movie",
					FallbackLibraryName: "电影库",
				}, syncPathID, now)
				task.LastEventAt = now + int64(i)
				task.RefreshAfterAt = now - 1
				task.DeadlineAt = now + 100 + int64(i)
				if err := db.Db.Create(task).Error; err != nil {
					t.Fatalf("创建 item 刷新任务失败: %v", err)
				}
				expectedItemIDs = append(expectedItemIDs, itemID)
				expectedSyncPathIDs = append(expectedSyncPathIDs, syncPathID)
			}
			expectedSyncPathIDs = append(expectedSyncPathIDs, 99)

			existingLibraryTask := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{99}, now-100)
			existingLibraryTask.LastEventAt = now - 100
			existingLibraryTask.RefreshAfterAt = now - 50
			existingLibraryTask.DeadlineAt = now + 500
			existingLibraryTask.SetItemIds([]string{"existing-item"})

			var injected atomic.Bool
			createCallbackName := "test:inject_emby_library_refresh_create_conflict"
			if err := db.Db.Callback().Create().Before("gorm:create").Register(createCallbackName, func(tx *gorm.DB) {
				task, ok := tx.Statement.Dest.(*EmbyLibraryRefreshTask)
				if !ok || task.TaskKey != existingLibraryTask.TaskKey || !injected.CompareAndSwap(false, true) {
					return
				}
				if err := tx.Session(&gorm.Session{NewDB: true}).Create(existingLibraryTask).Error; err != nil {
					tx.AddError(err)
				}
			}); err != nil {
				t.Fatalf("注册创建冲突回调失败: %v", err)
			}
			t.Cleanup(func() {
				if err := db.Db.Callback().Create().Remove(createCallbackName); err != nil {
					t.Errorf("移除创建冲突回调失败: %v", err)
				}
			})

			var saveFailed atomic.Bool
			if tt.saveErr != nil {
				updateCallbackName := "test:fail_emby_library_refresh_conflict_merge_save"
				if err := db.Db.Callback().Update().Before("gorm:update").Register(updateCallbackName, func(tx *gorm.DB) {
					task, ok := tx.Statement.Dest.(*EmbyLibraryRefreshTask)
					if !ok || task.TaskKey != existingLibraryTask.TaskKey || !saveFailed.CompareAndSwap(false, true) {
						return
					}
					tx.AddError(tt.saveErr)
				}); err != nil {
					t.Fatalf("注册保存失败回调失败: %v", err)
				}
				t.Cleanup(func() {
					if err := db.Db.Callback().Update().Remove(updateCallbackName); err != nil {
						t.Errorf("移除保存失败回调失败: %v", err)
					}
				})
			}

			err := reconcilePendingEmbyRefreshTasks(now)
			if tt.saveErr != nil {
				if !errors.Is(err, tt.saveErr) {
					t.Fatalf("协调错误=%v，期望 %v", err, tt.saveErr)
				}
				if !injected.Load() || !saveFailed.Load() {
					t.Fatalf("创建冲突=%v，保存失败=%v，期望均已触发", injected.Load(), saveFailed.Load())
				}

				var libraryCount int64
				if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
					Where("task_key = ?", embyLibraryRefreshTaskKey("lib-movie")).
					Count(&libraryCount).Error; err != nil {
					t.Fatalf("统计媒体库刷新任务失败: %v", err)
				}
				if libraryCount != 0 {
					t.Fatalf("事务回滚后媒体库刷新任务数量=%d，期望 0", libraryCount)
				}

				var pendingItems int64
				if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
					Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusPending).
					Count(&pendingItems).Error; err != nil {
					t.Fatalf("统计 pending item 任务失败: %v", err)
				}
				if pendingItems != EmbyRefreshItemAggregationThreshold {
					t.Fatalf("事务回滚后 pending item 数量=%d，期望 %d", pendingItems, EmbyRefreshItemAggregationThreshold)
				}
				return
			}

			if err != nil {
				t.Fatalf("协调 pending 刷新任务失败: %v", err)
			}
			if !injected.Load() {
				t.Fatal("未触发媒体库任务创建冲突")
			}

			var libraryTasks []EmbyLibraryRefreshTask
			if err := db.Db.Where("task_key = ?", embyLibraryRefreshTaskKey("lib-movie")).Find(&libraryTasks).Error; err != nil {
				t.Fatalf("查询媒体库刷新任务失败: %v", err)
			}
			if len(libraryTasks) != 1 {
				t.Fatalf("媒体库刷新任务数量=%d，期望 1", len(libraryTasks))
			}
			libraryTask := libraryTasks[0]
			if !reflect.DeepEqual(libraryTask.GetItemIds(), expectedItemIDs) {
				t.Fatalf("item_ids=%v，期望 %v", libraryTask.GetItemIds(), expectedItemIDs)
			}
			if !reflect.DeepEqual(libraryTask.GetSyncPathIds(), expectedSyncPathIDs) {
				t.Fatalf("sync_path_ids=%v，期望 %v", libraryTask.GetSyncPathIds(), expectedSyncPathIDs)
			}
			if libraryTask.LastEventAt != now+EmbyRefreshItemAggregationThreshold-1 {
				t.Fatalf("last_event_at=%d，期望 %d", libraryTask.LastEventAt, now+EmbyRefreshItemAggregationThreshold-1)
			}
			if libraryTask.RefreshAfterAt != now-1 {
				t.Fatalf("refresh_after_at=%d，期望 %d", libraryTask.RefreshAfterAt, now-1)
			}
			if libraryTask.DeadlineAt != now+100 {
				t.Fatalf("deadline_at=%d，期望 %d", libraryTask.DeadlineAt, now+100)
			}

			var cancelledItems int64
			if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
				Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusCancelled).
				Count(&cancelledItems).Error; err != nil {
				t.Fatalf("统计被吸收的 item 任务失败: %v", err)
			}
			if cancelledItems != EmbyRefreshItemAggregationThreshold {
				t.Fatalf("被吸收的 item 任务数量=%d，期望 %d", cancelledItems, EmbyRefreshItemAggregationThreshold)
			}
		})
	}
}

func TestReconcilePendingEmbyRefreshTasksThresholdBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		count         int
		expectLibrary bool
	}{
		{name: "8个", count: 8, expectLibrary: false},
		{name: "9个", count: 9, expectLibrary: false},
		{name: "10个", count: 10, expectLibrary: true},
		{name: "11个", count: 11, expectLibrary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			for i := 0; i < tc.count; i++ {
				task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
					TargetType:          EmbyRefreshTargetTypeItem,
					ItemID:              fmt.Sprintf("item-%02d", i),
					ItemName:            "电影",
					FallbackLibraryId:   "lib-movie",
					FallbackLibraryName: "电影库",
				}, 10, now)
				task.RefreshAfterAt = now - 1
				if err := db.Db.Create(task).Error; err != nil {
					t.Fatalf("创建 item 刷新任务失败: %v", err)
				}
			}

			if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
				t.Fatalf("协调 pending 刷新任务失败: %v", err)
			}
			var libraryCount int64
			if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
				Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeLibrary, EmbyLibraryRefreshStatusPending).
				Count(&libraryCount).Error; err != nil {
				t.Fatalf("统计媒体库刷新任务失败: %v", err)
			}
			if (libraryCount > 0) != tc.expectLibrary {
				t.Fatalf("媒体库任务数量 = %d，期望是否存在=%v", libraryCount, tc.expectLibrary)
			}
		})
	}
}

func TestReconcilePendingEmbyRefreshTasksRepairsStaleFallbackBeforeGrouping(t *testing.T) {
	tests := []struct {
		name                 string
		count                int
		createOldLibraryTask bool
		actualLibrary        func(int) string
		expectedLibraries    []string
		expectedPendingItems int64
		expectedLibACount    int64
		expectedLibBCount    int64
	}{
		{
			name:          "10个旧hint全部修复到新媒体库后聚合",
			count:         EmbyRefreshItemAggregationThreshold,
			actualLibrary: func(int) string { return "lib-b" },
			expectedLibraries: []string{
				"lib-b",
			},
			expectedLibBCount: EmbyRefreshItemAggregationThreshold,
		},
		{
			name:                 "已有旧媒体库任务不吸收已改属其他库的item",
			count:                1,
			createOldLibraryTask: true,
			actualLibrary:        func(int) string { return "lib-b" },
			expectedLibraries:    []string{"lib-a"},
			expectedPendingItems: 1,
			expectedLibBCount:    1,
		},
		{
			name:  "9个真实旧库加1个已改属新库均不达到阈值",
			count: EmbyRefreshItemAggregationThreshold,
			actualLibrary: func(index int) string {
				if index == EmbyRefreshItemAggregationThreshold-1 {
					return "lib-b"
				}
				return "lib-a"
			},
			expectedLibraries:    []string{},
			expectedPendingItems: EmbyRefreshItemAggregationThreshold,
			expectedLibACount:    EmbyRefreshItemAggregationThreshold - 1,
			expectedLibBCount:    1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			for _, relation := range []EmbyLibrarySyncPath{
				{LibraryId: "lib-a", LibraryName: "媒体库 A", SyncPathId: 10},
				{LibraryId: "lib-b", LibraryName: "媒体库 B", SyncPathId: 10},
			} {
				if err := db.Db.Create(&relation).Error; err != nil {
					t.Fatalf("创建媒体库关联失败: %v", err)
				}
			}
			if tt.createOldLibraryTask {
				libraryTask := newPendingEmbyLibraryRefreshTask("lib-a", "媒体库 A", []uint{10}, now)
				if err := db.Db.Create(libraryTask).Error; err != nil {
					t.Fatalf("创建旧媒体库刷新任务失败: %v", err)
				}
			}
			for i := 0; i < tt.count; i++ {
				itemID := fmt.Sprintf("stale-item-%02d", i)
				if err := db.Db.Create(&EmbyMediaItem{
					ItemId:    itemID,
					Name:      itemID,
					Type:      "Movie",
					LibraryId: tt.actualLibrary(i),
				}).Error; err != nil {
					t.Fatalf("创建 Emby item 索引失败: %v", err)
				}
				task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
					TargetType:          EmbyRefreshTargetTypeItem,
					ItemID:              itemID,
					ItemName:            itemID,
					FallbackLibraryId:   "lib-a",
					FallbackLibraryName: "媒体库 A",
				}, 10, now)
				task.RefreshAfterAt = now - 1
				if err := db.Db.Create(task).Error; err != nil {
					t.Fatalf("创建旧 hint item 刷新任务失败: %v", err)
				}
			}

			if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
				t.Fatalf("协调 pending 刷新任务失败: %v", err)
			}

			var libraryTasks []EmbyLibraryRefreshTask
			if err := db.Db.Where("target_type = ?", EmbyLibraryRefreshTargetTypeLibrary).
				Order("library_id ASC").Find(&libraryTasks).Error; err != nil {
				t.Fatalf("查询媒体库刷新任务失败: %v", err)
			}
			libraryIDs := make([]string, 0, len(libraryTasks))
			for i := range libraryTasks {
				libraryIDs = append(libraryIDs, libraryTasks[i].LibraryId)
			}
			if !reflect.DeepEqual(libraryIDs, tt.expectedLibraries) {
				t.Fatalf("媒体库刷新任务=%v，期望 %v", libraryIDs, tt.expectedLibraries)
			}

			var pendingItems int64
			if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
				Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusPending).
				Count(&pendingItems).Error; err != nil {
				t.Fatalf("统计 pending item 失败: %v", err)
			}
			if pendingItems != tt.expectedPendingItems {
				t.Fatalf("pending item 数量=%d，期望 %d", pendingItems, tt.expectedPendingItems)
			}

			var libACount int64
			if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
				Where("target_type = ? AND fallback_library_id = ?", EmbyLibraryRefreshTargetTypeItem, "lib-a").
				Count(&libACount).Error; err != nil {
				t.Fatalf("统计 lib-a item 失败: %v", err)
			}
			if libACount != tt.expectedLibACount {
				t.Fatalf("lib-a item 数量=%d，期望 %d", libACount, tt.expectedLibACount)
			}
			var libBCount int64
			if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
				Where("target_type = ? AND fallback_library_id = ?", EmbyLibraryRefreshTargetTypeItem, "lib-b").
				Count(&libBCount).Error; err != nil {
				t.Fatalf("统计 lib-b item 失败: %v", err)
			}
			if libBCount != tt.expectedLibBCount {
				t.Fatalf("lib-b item 数量=%d，期望 %d", libBCount, tt.expectedLibBCount)
			}
		})
	}
}

func TestReconcilePendingEmbyRefreshTasksResetsHistoricalItemIDsForNewCycle(t *testing.T) {
	tests := []struct {
		name               string
		status             string
		preserveHistorical bool
	}{
		{name: "pending保留当前周期item", status: EmbyLibraryRefreshStatusPending, preserveHistorical: true},
		{name: "completed开始新周期", status: EmbyLibraryRefreshStatusCompleted},
		{name: "failed开始新周期", status: EmbyLibraryRefreshStatusFailed},
		{name: "cancelled开始新周期", status: EmbyLibraryRefreshStatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			libraryTask := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
			libraryTask.Status = tt.status
			libraryTask.SetItemIds([]string{"historical-item"})
			if err := db.Db.Create(libraryTask).Error; err != nil {
				t.Fatalf("创建已有媒体库刷新任务失败: %v", err)
			}
			expectedItemIDs := make([]string, 0, EmbyRefreshItemAggregationThreshold+1)
			for i := 0; i < EmbyRefreshItemAggregationThreshold; i++ {
				itemID := fmt.Sprintf("current-item-%02d", i)
				task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
					TargetType:          EmbyRefreshTargetTypeItem,
					ItemID:              itemID,
					ItemName:            itemID,
					FallbackLibraryId:   "lib-movie",
					FallbackLibraryName: "电影库",
				}, 10, now)
				task.RefreshAfterAt = now - 1
				if err := db.Db.Create(task).Error; err != nil {
					t.Fatalf("创建当前周期 item 任务失败: %v", err)
				}
				expectedItemIDs = append(expectedItemIDs, itemID)
			}
			if tt.preserveHistorical {
				expectedItemIDs = append(expectedItemIDs, "historical-item")
			}

			if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
				t.Fatalf("协调 pending 刷新任务失败: %v", err)
			}
			var updated EmbyLibraryRefreshTask
			if err := db.Db.First(&updated, libraryTask.ID).Error; err != nil {
				t.Fatalf("查询媒体库刷新任务失败: %v", err)
			}
			if !reflect.DeepEqual(updated.GetItemIds(), expectedItemIDs) {
				t.Fatalf("item_ids=%v，期望 %v", updated.GetItemIds(), expectedItemIDs)
			}
		})
	}
}

func TestReconcilePendingEmbyRefreshTasksKeepsNineItemsAndSharesDebounce(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	maxRefreshAfter := now + 20
	for i := 0; i < EmbyRefreshItemAggregationThreshold-1; i++ {
		task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("item-%02d", i),
			ItemName:            "电影",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}, 10, now)
		task.RefreshAfterAt = now + int64(i+1)
		if task.RefreshAfterAt == maxRefreshAfter {
			task.RefreshAfterAt++
		}
		if i == EmbyRefreshItemAggregationThreshold-2 {
			task.RefreshAfterAt = maxRefreshAfter
		}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建 item 刷新任务失败: %v", err)
		}
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调 pending 刷新任务失败: %v", err)
	}

	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Where("target_type = ?", EmbyLibraryRefreshTargetTypeItem).Find(&tasks).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if len(tasks) != EmbyRefreshItemAggregationThreshold-1 {
		t.Fatalf("item 刷新任务数量 = %d，期望 %d", len(tasks), EmbyRefreshItemAggregationThreshold-1)
	}
	for _, task := range tasks {
		if task.Status != EmbyLibraryRefreshStatusPending || task.RefreshAfterAt != maxRefreshAfter {
			t.Fatalf("item 任务未共享防抖时间: %+v", task)
		}
	}
}

func TestReconcilePendingEmbyRefreshTasksLibraryFallbackAbsorbsItems(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	libraryTask := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now)
	libraryTask.RefreshAfterAt = now + 30
	if err := db.Db.Create(libraryTask).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("item-%02d", i),
			ItemName:            "电影",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}, 10, now)
		task.RefreshAfterAt = now + int64(i+1)
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建 item 刷新任务失败: %v", err)
		}
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调 pending 刷新任务失败: %v", err)
	}

	var pendingItems int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusPending).
		Count(&pendingItems).Error; err != nil {
		t.Fatalf("统计 pending item 任务失败: %v", err)
	}
	if pendingItems != 0 {
		t.Fatalf("已有 library fallback 时仍有 %d 个 pending item 任务", pendingItems)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, libraryTask.ID).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if updated.RefreshAfterAt != libraryTask.RefreshAfterAt {
		t.Fatalf("library 防抖时间 = %d，期望 %d", updated.RefreshAfterAt, libraryTask.RefreshAfterAt)
	}
}

func TestReconcilePendingEmbyRefreshTasksExcludesUnresolvedItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	for i := 0; i < EmbyRefreshItemAggregationThreshold-1; i++ {
		task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("resolved-%02d", i),
			ItemName:            "电影",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}, 10, now)
		task.RefreshAfterAt = now - 1
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建已解析 item 任务失败: %v", err)
		}
	}
	unresolved := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     "unresolved",
		ItemName:   "未知条目",
	}, 10, now)
	unresolved.RefreshAfterAt = now - 1
	if err := db.Db.Create(unresolved).Error; err != nil {
		t.Fatalf("创建 unresolved item 任务失败: %v", err)
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调 pending 刷新任务失败: %v", err)
	}

	var libraryCount int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("target_type = ?", EmbyLibraryRefreshTargetTypeLibrary).
		Count(&libraryCount).Error; err != nil {
		t.Fatalf("统计媒体库任务失败: %v", err)
	}
	if libraryCount != 0 {
		t.Fatalf("unresolved item 不应使 9 个已解析 item 达到阈值，媒体库任务数量=%d", libraryCount)
	}
}

func TestReconcilePendingEmbyRefreshTasksKeepsLibrariesIndependent(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	for i := 0; i < EmbyRefreshItemAggregationThreshold; i++ {
		for _, libraryID := range []string{"lib-a", "lib-b"} {
			task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
				TargetType:          EmbyRefreshTargetTypeItem,
				ItemID:              fmt.Sprintf("%s-item-%02d", libraryID, i),
				ItemName:            libraryID,
				FallbackLibraryId:   libraryID,
				FallbackLibraryName: libraryID,
			}, 10, now)
			task.RefreshAfterAt = now - 1
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatalf("创建 item 刷新任务失败: %v", err)
			}
		}
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调 pending 刷新任务失败: %v", err)
	}
	var libraryTasks []EmbyLibraryRefreshTask
	if err := db.Db.Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeLibrary, EmbyLibraryRefreshStatusPending).
		Order("library_id ASC").Find(&libraryTasks).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if len(libraryTasks) != 2 || libraryTasks[0].LibraryId != "lib-a" || libraryTasks[1].LibraryId != "lib-b" {
		t.Fatalf("媒体库刷新任务 = %+v，期望分别生成 lib-a、lib-b", libraryTasks)
	}
}

func TestReconcilePendingEmbyRefreshTasksDoesNotAbsorbIntoRefreshingLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	libraryTask := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now)
	libraryTask.Status = EmbyLibraryRefreshStatusRefreshing
	if err := db.Db.Create(libraryTask).Error; err != nil {
		t.Fatalf("创建 refreshing 媒体库任务失败: %v", err)
	}
	for i := 0; i < EmbyRefreshItemAggregationThreshold; i++ {
		task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("item-%02d", i),
			ItemName:            "电影",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}, 10, now)
		task.RefreshAfterAt = now - 1
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建 item 刷新任务失败: %v", err)
		}
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调 pending 刷新任务失败: %v", err)
	}

	var pendingItems int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusPending).
		Count(&pendingItems).Error; err != nil {
		t.Fatalf("统计 pending item 任务失败: %v", err)
	}
	if pendingItems != EmbyRefreshItemAggregationThreshold {
		t.Fatalf("refreshing library 不应吸收新 item，pending item 数量=%d", pendingItems)
	}
}

func TestMarkEmbyRefreshTaskCompletedDoesNotOverwriteRequeuedPendingTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, nowUnix())
	task.Status = EmbyLibraryRefreshStatusRefreshing
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建刷新任务失败: %v", err)
	}
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status": EmbyLibraryRefreshStatusPending,
	}).Error; err != nil {
		t.Fatalf("模拟刷新任务重新排队失败: %v", err)
	}

	if err := markEmbyRefreshTaskCompleted(task); err != nil {
		t.Fatalf("标记完成失败: %v", err)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("重新排队任务状态 = %s，期望保持 pending", updated.Status)
	}
}

func TestMarkEmbyRefreshTaskFailedDoesNotOverwriteRequeuedPendingTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影", []uint{10}, nowUnix())
	task.Status = EmbyLibraryRefreshStatusRefreshing
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建刷新任务失败: %v", err)
	}
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("id = ?", task.ID).Update("status", EmbyLibraryRefreshStatusPending).Error; err != nil {
		t.Fatalf("模拟刷新任务重新排队失败: %v", err)
	}

	if err := markEmbyRefreshTaskFailed(task, "旧请求失败"); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("重新排队任务状态 = %s，期望保持 pending", updated.Status)
	}
}

func TestUpsertEmbyLibraryRefreshTaskKeepsActiveDeadlineWhenAddingSyncPath(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
	task.DeadlineAt = now + 120
	task.SetItemIds([]string{"current-cycle-item"})
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}

	if err := upsertEmbyLibraryRefreshTask("lib-movie", "电影库", 11, now); err != nil {
		t.Fatalf("更新媒体库刷新任务失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if updated.DeadlineAt != task.DeadlineAt {
		t.Fatalf("活动周期新增同步目录后 deadline_at=%d，期望保留 %d", updated.DeadlineAt, task.DeadlineAt)
	}
	if !reflect.DeepEqual(updated.GetSyncPathIds(), []uint{10, 11}) {
		t.Fatalf("sync_path_ids=%v，期望 [10 11]", updated.GetSyncPathIds())
	}
	if !reflect.DeepEqual(updated.GetItemIds(), []string{"current-cycle-item"}) {
		t.Fatalf("活动周期 item_ids=%v，期望保留当前周期记录", updated.GetItemIds())
	}
}

func TestUpsertEmbyItemRefreshTaskKeepsActiveDeadlineWhenAddingSyncPath(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	for _, syncPathID := range []uint{10, 11} {
		if err := db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影库", SyncPathId: syncPathID}).Error; err != nil {
			t.Fatalf("创建媒体库关联失败: %v", err)
		}
	}
	target := EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "item-1",
		ItemName:            "电影",
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影库",
	}
	task := newPendingEmbyItemRefreshTask(target, 10, now-100)
	task.DeadlineAt = now + 120
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建 item 刷新任务失败: %v", err)
	}

	if err := upsertEmbyItemRefreshTask(target, 11, now); err != nil {
		t.Fatalf("更新 item 刷新任务失败: %v", err)
	}

	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if updated.DeadlineAt != task.DeadlineAt {
		t.Fatalf("活动周期新增同步目录后 deadline_at=%d，期望保留 %d", updated.DeadlineAt, task.DeadlineAt)
	}
	if !reflect.DeepEqual(updated.GetSyncPathIds(), []uint{10, 11}) {
		t.Fatalf("sync_path_ids=%v，期望 [10 11]", updated.GetSyncPathIds())
	}
}

func TestUpsertEmbyLibraryRefreshTaskStartsNewDeadlineForTerminalStatus(t *testing.T) {
	for _, status := range []string{
		EmbyLibraryRefreshStatusCompleted,
		EmbyLibraryRefreshStatusFailed,
		EmbyLibraryRefreshStatusCancelled,
	} {
		status := status
		t.Run(status, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
			task.Status = status
			task.DeadlineAt = now + 120
			task.SetItemIds([]string{"historical-item"})
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatalf("创建 terminal 刷新任务失败: %v", err)
			}

			if err := upsertEmbyLibraryRefreshTask("lib-movie", "电影库", 10, now); err != nil {
				t.Fatalf("重新提交刷新任务失败: %v", err)
			}
			var updated EmbyLibraryRefreshTask
			if err := db.Db.First(&updated, task.ID).Error; err != nil {
				t.Fatalf("查询刷新任务失败: %v", err)
			}
			if updated.Status != EmbyLibraryRefreshStatusPending {
				t.Fatalf("状态=%s，期望 pending", updated.Status)
			}
			if updated.DeadlineAt != now+DefaultEmbyRefreshMaxWaitSeconds {
				t.Fatalf("terminal 任务 deadline_at=%d，期望 %d", updated.DeadlineAt, now+DefaultEmbyRefreshMaxWaitSeconds)
			}
			if len(updated.GetItemIds()) != 0 {
				t.Fatalf("terminal 任务新周期 item_ids=%v，期望清空", updated.GetItemIds())
			}
		})
	}
}

func TestEmbyRefreshAggregationCoversPendingItemsAcrossSubmissions(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影库", SyncPathId: 10})
	makeTarget := func(index int) EmbyRefreshTarget {
		return EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("item-%02d", index),
			ItemName:            "电影",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}
	}
	for i := 0; i < 6; i++ {
		if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{makeTarget(i)}); err != nil {
			t.Fatalf("第一批提交刷新目标失败: %v", err)
		}
	}
	for i := 6; i < 10; i++ {
		if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{makeTarget(i)}); err != nil {
			t.Fatalf("第二批提交刷新目标失败: %v", err)
		}
	}

	now := nowUnix()
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusPending).
		Updates(map[string]interface{}{"refresh_after_at": now - 1}).Error; err != nil {
		t.Fatalf("设置测试防抖时间失败: %v", err)
	}
	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调跨批次刷新任务失败: %v", err)
	}

	var libraryTask EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyLibraryRefreshTaskKey("lib-movie")).First(&libraryTask).Error; err != nil {
		t.Fatalf("跨批次达到阈值后应创建 library 任务: %v", err)
	}
	if libraryTask.TargetType != EmbyLibraryRefreshTargetTypeLibrary {
		t.Fatalf("任务类型=%s，期望 library", libraryTask.TargetType)
	}
}

func TestReconcilePendingEmbyRefreshTasksCancelsExpiredLibraryBeforeAggregation(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	libraryTask := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
	libraryTask.DeadlineAt = now - 1
	if err := db.Db.Create(libraryTask).Error; err != nil {
		t.Fatalf("创建过期媒体库刷新任务失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		itemTask := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              fmt.Sprintf("item-%d", i),
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		}, 10, now)
		itemTask.RefreshAfterAt = now - 1
		if err := db.Db.Create(itemTask).Error; err != nil {
			t.Fatalf("创建 item 刷新任务失败: %v", err)
		}
	}

	if err := reconcilePendingEmbyRefreshTasks(now); err != nil {
		t.Fatalf("协调 pending 刷新任务失败: %v", err)
	}

	var updatedLibrary EmbyLibraryRefreshTask
	if err := db.Db.First(&updatedLibrary, libraryTask.ID).Error; err != nil {
		t.Fatalf("查询过期媒体库刷新任务失败: %v", err)
	}
	if updatedLibrary.Status != EmbyLibraryRefreshStatusCancelled {
		t.Fatalf("过期媒体库任务状态=%s，期望 cancelled", updatedLibrary.Status)
	}
	var pendingItems int64
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).
		Where("target_type = ? AND status = ?", EmbyLibraryRefreshTargetTypeItem, EmbyLibraryRefreshStatusPending).
		Count(&pendingItems).Error; err != nil {
		t.Fatalf("统计 pending item 失败: %v", err)
	}
	if pendingItems != 3 {
		t.Fatalf("pending item 数量=%d，期望 3", pendingItems)
	}
}

func TestCancelAbsorbedEmbyItemTasksRequiresAllItemsPending(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	pendingTask := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:        EmbyRefreshTargetTypeItem,
		ItemID:            "pending-item",
		FallbackLibraryId: "lib-movie",
	}, 10, now)
	completedTask := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:        EmbyRefreshTargetTypeItem,
		ItemID:            "completed-item",
		FallbackLibraryId: "lib-movie",
	}, 10, now)
	completedTask.Status = EmbyLibraryRefreshStatusCompleted
	if err := db.Db.Create([]*EmbyLibraryRefreshTask{pendingTask, completedTask}).Error; err != nil {
		t.Fatalf("创建 item 刷新任务失败: %v", err)
	}

	err := db.Db.Transaction(func(tx *gorm.DB) error {
		return cancelAbsorbedEmbyItemTasksWithDB(tx, []*EmbyLibraryRefreshTask{pendingTask, completedTask}, "lib-movie")
	})
	if !errors.Is(err, errEmbyRefreshTasksChanged) {
		t.Fatalf("取消数量不一致错误=%v，期望 errEmbyRefreshTasksChanged", err)
	}

	var updatedPending EmbyLibraryRefreshTask
	if err := db.Db.First(&updatedPending, pendingTask.ID).Error; err != nil {
		t.Fatalf("查询 pending item 失败: %v", err)
	}
	if updatedPending.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("事务回滚后 pending item 状态=%s，期望 pending", updatedPending.Status)
	}
}

func TestFindActivePendingEmbyItemRefreshTasksExcludesHistoryAndUnresolved(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	active := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:        EmbyRefreshTargetTypeItem,
		ItemID:            "active",
		FallbackLibraryId: "lib-movie",
	}, 10, now)
	unresolved := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     "unresolved",
	}, 10, now)
	expired := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:        EmbyRefreshTargetTypeItem,
		ItemID:            "expired",
		FallbackLibraryId: "lib-movie",
	}, 10, now-100)
	expired.DeadlineAt = now - 1
	completed := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{
		TargetType:        EmbyRefreshTargetTypeItem,
		ItemID:            "completed",
		FallbackLibraryId: "lib-movie",
	}, 10, now)
	completed.Status = EmbyLibraryRefreshStatusCompleted
	libraryTask := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now)
	if err := db.Db.Create([]*EmbyLibraryRefreshTask{active, unresolved, expired, completed, libraryTask}).Error; err != nil {
		t.Fatalf("创建刷新任务失败: %v", err)
	}

	tasks, err := findActivePendingEmbyItemRefreshTasksWithDB(db.Db, now, []string{"lib-movie"})
	if err != nil {
		t.Fatalf("查询有效 pending item 失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != active.TaskKey {
		t.Fatalf("有效 pending item=%+v，期望只有 %s", tasks, active.TaskKey)
	}
}

func TestEmbyRefreshTaskMutationUsesProcessLockForSQLite(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	if !shouldUseEmbyRefreshTaskProcessLock(db.Db) {
		t.Fatal("SQLite 应使用 Emby 刷新任务进程级写锁")
	}
}

func TestUpdatePendingEmbyRefreshTaskCheckResultDoesNotOverwriteChangedTask(t *testing.T) {
	tests := []struct {
		name             string
		checkErr         error
		concurrentStatus string
	}{
		{name: "暂不可执行时丢弃旧检查结果"},
		{name: "检查失败时丢弃旧错误", checkErr: errors.New("旧检查失败")},
		{name: "状态已变为refreshing时丢弃旧检查结果", checkErr: errors.New("旧检查失败"), concurrentStatus: EmbyLibraryRefreshStatusRefreshing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
			task.RefreshAfterAt = now - 1
			task.DeadlineAt = now + 100
			task.Error = "旧错误"
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatalf("创建媒体库刷新任务失败: %v", err)
			}
			snapshot := *task

			if tt.concurrentStatus != "" {
				if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("id = ?", task.ID).
					Updates(map[string]interface{}{"status": tt.concurrentStatus, "error": ""}).Error; err != nil {
					t.Fatalf("模拟并发状态变化失败: %v", err)
				}
			} else {
				updatedTask := *task
				updatedTask.SetSyncPathIds([]uint{10, 11})
				if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
					"last_event_at":     now + 1,
					"refresh_after_at":  now + DefaultEmbyRefreshDebounceSeconds + 1,
					"deadline_at":       now + 200,
					"sync_path_ids_str": updatedTask.SyncPathIdsStr,
					"error":             "",
				}).Error; err != nil {
					t.Fatalf("模拟并发新事件失败: %v", err)
				}
			}

			if err := updatePendingEmbyRefreshTaskCheckResult(&snapshot, now, tt.checkErr); err != nil {
				t.Fatalf("保存扫描检查结果失败: %v", err)
			}

			var current EmbyLibraryRefreshTask
			if err := db.Db.First(&current, task.ID).Error; err != nil {
				t.Fatalf("查询媒体库刷新任务失败: %v", err)
			}
			if tt.concurrentStatus != "" {
				if current.Status != tt.concurrentStatus {
					t.Fatalf("并发状态=%s，期望 %s", current.Status, tt.concurrentStatus)
				}
			} else {
				if current.LastEventAt != now+1 || current.RefreshAfterAt != now+DefaultEmbyRefreshDebounceSeconds+1 || current.DeadlineAt != now+200 {
					t.Fatalf("新事件时间被旧快照覆盖: last_event_at=%d refresh_after_at=%d deadline_at=%d", current.LastEventAt, current.RefreshAfterAt, current.DeadlineAt)
				}
				if !reflect.DeepEqual(current.GetSyncPathIds(), []uint{10, 11}) {
					t.Fatalf("新同步目录被旧快照覆盖: sync_path_ids=%v", current.GetSyncPathIds())
				}
			}
			if current.LastCheckedAt != 0 || current.Error != "" {
				t.Fatalf("旧检查结果不应写入已变化任务: last_checked_at=%d error=%q", current.LastCheckedAt, current.Error)
			}
		})
	}
}

func TestUpdatePendingEmbyRefreshTaskCheckResultUpdatesCurrentTask(t *testing.T) {
	tests := []struct {
		name      string
		checkErr  error
		wantError string
	}{
		{name: "暂不可执行时只记录检查时间", wantError: "原错误"},
		{name: "检查失败时记录错误", checkErr: errors.New("查询下载任务失败"), wantError: "查询下载任务失败"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			now := nowUnix()
			task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
			task.RefreshAfterAt = now - 1
			task.Error = "原错误"
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatalf("创建媒体库刷新任务失败: %v", err)
			}

			if err := updatePendingEmbyRefreshTaskCheckResult(task, now, tt.checkErr); err != nil {
				t.Fatalf("保存扫描检查结果失败: %v", err)
			}

			var current EmbyLibraryRefreshTask
			if err := db.Db.First(&current, task.ID).Error; err != nil {
				t.Fatalf("查询媒体库刷新任务失败: %v", err)
			}
			if current.LastCheckedAt != now || current.Error != tt.wantError {
				t.Fatalf("检查结果 last_checked_at=%d error=%q，期望 %d/%q", current.LastCheckedAt, current.Error, now, tt.wantError)
			}
		})
	}
}

func TestMarkEmbyRefreshTaskCancelledDoesNotCancelRenewedDeadline(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
	task.RefreshAfterAt = now - 10
	task.DeadlineAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建已过期刷新任务失败: %v", err)
	}
	snapshot := *task

	newDeadlineAt := now + 120
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"last_event_at":    now + 1,
		"refresh_after_at": now + DefaultEmbyRefreshDebounceSeconds + 1,
		"deadline_at":      newDeadlineAt,
	}).Error; err != nil {
		t.Fatalf("模拟新事件续期失败: %v", err)
	}

	if err := markEmbyRefreshTaskCancelled(&snapshot, "等待超过最大时长，取消刷新"); err != nil {
		t.Fatalf("取消旧快照刷新任务失败: %v", err)
	}

	var current EmbyLibraryRefreshTask
	if err := db.Db.First(&current, task.ID).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if current.Status != EmbyLibraryRefreshStatusPending || current.DeadlineAt != newDeadlineAt {
		t.Fatalf("续期任务状态=%s deadline_at=%d，期望 pending/%d", current.Status, current.DeadlineAt, newDeadlineAt)
	}
	if current.LastCheckedAt != 0 || current.Error != "" {
		t.Fatalf("旧取消结果不应写入续期任务: last_checked_at=%d error=%q", current.LastCheckedAt, current.Error)
	}
}

func TestRefreshEmbyLibraryTaskDoesNotClaimExtendedDebounceTask(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建媒体库刷新任务失败: %v", err)
	}
	if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Where("id = ?", task.ID).
		Update("refresh_after_at", now+30).Error; err != nil {
		t.Fatalf("模拟并发事件延长防抖失败: %v", err)
	}

	if err := refreshEmbyLibraryTask(task); err != nil {
		t.Fatalf("领取媒体库刷新任务失败: %v", err)
	}
	var updated EmbyLibraryRefreshTask
	if err := db.Db.First(&updated, task.ID).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if updated.Status != EmbyLibraryRefreshStatusPending {
		t.Fatalf("防抖已延长任务状态=%s，期望保持 pending", updated.Status)
	}
	if requests.Load() != 0 {
		t.Fatalf("防抖已延长任务不应请求 Emby，实际请求=%d", requests.Load())
	}
}

func TestRefreshEmbyLibraryTaskRegistersBeforeTaskGateBlocks(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	taskgate.AllowNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	requestStarted := make(chan struct{})
	allowRequest := make(chan struct{})
	var requestStartedOnce sync.Once
	var allowRequestOnce sync.Once
	releaseRequest := func() {
		allowRequestOnce.Do(func() { close(allowRequest) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestStartedOnce.Do(func() { close(requestStarted) })
		<-allowRequest
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-movie", "电影库", []uint{10}, now-100)
	task.RefreshAfterAt = now - 1
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建待刷新任务失败: %v", err)
	}

	claimUpdateStarted := make(chan struct{})
	allowClaimCommit := make(chan struct{})
	var claimUpdateOnce sync.Once
	var allowClaimOnce sync.Once
	releaseClaim := func() {
		allowClaimOnce.Do(func() { close(allowClaimCommit) })
	}
	callbackName := "test:pause-refresh-claim"
	if err := db.Db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != (&EmbyLibraryRefreshTask{}).TableName() {
			return
		}
		claimUpdateOnce.Do(func() {
			close(claimUpdateStarted)
			<-allowClaimCommit
		})
	}); err != nil {
		t.Fatalf("注册刷新任务 claim 暂停回调失败: %v", err)
	}
	refreshDone := make(chan error, 1)
	refreshFinished := false
	defer func() {
		releaseClaim()
		releaseRequest()
		if !refreshFinished {
			select {
			case <-refreshDone:
			case <-time.After(time.Second):
				t.Error("刷新任务未在清理时退出")
			}
		}
		if err := db.Db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("移除刷新任务 claim 暂停回调失败: %v", err)
		}
	}()

	go func() {
		refreshDone <- refreshEmbyLibraryTask(task)
	}()
	select {
	case <-claimUpdateStarted:
	case <-time.After(time.Second):
		t.Fatal("刷新任务未进入 claim 更新")
	}

	blockDone := make(chan struct{})
	go func() {
		taskgate.BlockNewTasks()
		close(blockDone)
	}()
	deadline := time.Now().Add(time.Second)
	for !taskgate.IsBlocked() {
		if time.Now().After(deadline) {
			t.Fatal("BlockNewTasks() 未关闭任务准入")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-blockDone:
		t.Fatal("刷新任务 claim 提交前 BlockNewTasks() 不得返回")
	default:
	}

	releaseClaim()
	select {
	case <-blockDone:
	case <-time.After(time.Second):
		t.Fatal("刷新任务 claim 提交后 BlockNewTasks() 未返回")
	}
	var registered EmbyLibraryRefreshTask
	if err := db.Db.First(&registered, task.ID).Error; err != nil {
		t.Fatalf("读取已领取刷新任务失败: %v", err)
	}
	if registered.Status != EmbyLibraryRefreshStatusRefreshing {
		t.Fatalf("BlockNewTasks() 返回时刷新任务状态 = %s，期望 refreshing", registered.Status)
	}

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("已领取刷新任务未请求 Emby")
	}
	releaseRequest()
	if err := <-refreshDone; err != nil {
		t.Fatalf("刷新任务执行失败: %v", err)
	}
	refreshFinished = true
}
