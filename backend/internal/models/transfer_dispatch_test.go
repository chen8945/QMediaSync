package models

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"

	"gorm.io/gorm"
)

func controlledTransfers(t *testing.T, limit, count int) (*transferDispatch[uint], chan uint, map[uint]chan struct{}) {
	t.Helper()
	var mutex sync.Mutex
	pending := make(map[uint]bool)
	release := make(map[uint]chan struct{})
	for id := uint(1); id <= uint(count); id++ {
		pending[id] = true
		release[id] = make(chan struct{})
	}
	started := make(chan uint, count)
	dispatch := newTransferDispatch(limit, 100*time.Millisecond, func(id uint) uint { return id },
		func(_ context.Context, limit int, excluded []uint) ([]uint, error) {
			mutex.Lock()
			defer mutex.Unlock()
			var ids []uint
			for id := uint(1); id <= uint(count) && len(ids) < limit; id++ {
				if pending[id] && !slices.Contains(excluded, id) {
					ids = append(ids, id)
				}
			}
			return ids, nil
		},
		func(ctx context.Context, id uint) {
			mutex.Lock()
			delete(pending, id)
			mutex.Unlock()
			started <- id
			select {
			case <-release[id]:
			case <-ctx.Done():
			}
		}, nil,
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatch.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return dispatch, started, release
}

func TestTransferDispatchRechecksLimitAfterPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dispatch, started, release := controlledTransfers(t, 3, 5)
		prepared := make(chan struct{}, 3)
		finishPreparation := make(chan struct{})
		dispatch.prepare = func(ctx context.Context) error {
			prepared <- struct{}{}
			select {
			case <-finishPreparation:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		dispatch.Start()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if len(prepared) != 3 {
			t.Fatal("三个候选应正在等待准备")
		}
		dispatch.UpdateConcurrency(1)
		close(finishPreparation)
		synctest.Wait()
		if len(started) != 1 {
			t.Fatalf("旧预留不能越过新上限：started=%d", len(started))
		}
		first := <-started
		dispatch.Stop()
		close(release[first])
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("暂停后不得补位")
		}
	})
}

func TestTransferDispatchTemporaryPausePreservesUserIntent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dispatch, started, _ := controlledTransfers(t, 1, 2)
		dispatch.Start()
		releaseA, releaseB := dispatch.Suspend(), dispatch.Suspend()
		releaseA()
		releaseA()
		if dispatch.IsRunning() {
			t.Fatal("重叠暂停不能提前恢复")
		}
		dispatch.Stop()
		dispatch.UpdateConcurrency(2)
		releaseB()
		if dispatch.IsRunning() {
			t.Fatal("释放临时暂停不能覆盖用户暂停")
		}
		dispatch.Start()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if len(started) != 2 {
			t.Fatalf("恢复应按最新并发派发：%d", len(started))
		}
	})
}

func TestTransferDispatchShutdownReportsUnfinishedQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queried, release := make(chan struct{}), make(chan struct{})
		started := false
		dispatch := newTransferDispatch(1, 100*time.Millisecond, func(id uint) uint { return id },
			func(context.Context, int, []uint) ([]uint, error) { close(queried); <-release; return []uint{1}, nil },
			func(context.Context, uint) { started = true }, nil,
		)
		dispatch.Start()
		<-queried
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := dispatch.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("慢查询应明确尚未退出：%v", err)
		}
		dispatch.Start()
		if dispatch.IsRunning() {
			t.Fatal("最终关闭后不能重新准入")
		}
		close(release)
		if err := dispatch.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if started {
			t.Fatal("迟到查询不得执行任务")
		}
	})
}

