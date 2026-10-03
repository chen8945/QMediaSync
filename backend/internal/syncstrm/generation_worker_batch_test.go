package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestStrmGenerationWorkerBatchContinuation(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		count       int
		fault       string
		wantBatches int
	}{
		{"backlog", 11, "", 3},
		{"slow backlog", 11, "slow", 4},
		{"short", 3, "", 1},
		{"idle", 0, "", 1},
		{"terminal failures", 11, "generate", 3},
		{"finalizing error on full batch", 5, "finalizing", 1},
		{"claim error", 5, "claim", 1},
		{"query error", 5, "query", 1},
		{"cancel after full batch", 10, "cancel", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account, path := setupStrmGenerationServiceTestDB(t)
				sqlDB, err := db.Db.DB()
				if err != nil {
					t.Fatal(err)
				}
				defer sqlDB.Close()
				tasks := make([]*models.StrmGenerationTask, 0, scenario.count)
				for i := 0; i < scenario.count; i++ {
					task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
						Source:     models.StrmGenerationSourceUploadCompleted,
						TaskType:   models.StrmGenerationTaskTypeFile,
						SyncPathId: path.ID, AccountId: account.ID,
						FileId: fmt.Sprint(i), PickCode: fmt.Sprint(i), ParentId: "root",
						Path: "/remote", FileName: fmt.Sprintf("movie%d.mkv", i),
						FileSize: 1024, Mtime: 1, Sha1: "test-sha",
					})
					if err != nil {
						t.Fatal(err)
					}
					tasks = append(tasks, task)
				}
				if scenario.fault == "finalizing" {
					if err := db.Db.Model(tasks[4]).Update("status", models.StrmGenerationStatusFinalizing).Error; err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if scenario.fault == "cancel" {
					if err := db.Db.Model(tasks[4]).Update("upload_task_id", 42).Error; err != nil {
						t.Fatal(err)
					}
					oldCleanup := cleanupSourceAfterStrmSuccess
					defer func() { cleanupSourceAfterStrmSuccess = oldCleanup }()
					cleanupSourceAfterStrmSuccess = func(uint) error { cancel(); return nil }
				}
				service := NewStrmGenerationService()
				service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) { return true, nil }
				service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
				service.acquireRefreshSubmission = func(context.Context) (func(), error) { return func() {}, nil }
				service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
					return nil, errors.New("unexpected remote request")
				}
				writes := 0
				var urls []string
				var paths []string
				write := service.processStrmFile
				service.processStrmFile = func(syncer *SyncStrm, file *SyncFileCache) error {
					writes++
					if scenario.fault == "generate" {
						return errors.New("write failed")
					}
					urls = append(urls, syncer.Config.StrmBaseUrl)
					paths = append(paths, file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath))
					if writes == 1 {
						if scenario.fault == "slow" {
							time.Sleep(6 * time.Second)
						}
						if err := db.Db.Model(path).Update("strm_base_url", "http://new-config").Error; err != nil {
							return err
						}
					}
					return write(syncer, file)
				}
				var batches atomic.Int64
				if err := db.Db.Callback().Query().Before("gorm:query").Register("worker_batch_query", func(tx *gorm.DB) {
					if _, ok := tx.Statement.Dest.(*[]*models.StrmGenerationTask); !ok {
						return
					}
					if _, ok := tx.Statement.Clauses["LIMIT"]; !ok {
						return
					}
					batches.Add(1)
					if scenario.fault == "query" {
						tx.AddError(errors.New("query failed"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Callback().Update().Before("gorm:update").Register("worker_batch_update", func(tx *gorm.DB) {
					task, ok := tx.Statement.Model.(*models.StrmGenerationTask)
					if !ok {
						return
					}
					var status any = task.Status
					if values, ok := tx.Statement.Dest.(map[string]any); ok {
						status = values["status"]
					}
					if scenario.fault == "claim" && status == models.StrmGenerationStatusRunning {
						tx.AddError(errors.New("claim failed"))
					}
					if scenario.fault == "finalizing" && task.ID == tasks[4].ID && status == models.StrmGenerationStatusCompleted {
						tx.AddError(errors.New("completion failed"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				done := make(chan struct{})
				go func() { defer close(done); runStrmGenerationWorker(ctx, service) }()
				synctest.Wait()
				elapsed := time.Duration(0)
				if scenario.fault == "slow" {
					// 慢批跨过第一个定时点；积压清空后会消费已到期的检查。
					elapsed = 6 * time.Second
					time.Sleep(elapsed)
					synctest.Wait()
				}
				if int(batches.Load()) != scenario.wantBatches {
					t.Fatalf("before tick: batches=%d want=%d", batches.Load(), scenario.wantBatches)
				}
				if time.Since(start) != elapsed {
					t.Fatalf("batches took %v", time.Since(start))
				}
				if scenario.fault == "cancel" {
					select {
					case <-done:
					default:
						t.Fatal("worker did not stop")
					}
					if writes != 5 {
						t.Fatalf("writes after cancellation=%d", writes)
					}
					return
				}
				if scenario.fault == "" || scenario.fault == "slow" || scenario.fault == "generate" {
					if writes != scenario.count {
						t.Fatalf("writes=%d want=%d", writes, scenario.count)
					}
					for i, task := range tasks {
						saved, err := models.GetStrmGenerationTaskByID(task.ID)
						if err != nil {
							t.Fatal(err)
						}
						wantStatus := models.StrmGenerationStatusCompleted
						if scenario.fault == "generate" {
							wantStatus = models.StrmGenerationStatusFailed
						}
						if saved.Status != wantStatus {
							t.Fatalf("task %d: status=%s", i, saved.Status)
						}
						if scenario.fault == "generate" {
							continue
						}
						wantURL := "http://qms.local"
						if i >= 5 {
							wantURL = "http://new-config"
						}
						if urls[i] != wantURL {
							t.Fatalf("task %d config=%s want=%s", i, urls[i], wantURL)
						}
						data, err := os.ReadFile(paths[i])
						if err != nil || !strings.HasPrefix(string(data), wantURL) {
							t.Fatalf("task %d STRM=%q err=%v", i, data, err)
						}
					}
				}
				time.Sleep(strmGenerationWorkerInterval - elapsed%strmGenerationWorkerInterval - time.Nanosecond)
				synctest.Wait()
				if int(batches.Load()) != scenario.wantBatches {
					t.Fatalf("retried before tick: %d", batches.Load())
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if int(batches.Load()) != scenario.wantBatches+1 {
					t.Fatalf("after tick: batches=%d", batches.Load())
				}
				cancel()
				<-done
				t.Logf("after %v: %d batches, %d file attempts", elapsed, scenario.wantBatches, writes)
			})
		})
	}
}
