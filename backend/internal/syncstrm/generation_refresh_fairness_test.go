package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestStrmRefreshRetryDoesNotPostponeHealthyEndpoint(t *testing.T) {
	for _, stage := range []string{"generate", "finalizing", "parent"} {
		for _, healthy := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
			for _, targetType := range []models.EmbyRefreshTargetType{models.EmbyRefreshTargetTypeLibrary, models.EmbyRefreshTargetTypeItem} {
				t.Run(stage+"/"+string(healthy)+"/"+string(targetType), func(t *testing.T) {
					account, sp := setupStrmGenerationServiceTestDB(t)
					testStrmRefreshRetryHealthyEndpoint(t, account, sp, stage, healthy, targetType)
				})
			}
		}
	}
}

func testStrmRefreshRetryHealthyEndpoint(t *testing.T, account *models.Account, sp *models.SyncPath, stage string, healthy models.MediaServerType, targetType models.EmbyRefreshTargetType) {
	t.Helper()
	if db.Db.Dialector.Name() == "sqlite" {
		sqlDB, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.Db.AutoMigrate(&models.DbDownloadTask{}, &models.EmbyConfig{}, &models.JellyfinConfig{}, &models.EmbyLibrary{}, &models.EmbyLibrarySyncPath{}, &models.EmbyLibraryRefreshTask{}, &models.EmbyIndexState{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	var offline atomic.Bool
	offline.Store(true)
	var refreshes atomic.Int32
	var targets []models.EmbyRefreshTarget
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if kind != healthy && offline.Load() {
				http.Error(w, "offline", http.StatusServiceUnavailable)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
				fmt.Fprintf(w, `{"Id":%q,"ProductName":%q}`, "server-"+string(kind), kind.DisplayName()+" Server")
				return
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Refresh") {
				if kind == healthy {
					refreshes.Add(1)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(server.Close)
		var config any = &models.EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "fixture", EnableRefreshLibrary: 1}
		if kind == models.MediaServerJellyfin {
			config = &models.JellyfinConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "fixture", EnableRefreshLibrary: 1}
		}
		if err := db.Db.Create(config).Error; err != nil {
			t.Fatal(err)
		}
		const libraryID = "11111111111111111111111111111111"
		if err := db.Db.Create(&models.EmbyLibrarySyncPath{MediaServerType: kind, SyncPathId: sp.ID, LibraryId: libraryID}).Error; err != nil {
			t.Fatal(err)
		}
		target := models.EmbyRefreshTarget{MediaServerType: kind, ServerID: "server-" + string(kind), TargetType: targetType, FallbackLibraryId: libraryID}
		if targetType == models.EmbyRefreshTargetTypeItem {
			target.ItemID = "101"
			if kind == models.MediaServerJellyfin {
				target.ItemID = "22222222222222222222222222222222"
			}
			if err := db.Db.Create(&models.EmbyMediaItem{MediaServerType: kind, ServerId: target.ServerID, ItemId: target.ItemID, LibraryId: libraryID, Type: "Movie"}).Error; err != nil {
				t.Fatal(err)
			}
		}
		targets = append(targets, target)
	}
	task, service, _ := standaloneFinalizationFixture(t, account, sp)
	service.resolveRefreshTargets = func(*models.SyncFile) ([]models.EmbyRefreshTarget, error) { return targets, nil }
	service.requestEmbyRefreshTargets = func(id uint, pending []models.EmbyRefreshTarget) error {
		if len(pending) > 0 && pending[0].MediaServerType != healthy && offline.Load() {
			return errors.New("refresh queue temporarily unavailable")
		}
		return models.RequestMediaLibraryRefreshTargets(id, pending)
	}
	if stage != "generate" {
		task.Status = models.StrmGenerationStatusFinalizing
		task.SetRefreshTargets(targets)
		if stage == "parent" {
			parent := &models.StrmGenerationTask{SyncPathId: sp.ID, AccountId: account.ID, Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeBatchFiles, RefreshEmby: true, Status: models.StrmGenerationStatusCompleted, TotalItems: 1, AcceptedItems: 1, ChangedItems: 1}
			parent.SetRefreshTargets(targets)
			if err := db.Db.Create(parent).Error; err != nil {
				t.Fatal(err)
			}
			task.ParentTaskId = parent.ID
		}
		if err := db.Db.Save(task).Error; err != nil {
			t.Fatal(err)
		}
		if err := completeStrmGenerationTaskAfterSideEffects(t.Context(), service, task); err == nil {
			t.Fatal("offline endpoint unexpectedly submitted")
		}
	} else if _, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); err == nil {
		t.Fatal("offline endpoint unexpectedly generated without refresh failure")
	}
	var queued models.EmbyLibraryRefreshTask
	if err := db.Db.Where("media_server_type = ?", healthy).First(&queued).Error; err != nil {
		t.Fatal(err)
	}
	// 把首次防抖时间移到已到期，避免靠真实等待验证重试是否推迟执行。
	due := time.Now().Unix() - 1
	if err := db.Db.Model(&queued).Update("refresh_after_at", due).Error; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := db.Db.First(task, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := completeStrmGenerationTaskAfterSideEffects(t.Context(), service, task); err == nil {
			t.Fatal("offline endpoint unexpectedly recovered")
		}
		if err := db.Db.First(&queued, queued.ID).Error; err != nil {
			t.Fatal(err)
		}
		if queued.RefreshAfterAt != due {
			t.Fatalf("healthy endpoint postponed: got=%d want=%d", queued.RefreshAfterAt, due)
		}
	}
	if stage == "parent" {
		sibling := *task
		sibling.BaseModel = models.BaseModel{}
		sibling.UploadTaskId, sibling.RequestHash = 0, ""
		if err := db.Db.Create(&sibling).Error; err != nil {
			t.Fatal(err)
		}
		if err := completeStrmGenerationTaskAfterSideEffects(t.Context(), service, &sibling); err == nil {
			t.Fatal("sibling unexpectedly completed while peer offline")
		}
		if err := db.Db.First(&queued, queued.ID).Error; err != nil || queued.RefreshAfterAt != due {
			t.Fatalf("sibling resubmitted healthy target: %+v err=%v", queued, err)
		}
	}
	models.CheckPendingEmbyLibraryRefreshTasks()
	if refreshes.Load() != 1 {
		db.Db.First(&queued, queued.ID)
		t.Fatalf("healthy endpoint did not refresh while peer offline: calls=%d task=%+v", refreshes.Load(), queued)
	}
	offline.Store(false)
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := completeStrmGenerationTaskAfterSideEffects(t.Context(), service, task); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&queued, queued.ID).Error; err != nil || queued.Status != models.EmbyLibraryRefreshStatusCompleted {
		t.Fatalf("peer recovery resubmitted healthy endpoint: %+v err=%v", queued, err)
	}
	var recovered []models.EmbyLibraryRefreshTask
	if err := db.Db.Where("media_server_type <> ?", healthy).Find(&recovered).Error; err != nil || len(recovered) != 1 || recovered[0].Status != models.EmbyLibraryRefreshStatusPending {
		t.Fatalf("recovered peer did not receive its pending refresh: %+v err=%v", recovered, err)
	}
	for _, target := range targets {
		if target.MediaServerType == healthy {
			if err := models.RequestMediaLibraryRefreshTargets(sp.ID, []models.EmbyRefreshTarget{target}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Db.First(&queued, queued.ID).Error; err != nil || queued.RefreshAfterAt <= due || queued.Status != models.EmbyLibraryRefreshStatusPending {
		t.Fatalf("new change did not restart debounce: %+v err=%v", queued, err)
	}
}

func TestStrmRefreshSubmissionProgressCancellationAndSaveFailure(t *testing.T) {
	for _, fault := range []string{"cancel", "database", "stale", "cancelled task"} {
		t.Run(fault, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			task, service, _ := standaloneFinalizationFixture(t, account, sp)
			task.Status = models.StrmGenerationStatusFinalizing
			targets := []models.EmbyRefreshTarget{
				{MediaServerType: models.MediaServerEmby, ServerID: "original-emby", TargetType: models.EmbyRefreshTargetTypeLibrary},
				{MediaServerType: models.MediaServerJellyfin, ServerID: "original-jellyfin", TargetType: models.EmbyRefreshTargetTypeLibrary},
			}
			task.SetRefreshTargets(targets)
			if err := db.Db.Save(task).Error; err != nil {
				t.Fatal(err)
			}
			if fault == "stale" {
				newer := *task
				newer.SetRefreshTargets(targets[1:])
				if err := db.Db.Model(task).Update("refresh_targets_str", newer.RefreshTargetsStr).Error; err != nil {
					t.Fatal(err)
				}
				// 模拟仍持有旧快照的收尾请求，不能覆盖更新后的进度。
				task.SetRefreshTargets(targets)
			}
			if fault == "cancelled task" {
				if err := db.Db.Model(&models.StrmGenerationTask{}).Where("id = ?", task.ID).Update("status", models.StrmGenerationStatusCancelled).Error; err != nil {
					t.Fatal(err)
				}
			}
			saveErr, peerErr := errors.New("cannot save progress"), errors.New("peer offline")
			if fault == "database" {
				const callback = "test:refresh_progress_failure"
				if err := db.Db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					if values, ok := tx.Statement.Dest.(map[string]any); ok && tx.Statement.Table == "strm_generation_tasks" && values["refresh_targets_str"] != nil {
						tx.AddError(saveErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove(callback) })
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := map[models.MediaServerType]int{}
			service.requestEmbyRefreshTargets = func(_ uint, selected []models.EmbyRefreshTarget) error {
				kind := selected[0].MediaServerType
				calls[kind]++
				if kind == models.MediaServerEmby {
					if fault == "cancel" {
						cancel()
					}
					return nil
				}
				return peerErr
			}
			err := service.submitPendingRefresh(ctx, task)
			if err == nil || (fault == "cancel" && !errors.Is(err, context.Canceled)) || (fault == "database" && (!errors.Is(err, saveErr) || !errors.Is(err, peerErr))) {
				t.Fatalf("lost submission/persistence error: %v", err)
			}
			var saved models.StrmGenerationTask
			if err := db.Db.First(&saved, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := 1
			if fault == "database" || fault == "cancelled task" {
				want = 2
			}
			if saved.RefreshSubmitted || len(saved.GetRefreshTargets()) != want {
				t.Fatalf("failed save/cancellation lost pending work: %+v", saved)
			}
			if fault == "cancelled task" && saved.Status != models.StrmGenerationStatusCancelled {
				t.Fatal("progress update revived cancelled task")
			}
			if fault == "cancel" {
				if calls[models.MediaServerJellyfin] != 0 {
					t.Fatal("submitted another endpoint after cancellation")
				}
				service.requestEmbyRefreshTargets = func(_ uint, selected []models.EmbyRefreshTarget) error {
					if selected[0].MediaServerType != models.MediaServerJellyfin {
						t.Fatal("resubmitted successful endpoint after reload")
					}
					return nil
				}
				if err := service.submitPendingRefresh(t.Context(), &saved); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStrmRefreshSubmissionProgressWithLegacyNullTargets(t *testing.T) {
	_, sp := setupStrmGenerationServiceTestDB(t)
	task := &models.StrmGenerationTask{SyncPathId: sp.ID, Status: models.StrmGenerationStatusCompleted}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(task).Update("refresh_targets_str", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := task.SaveRefreshSubmissionProgress(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil || !task.RefreshSubmitted {
		t.Fatalf("legacy null targets did not finalize: %+v err=%v", task, err)
	}
}