func TestDownloadQueueRetainsStartQPS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := NewDq(1)
		var next uint
		queue.candidates = func(context.Context, int, []uint) ([]*DbDownloadTask, error) {
			next++
			if next > 3 {
				return nil, nil
			}
			return []*DbDownloadTask{{BaseModel: BaseModel{ID: next}}}, nil
		}
		starts := make(chan time.Time, 3)
		queue.run = func(context.Context, *DbDownloadTask) { starts <- time.Now() }
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := queue.Shutdown(ctx); err != nil {
				t.Error(err)
			}
		})
		queue.Start()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		first := <-starts
		time.Sleep(900 * time.Millisecond)
		synctest.Wait()
		if len(starts) != 0 {
			t.Fatal("并发空闲不能绕过启动QPS")
		}
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		second := <-starts
		if second.Sub(first) < time.Second {
			t.Fatalf("启动间隔不足：%s", second.Sub(first))
		}
	})
}

func TestTransferQueuesShutdownCancelsIdleRetrySQL(t *testing.T) {
	for _, kind := range []string{"download", "upload", "download-config", "download-account", "upload-account"} {
		t.Run(kind, func(t *testing.T) {
			setupMediaDownloadTestDB(t)
			var accountID uint
			if kind == "download-account" || kind == "upload-account" {
				if err := db.Db.AutoMigrate(&Account{}); err != nil {
					t.Fatal(err)
				}
				account := &Account{SourceType: SourceTypeOpenList}
				if err := db.Db.Create(account).Error; err != nil {
					t.Fatal(err)
				}
				accountID = account.ID
			}
			var queue interface {
				Start()
				IsRunning() bool
				UpdateConcurrency(int)
				Shutdown(context.Context) error
			}
			if kind == "upload" || kind == "upload-account" {
				task := &DbUploadTask{Status: UploadStatusFailed, SourceType: SourceTypeLocal}
				if accountID != 0 {
					task.AccountId, task.Status, task.SourceType = accountID, UploadStatusPending, SourceTypeOpenList
					task.LocalFullPath = filepath.Join(t.TempDir(), "movie.nfo")
					if err := os.WriteFile(task.LocalFullPath, []byte("movie"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Db.Create(task).Error; err != nil {
					t.Fatal(err)
				}
				upload := NewUq(1)
				upload.interval = 10 * time.Millisecond
				queue = upload
			} else {
				task := &DbDownloadTask{Status: DownloadStatusFailed, Source: DownloadSourceLocalFile, SourceType: SourceTypeLocal}
				if accountID != 0 {
					task.AccountId, task.Status, task.Source, task.SourceType = accountID, DownloadStatusPending, DownloadSourceStrm, SourceTypeOpenList
					task.LocalFullPath = filepath.Join(t.TempDir(), "movie.nfo")
				}
				if err := db.Db.Create(task).Error; err != nil {
					t.Fatal(err)
				}
				download := NewDq(1)
				download.interval = 10 * time.Millisecond
				queue = download
			}
			queried, release := make(chan struct{}, 1), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			const callback = "test:block_transfer_retry"
			if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				blocked := false
				switch tx.Statement.Dest.(type) {
				case *[]DbUploadTask:
					blocked = kind == "upload"
				case *[]DbDownloadTask:
					blocked = kind == "download"
				case *EmbyConfig:
					blocked = kind == "download-config"
				case *Account:
					blocked = kind == "download-account" || kind == "upload-account"
				}
				if !blocked {
					return
				}
				select {
				case queried <- struct{}{}:
				default:
				}
				select {
				case <-tx.Statement.Context.Done():
					tx.AddError(tx.Statement.Context.Err())
				case <-release:
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				unblock()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := queue.Shutdown(ctx); err != nil {
					t.Error(err)
				}
				db.Db.Callback().Query().Remove(callback)
			})
			queue.Start()
			select {
			case <-queried:
			case <-time.After(3 * time.Second):
				t.Fatal("队列没有进入受阻查询")
			}
			if !queue.IsRunning() {
				t.Fatal("慢SQL不能阻塞运行状态读取")
			}
			queue.UpdateConcurrency(2)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := queue.Shutdown(ctx); err != nil {
				t.Fatalf("最终停止应取消受阻SQL并等待真实退出：%v", err)
			}
			if queue.IsRunning() {
				t.Fatal("最终停止仍在准入")
			}
		})
	}
}
