package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	v115open "qmediasync/internal/v115/openapi"
)

type controlledUpload115Runner struct {
	started  chan uint
	release  map[uint]chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

func (runner *controlledUpload115Runner) Upload(_ context.Context, task *DbUploadTask, _ *v115open.OpenClient) (upload115TaskResult, error) {
	select {
	case runner.started <- task.ID:
	case <-runner.stopped:
		return upload115TaskResult{}, nil
	}
	select {
	case <-runner.release[task.ID]:
	case <-runner.stopped:
	}
	return upload115TaskResult{UploadResult: UploadResultRapidUpload}, nil
}

func (runner *controlledUpload115Runner) unblockAll() {
	runner.stopOnce.Do(func() { close(runner.stopped) })
}

func setupUploadQueueTest(t *testing.T, concurrency, taskCount int) (*UQ, *controlledUpload115Runner, []*DbUploadTask) {
	t.Helper()
	oldDB := db.Db
	setupQueueStatusTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		db.Db = oldDB
	})
	if err := db.Db.AutoMigrate(&Account{}); err != nil {
		t.Fatal(err)
	}
	account := &Account{SourceType: SourceType115, Name: "上传队列测试"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(t.TempDir(), "movie.nfo")
	if err := os.WriteFile(localPath, []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &controlledUpload115Runner{
		started: make(chan uint, taskCount+MaxUploadThreads),
		release: make(map[uint]chan struct{}),
		stopped: make(chan struct{}),
	}
	tasks := make([]*DbUploadTask, 0, taskCount)
	for i := range taskCount {
		task := &DbUploadTask{
			AccountId:      account.ID,
			Source:         UploadSourceStrm,
			SourceType:     SourceType115,
			Status:         UploadStatusPending,
			LocalFullPath:  localPath,
			RemoteFullPath: fmt.Sprintf("/remote/%d.nfo", i),
			FileName:       fmt.Sprintf("%d.nfo", i),
		}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		runner.release[task.ID] = make(chan struct{})
	}
	setUpload115RunnerForTesting(t, runner)
	queue := NewUq(concurrency)
	t.Cleanup(func() {
		queue.Stop()
		runner.unblockAll()
		queue.workers.Wait()
	})
	return queue, runner, tasks
}

func expectStartedUploads(t *testing.T, runner *controlledUpload115Runner, want ...uint) {
	t.Helper()
	synctest.Wait()
	var got []uint
	for len(runner.started) > 0 {
		got = append(got, <-runner.started)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("新开始上传的任务 = %v，期望 %v", got, want)
	}
}

func TestUploadQueueConcurrencyChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue, runner, tasks := setupUploadQueueTest(t, 1, 12)
		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner, tasks[0].ID)

		queue.UpdateConcurrency(3)
		expectStartedUploads(t, runner, tasks[1].ID, tasks[2].ID)
		queue.UpdateConcurrency(5)
		expectStartedUploads(t, runner, tasks[3].ID, tasks[4].ID)
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)

		queue.UpdateConcurrency(2)
		expectStartedUploads(t, runner)
		for _, task := range tasks[:3] {
			close(runner.release[task.ID])
			expectStartedUploads(t, runner)
		}
		// 原来的五个任务降至一个后即可补入一个，不必等待整个批次结束。
		close(runner.release[tasks[3].ID])
		expectStartedUploads(t, runner, tasks[5].ID)

		queue.Stop()
		queue.UpdateConcurrency(3)
		if queue.IsRunning() || queue.GetConcurrency() != 3 {
			t.Fatal("暂停时修改并发应保留暂停状态及新配置")
		}
		close(runner.release[tasks[4].ID])
		close(runner.release[tasks[5].ID])
		time.Sleep(2 * time.Second)
		expectStartedUploads(t, runner)

		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner, tasks[6].ID, tasks[7].ID, tasks[8].ID)
		queue.UpdateConcurrency(1)
		for range 10 {
			queue.Stop()
			queue.Start()
		}
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		close(runner.release[tasks[6].ID])
		close(runner.release[tasks[7].ID])
		expectStartedUploads(t, runner)
		close(runner.release[tasks[8].ID])
		expectStartedUploads(t, runner, tasks[9].ID)
	})
}

