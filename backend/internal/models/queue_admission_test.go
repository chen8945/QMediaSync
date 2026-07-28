package models

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/taskgate"

	"gorm.io/gorm"
)

func TestTaskAdmissionGatePreventsTransferQueueStart(t *testing.T) {
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	downloadQueue := NewDq(1)
	downloadQueue.Start()
	if downloadQueue.IsRunning() {
		t.Fatal("任务准入关闭时下载队列不能启动")
	}

	uploadQueue := NewUq(1)
	uploadQueue.Start()
	if uploadQueue.IsRunning() {
		t.Fatal("任务准入关闭时上传队列不能启动")
	}
}

func TestTaskAdmissionGatePreventsTransferAndStrmEnqueue(t *testing.T) {
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	if err := AddDownloadTaskFromSyncFile(nil); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("AddDownloadTaskFromSyncFile() error = %v，期望任务准入被拒绝", err)
	}
	if err := AddUploadTaskFromSyncFile(nil); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("AddUploadTaskFromSyncFile() error = %v，期望任务准入被拒绝", err)
	}
	if _, err := EnqueueStrmGenerationTask(nil); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("EnqueueStrmGenerationTask() error = %v，期望任务准入被拒绝", err)
	}
}

func TestTaskAdmissionGatePreventsDirectoryMonitorUploadPersistence(t *testing.T) {
	setupDirectoryUploadRuleTestDB(t)
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	if err := AddDirectoryMonitorUploadTask(&DbUploadTask{}); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("AddDirectoryMonitorUploadTask() error = %v，期望任务准入被拒绝", err)
	}
	if err := SaveDirectoryMonitorUploadTaskWithDB(db.Db, &DbUploadTask{}); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("SaveDirectoryMonitorUploadTaskWithDB() error = %v，期望任务准入被拒绝", err)
	}

	var total int64
	if err := db.Db.Model(&DbUploadTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计目录监控上传任务失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("任务准入关闭时不应写入目录监控上传任务，实际数量 = %d", total)
	}
}

func TestTaskAdmissionGateWaitsForTransferTaskCreateCommit(t *testing.T) {
	tests := []struct {
		name   string
		invoke func() error
	}{
		{
			name: "同步文件下载任务",
			invoke: func() error {
				return AddDownloadTaskFromSyncFile(&SyncFile{
					BaseModel:     BaseModel{ID: 1},
					SourceType:    SourceType115,
					FileId:        "download-file",
					Path:          "/remote",
					FileName:      "movie.mkv",
					LocalFilePath: "/library/movie.mkv",
					SyncPath:      &SyncPath{},
				})
			},
		},
		{
			name: "Emby 下载任务",
			invoke: func() error {
				return AddDownloadTaskFromEmbyMedia("https://emby.example/download", "emby-item", "movie.mkv")
			},
		},
		{
			name: "同步文件上传任务",
			invoke: func() error {
				return AddUploadTaskFromSyncFile(&SyncFile{
					BaseModel:  BaseModel{ID: 2},
					SourceType: SourceType115,
					Path:       "/remote",
					FileName:   "movie.mkv",
				})
			},
		},
		{
			name: "目录监控上传任务",
			invoke: func() error {
				return AddDirectoryMonitorUploadTask(&DbUploadTask{
					SourceType:     SourceType115,
					RemoteFullPath: "/remote/directory-movie.mkv",
					FileName:       "directory-movie.mkv",
				})
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			setupQueueStatusTestDB(t)
			assertTaskAdmissionWaitsForCreateCommit(t, testCase.invoke)
		})
	}
}

func TestTaskAdmissionGateWaitsForStrmTaskCreateCommit(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)
	assertTaskAdmissionWaitsForCreateCommit(t, func() error {
		_, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
			Source:      StrmGenerationSourceWebhook,
			SyncPathId:  1,
			RequestHash: "admission-strm-task",
		})
		return err
	})
}

func TestDownloadConcurrencyWorkersAreTracked(t *testing.T) {
	setupQueueStatusTestDB(t)
	taskgate.AllowNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer release()

	queue := NewDq(1)
	queue.running = true
	queue.UpdateConcurrency(2)
	task := &DbDownloadTask{
		Source:            DownloadSourceEmbyMedia,
		RemoteDownloadUrl: server.URL,
		LocalFullPath:     t.TempDir() + "/movie.mkv",
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建下载任务失败: %v", err)
	}
	queue.tasks <- task
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("动态增加的下载 worker 未开始处理任务")
	}

	stopped := make(chan struct{})
	go func() {
		queue.StopAndWait()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("StopAndWait() 不得在动态增加的 worker 退出前返回")
	case <-time.After(20 * time.Millisecond):
	}

	release()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("动态增加的 worker 退出后 StopAndWait() 未返回")
	}
}

func TestTransferQueuesStopAndWaitForDispatchedWorkers(t *testing.T) {
	for _, name := range []string{"下载队列", "上传队列"} {
		t.Run(name, func(t *testing.T) {
			var stop, done func()
			switch name {
			case "下载队列":
				queue := NewDq(1)
				queue.running = true
				queue.workerWG.Add(1)
				stop = queue.StopAndWait
				done = queue.workerWG.Done
			case "上传队列":
				queue := NewUq(1)
				queue.running = true
				queue.workerWG.Add(1)
				stop = queue.StopAndWait
				done = queue.workerWG.Done
			}

			stopped := make(chan struct{})
			go func() {
				stop()
				close(stopped)
			}()
			select {
			case <-stopped:
				t.Fatal("StopAndWait() 在已派发 worker 退出前返回")
			case <-time.After(20 * time.Millisecond):
			}

			done()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("StopAndWait() 未在 worker 退出后返回")
			}
		})
	}
}

func assertTaskAdmissionWaitsForCreateCommit(t *testing.T, invoke func() error) {
	t.Helper()
	taskgate.AllowNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	var createOnce sync.Once
	callbackName := fmt.Sprintf("qms:test_block_task_create_%s", t.Name())
	if err := db.Db.Callback().Create().Before("gorm:create").Register(callbackName, func(*gorm.DB) {
		createOnce.Do(func() {
			close(createStarted)
			<-releaseCreate
		})
	}); err != nil {
		t.Fatalf("注册测试 callback 失败: %v", err)
	}
	t.Cleanup(func() {
		createOnce.Do(func() { close(releaseCreate) })
		_ = db.Db.Callback().Create().Remove(callbackName)
	})

	created := make(chan error, 1)
	go func() {
		created <- invoke()
	}()

	select {
	case <-createStarted:
	case <-time.After(time.Second):
		t.Fatal("等待任务创建进入事务超时")
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
		runtime.Gosched()
	}
	select {
	case <-blockDone:
		t.Fatal("任务创建提交前 BlockNewTasks() 不得返回")
	default:
	}

	close(releaseCreate)
	if err := <-created; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	select {
	case <-blockDone:
	case <-time.After(time.Second):
		t.Fatal("任务创建提交后 BlockNewTasks() 未返回")
	}
}
