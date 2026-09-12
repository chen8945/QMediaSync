package models

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
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
	for i := 0; i < taskCount; i++ {
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
		releaseQuery := make(chan struct{}, 1)
		var firstQuery sync.Once
		var stopping sync.WaitGroup
		callbackName := "qms:test_block_upload_queue_query"
		if err := db.Db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*[]*DbUploadTask); ok {
				firstQuery.Do(func() {
					queried <- struct{}{}
					<-releaseQuery
				})
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(releaseQuery)
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
		releaseQuery <- struct{}{}
		stopping.Wait()
		// 新队列虽已启动，旧查询也不能借用新代次派发任务。
		expectStartedUploads(t, runner)
		time.Sleep(time.Second)
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