func TestUploadQueueDiscardsQueryFromStoppedScheduler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue, runner, tasks := setupUploadQueueTest(t, 1, 2)
		queried := make(chan struct{}, 1)
		newQueried := make(chan struct{}, 1)
		releaseQuery, releaseNewQuery := make(chan struct{}), make(chan struct{})
		releaseOld := sync.OnceFunc(func() { close(releaseQuery) })
		releaseNew := sync.OnceFunc(func() { close(releaseNewQuery) })
		var queryCount atomic.Int32
		var stopping sync.WaitGroup
		callbackName := "qms:test_block_upload_queue_query"
		if err := db.Db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*[]*DbUploadTask); ok {
				switch queryCount.Add(1) {
				case 1:
					queried <- struct{}{}
					<-releaseQuery
				case 2:
					newQueried <- struct{}{}
					<-releaseNewQuery
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			releaseOld()
			releaseNew()
			queue.Stop()
			stopping.Wait()
			runner.unblockAll()
			queue.workers.Wait()
			_ = db.Db.Callback().Query().Remove(callbackName)
		}()
		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		if len(queried) != 1 {
			t.Fatal("旧调度协程应正在等待查询结果返回")
		}
		stopping.Go(queue.Stop)
		synctest.Wait()
		if queue.IsRunning() {
			t.Fatal("暂停应先停止派发，再等待旧查询退出")
		}
		queue.UpdateConcurrency(2)
		queue.Start()
		synctest.Wait()
		if len(newQueried) != 1 {
			t.Fatal("新调度查询应已到达独立屏障")
		}
		releaseOld()
		stopping.Wait()
		// 新队列虽已启动，旧查询也不能借用新代次派发任务。
		expectStartedUploads(t, runner)
		releaseNew()
		expectStartedUploads(t, runner, tasks[0].ID, tasks[1].ID)
	})
}

func TestUploadQueueKeepsDistinctReservationsAcrossRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue, runner, tasks := setupUploadQueueTest(t, 2, 6)
		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner, tasks[0].ID, tasks[1].ID)
		// 启动恢复可能在队列启动后重置状态；内存预留仍须覆盖完整在途生命周期。
		if err := UpdateUploadingToPending(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		close(runner.release[tasks[0].ID])
		expectStartedUploads(t, runner, tasks[2].ID)
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		close(runner.release[tasks[1].ID])
		expectStartedUploads(t, runner, tasks[3].ID)
		close(runner.release[tasks[2].ID])
		expectStartedUploads(t, runner, tasks[4].ID)
	})
}

func TestUploadQueueSkipsClearedBufferedTasks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue, runner, tasks := setupUploadQueueTest(t, 1, 2)
		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner, tasks[0].ID)
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		if err := ClearPendingUploadTasks(); err != nil {
			t.Fatal(err)
		}
		close(runner.release[tasks[0].ID])
		expectStartedUploads(t, runner)
		var count int64
		if err := db.Db.Model(&DbUploadTask{}).Where("id = ?", tasks[1].ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("缓冲中的已清空任务不应重新执行或写回数据库")
		}
	})
}

func TestUploadQueueKeepsSlotUntilFinalizeReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue, runner, tasks := setupUploadQueueTest(t, 1, 2)
		finalizing := make(chan struct{}, 1)
		releaseFinalize := make(chan struct{}, 1)
		callbackName := "qms:test_block_upload_completion"
		if err := db.Db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(callbackName, func(tx *gorm.DB) {
			if task, ok := tx.Statement.Dest.(*DbUploadTask); ok && task.ID == tasks[0].ID && task.Status == UploadStatusCompleted {
				finalizing <- struct{}{}
				<-releaseFinalize
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(releaseFinalize)
			queue.Stop()
			runner.unblockAll()
			queue.workers.Wait()
			_ = db.Db.Callback().Update().Remove(callbackName)
		}()
		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner, tasks[0].ID)
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		close(runner.release[tasks[0].ID])
		expectStartedUploads(t, runner)
		if len(finalizing) != 1 {
			t.Fatal("首个上传任务应已进入受控完成处理")
		}
		releaseFinalize <- struct{}{}
		expectStartedUploads(t, runner, tasks[1].ID)
	})
}

func TestUploadClaimsPendingTaskOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, runner, tasks := setupUploadQueueTest(t, 1, 1)
		var uploads sync.WaitGroup
		defer func() {
			runner.unblockAll()
			uploads.Wait()
		}()
		for range MaxUploadThreads {
			copy := *tasks[0]
			uploads.Go(copy.Upload)
		}
		expectStartedUploads(t, runner, tasks[0].ID)
		close(runner.release[tasks[0].ID])
		uploads.Wait()
		// 仍持有 pending 状态的旧副本也不能重传已经完成的任务。
		tasks[0].Upload()
		expectStartedUploads(t, runner)
		var got DbUploadTask
		if err := db.Db.First(&got, tasks[0].ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != UploadStatusCompleted {
			t.Fatalf("任务状态 = %s，期望 completed", got.Status.String())
		}
	})
}

func TestUploadSkipsStalePendingCopies(t *testing.T) {
	for _, status := range []UploadStatus{UploadStatusUploading, UploadStatusCompleted, UploadStatusFailed, UploadStatusCancelled, UploadStatusRemoteCompletedPendingFinalize, UploadStatusRemoteCompletedFinalizing} {
		t.Run(status.String(), func(t *testing.T) {
			setupQueueStatusTestDB(t)
			task := &DbUploadTask{Status: UploadStatusPending, LocalFullPath: "/missing/upload-test.nfo"}
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(task).Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
			task.Status = UploadStatusPending
			task.Upload()
			var got DbUploadTask
			if err := db.Db.First(&got, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.Status != status || got.Error != "" {
				t.Fatalf("旧副本不应改变当前任务：status=%s error=%q", got.Status.String(), got.Error)
			}
		})
	}
}

func TestUploadQueueFinalizesWithoutRetransfer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue, runner, tasks := setupUploadQueueTest(t, 2, 1)
		if err := db.Db.Model(tasks[0]).Updates(map[string]any{
			"status":          UploadStatusRemoteCompletedPendingFinalize,
			"local_full_path": "/missing/upload-test.nfo",
		}).Error; err != nil {
			t.Fatal(err)
		}
		queue.Start()
		time.Sleep(time.Second)
		expectStartedUploads(t, runner)
		var got DbUploadTask
		if err := db.Db.First(&got, tasks[0].ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != UploadStatusCompleted {
			t.Fatalf("收尾任务状态 = %s，期望不重新传输即可完成", got.Status.String())
		}
	})
}

func TestUploadQueueShutdownCancelsFinalizeClaimSQL(t *testing.T) {
	setupUpload115ProcessedTestDB(t)
	task := &DbUploadTask{Status: UploadStatusRemoteCompletedPendingFinalize}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	const callback = "test:block_upload_finalize_claim"
	if err := db.Db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok || tx.Statement.Table != "db_upload_tasks" || updates["status"] != UploadStatusRemoteCompletedFinalizing {
			return
		}
		entered <- struct{}{}
		select {
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		case <-release:
		}
	}); err != nil {
		t.Fatal(err)
	}
	queue := NewUq(1)
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		db.Db.Callback().Update().Remove(callback)
	})
	queue.Start()
	queue.signal()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("上传未进入收尾领取")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := queue.Shutdown(ctx); err != nil {
		t.Fatalf("最终停止没有取消收尾领取SQL：%v", err)
	}
	var stored DbUploadTask
	if err := db.Db.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != UploadStatusRemoteCompletedPendingFinalize {
		t.Fatalf("取消收尾不能重新传输文件：%s", stored.Status.String())
	}
}

type upload115HTTPRunner struct{ client *v115open.OpenClient }

func (runner upload115HTTPRunner) Upload(ctx context.Context, task *DbUploadTask, _ *v115open.OpenClient) (upload115TaskResult, error) {
	return open115UploadRunner{}.Upload(ctx, task, runner.client)
}

type upload115HTTPTransport func(*http.Request) (*http.Response, error)

func (transport upload115HTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestUploadQueueShutdownPreserves115ResumeCheckpoint(t *testing.T) {
	setupUpload115ProcessedTestDB(t)
	if err := db.Db.AutoMigrate(&Account{}); err != nil {
		t.Fatal(err)
	}
	account := &Account{SourceType: SourceType115, Name: "续传关闭测试"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(t.TempDir(), "movie.nfo")
	if err := os.WriteFile(localPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := buildUpload115LocalFileInfo(localPath, "movie.nfo")
	if err != nil {
		t.Fatal(err)
	}
	task := &DbUploadTask{
		AccountId: account.ID, Source: UploadSourceStrm, SourceType: SourceType115,
		Status: UploadStatusPending, LocalFullPath: localPath, FileName: info.FileName,
		RemoteFullPath: "/remote/movie.nfo", RemotePathId: "100", UploadedBytes: 512,
		RetryCount: 1,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	session := &UploadSession{
		UploadTaskId: task.ID, AccountId: account.ID, LocalFullPath: localPath,
		FileName: info.FileName, FileSize: info.FileSize, LocalMtime: info.LocalMtime,
		LocalSignature: info.LocalSignature, FileSha1: info.FileSha1, Preid: info.Preid,
		ParentFileId: "100", PickCode: "pick-1", Bucket: "bucket-1", Object: "object-1",
		UploadId: "upload-1", PartSize: 256, TotalParts: 4, UploadedBytes: 512,
		UploadedParts: 2, LastPartNumber: 2, LastPartEtag: "etag-2",
		Status: UploadSessionStatusMultipart, ResumeState: UploadResumeStateNewSession,
	}
	if err := session.Save(); err != nil {
		t.Fatal(err)
	}
	resuming, requestDone, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/open/folder/get_info":
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"state":true,"data":{"file_id":"100"}}`)
			} else {
				fmt.Fprint(w, `{"state":true,"data":{}}`)
			}
		case "/open/upload/resume":
			if r.Form.Get("pick_code") != "pick-1" {
				t.Errorf("续传请求未复用原 PickCode：%q", r.Form.Get("pick_code"))
			}
			close(resuming)
			defer close(requestDone)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			t.Errorf("取消续传不应重新初始化上传：%s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := v115open.NewClient(account.ID, "", "test-token", "")
	client.SetTransport(upload115HTTPTransport(func(request *http.Request) (*http.Response, error) {
		request = request.Clone(request.Context())
		request.URL.Scheme, request.URL.Host = serverURL.Scheme, serverURL.Host
		return transport.RoundTrip(request)
	}))
	setUpload115RunnerForTesting(t, upload115HTTPRunner{client: client})
	queue := NewUq(1)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		unblock()
		server.Close()
		transport.CloseIdleConnections()
	})
	queue.Start()
	queue.signal()
	select {
	case <-resuming:
	case <-time.After(3 * time.Second):
		t.Fatal("上传未进入真实续传请求")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := queue.Shutdown(ctx); err != nil {
		t.Fatalf("关闭队列未取消续传请求：%v", err)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("续传 HTTP 请求仍未退出")
	}
	got, err := GetUploadSessionByUploadTaskId(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UploadId != session.UploadId || got.PartSize != session.PartSize || got.TotalParts != session.TotalParts ||
		got.UploadedBytes != session.UploadedBytes || got.UploadedParts != session.UploadedParts ||
		got.LastPartNumber != session.LastPartNumber || got.LastPartEtag != session.LastPartEtag || got.PickCode != session.PickCode {
		t.Fatalf("正常关闭丢失了有效上传 checkpoint：%+v", got)
	}
	if got.ResumeState != UploadResumeStateResumedSession || got.Status != UploadSessionStatusMultipart {
		t.Fatalf("正常关闭不能将续传会话标为失效：%s/%s", got.ResumeState, got.Status)
	}
	var stored DbUploadTask
	if err := db.Db.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != UploadStatusPending || stored.UploadedBytes != session.UploadedBytes ||
		stored.RetryCount != 1 || stored.Error != "" || stored.StartTime != 0 || stored.EndTime != 0 {
		t.Fatalf("取消上传应保留进度和重试预算回到 pending：%+v", stored)
	}
}
