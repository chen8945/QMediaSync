package syncstrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestLegacyStrmParentRefreshFinalizes(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	testLegacyStrmParentRefreshFinalizes(t, account, sp)
}

func TestLegacyStrmParentRefreshEnabledKeepsEmbyOwnership(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	if err := db.Db.AutoMigrate(&models.JellyfinConfig{}, &models.EmbyIndexState{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			t.Errorf("unexpected Emby request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"Id":"original-server","ProductName":"Emby Server"}`)
	}))
	defer server.Close()
	var jellyfinRequests atomic.Int32
	jellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jellyfinRequests.Add(1)
		http.Error(w, "legacy Emby target must not reach Jellyfin", http.StatusServiceUnavailable)
	}))
	defer jellyfin.Close()
	for _, row := range []any{
		&models.EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "test-key", EnableRefreshLibrary: 1},
		&models.JellyfinConfig{Enabled: true, EmbyUrl: jellyfin.URL, EmbyApiKey: "test-key", EnableRefreshLibrary: 1},
		&models.EmbyLibrarySyncPath{MediaServerType: models.MediaServerEmby, SyncPathId: sp.ID, LibraryId: "library-1"},
	} {
		if err := db.Db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	testLegacyStrmParentRefreshFinalizes(t, account, sp)
	var queued []models.EmbyLibraryRefreshTask
	if err := db.Db.Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].MediaServerType != models.MediaServerEmby || queued[0].ServerID != "original-server" || jellyfinRequests.Load() != 0 {
		t.Fatalf("legacy target lost ownership: queued=%+v Jellyfin requests=%d", queued, jellyfinRequests.Load())
	}
}

func TestStrmRefreshHistoricalTypeBoundary(t *testing.T) {
	for _, kind := range []models.MediaServerType{"", models.MediaServerJellyfin, "invalid"} {
		data, err := json.Marshal([]models.EmbyRefreshTarget{{MediaServerType: kind, TargetType: models.EmbyRefreshTargetTypeLibrary}})
		if err != nil {
			t.Fatal(err)
		}
		task := &models.StrmGenerationTask{RefreshTargetsStr: string(data)}
		got := task.GetRefreshTargets()
		want := kind
		if kind == "" {
			want = models.MediaServerEmby
		}
		if len(got) != 1 || got[0].MediaServerType != want || task.RefreshTargetsStr != string(data) {
			t.Fatalf("historical type rewritten: kind=%q got=%+v raw=%s", kind, got, task.RefreshTargetsStr)
		}
		if kind != models.MediaServerJellyfin {
			if err := models.RequestMediaLibraryRefreshTargets(1, []models.EmbyRefreshTarget{{MediaServerType: kind}}); err == nil {
				t.Fatalf("strict entry accepted type %q", kind)
			}
		}
	}
}

func TestStrmRefreshMetadataNoopDoesNotRequireOnlineServer(t *testing.T) {
	for _, state := range []string{"no match", "local exists", "active download", "new metadata"} {
		t.Run(state, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			service := newTestGenerationService(t, sp, account)
			build := service.buildSyncer
			service.buildSyncer = func(path *models.SyncPath, a *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
				syncer, err := build(path, a, config)
				if err != nil {
					return nil, err
				}
				if state != "no match" {
					syncer.SyncDriver = &fakeDirectoryScanDriver{filesByID: map[string][]*SyncFileCache{
						"root": {{FileId: "nfo-1", ParentId: "root", FileName: "movie.nfo", Path: "/remote", PickCode: "nfo-pick", SourceType: models.SourceType115}},
					}}
				}
				return syncer, nil
			}
			service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 1 }
			service.processStrmFile = func(*SyncStrm, *SyncFileCache) error { t.Fatal("unchanged STRM was rewritten"); return nil }
			unavailable := errors.New("media server offline")
			service.resolveRefreshTargets = func(*models.SyncFile) ([]models.EmbyRefreshTarget, error) { return nil, unavailable }
			service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error {
				t.Fatal("no new metadata should submit refresh")
				return nil
			}
			if state == "local exists" {
				p := filepath.Join(sp.LocalPath, "remote", "movie.nfo")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("existing metadata"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "active download" {
				if err := db.Db.Create(&models.DbDownloadTask{Source: models.DownloadSourceStrm, SourceType: models.SourceType115, AccountId: account.ID, SyncPathId: sp.ID, RemoteFileId: "nfo-1", Status: models.DownloadStatusPending}).Error; err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.Generate(t.Context(), StrmGenerationInput{Task: &models.StrmGenerationTask{
				Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID,
				FileId: "video-1", ParentId: "root", Path: "/remote", FileName: "movie.mkv", PickCode: "video-pick", FileSize: 1024, Sha1: "sha", Mtime: 1, RefreshEmby: true, DownloadMeta: true,
			}})
			if state == "new metadata" {
				if !errors.Is(err, unavailable) {
					t.Fatalf("new metadata must keep refresh recoverable: result=%+v err=%v", result, err)
				}
			} else if err != nil || result == nil || result.NewMeta != 0 || result.Changed {
				t.Fatalf("metadata noop blocked by offline server: result=%+v err=%v", result, err)
			}
			var downloads, metadata int64
			if err := db.Db.Model(&models.DbDownloadTask{}).Count(&downloads).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&models.SyncFile{}).Where("is_meta = ?", true).Count(&metadata).Error; err != nil {
				t.Fatal(err)
			}
			wantDownloads := int64(0)
			if state == "active download" {
				wantDownloads = 1
			}
			if downloads != wantDownloads || metadata != 0 {
				t.Fatalf("created metadata despite unresolved refresh: downloads=%d metadata=%d", downloads, metadata)
			}
		})
	}
}

func TestStrmRefreshFailurePreservesPersistenceErrorsAndNewerState(t *testing.T) {
	for _, fault := range []string{"save failure", "state changed", "cancelled submission"} {
		t.Run(fault, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			task, service, source := standaloneFinalizationFixture(t, account, sp)
			task.Source, task.RefreshEmby = models.StrmGenerationSourceWebhook, true
			if err := db.Db.Save(task).Error; err != nil {
				t.Fatal(err)
			}
			service.resolveRefreshTargets = func(*models.SyncFile) ([]models.EmbyRefreshTarget, error) {
				return []models.EmbyRefreshTarget{{MediaServerType: models.MediaServerEmby, ServerID: "original-server", TargetType: models.EmbyRefreshTargetTypeLibrary}}, nil
			}
			submitErr, saveErr := errors.New("refresh unavailable"), errors.New("cannot persist refresh")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error {
				if fault == "state changed" {
					if err := db.Db.Model(task).Update("status", models.StrmGenerationStatusCancelled).Error; err != nil {
						t.Fatal(err)
					}
				}
				if fault == "cancelled submission" {
					cancel()
					return context.Canceled
				}
				return submitErr
			}
			if fault == "save failure" {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("refresh:save-failure", func(tx *gorm.DB) {
					if updates, ok := tx.Statement.Dest.(map[string]any); ok && updates["refresh_targets_str"] != nil {
						tx.AddError(saveErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ProcessPendingStrmGenerationTasks(ctx, service, 1)
			if fault == "cancelled submission" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			} else if !errors.Is(err, submitErr) {
				t.Fatalf("submission failure lost: %v", err)
			}
			if fault == "save failure" && !errors.Is(err, saveErr) {
				t.Fatalf("save failure lost: %v", err)
			}
			if err := db.Db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := models.StrmGenerationStatusRunning
			if fault == "state changed" {
				want = models.StrmGenerationStatusCancelled
			}
			if fault == "cancelled submission" {
				want = models.StrmGenerationStatusFinalizing
			}
			if task.Status != want {
				t.Fatalf("status=%s want=%s", task.Status, want)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatalf("source cleaned after failure: %v", err)
			}
		})
	}
}

func testLegacyStrmParentRefreshFinalizes(t *testing.T, account *models.Account, sp *models.SyncPath) {
	t.Helper()
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeBatchFiles, SyncPathId: sp.ID, AccountId: account.ID, RefreshEmby: true, TotalItems: 1, AcceptedItems: 1, ChangedItems: 1, Status: models.StrmGenerationStatusCompleted, RefreshTargetsStr: `[{"TargetType":"library","SyncPathID":1}]`})
	if err != nil {
		t.Fatal(err)
	}
	child, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile, ParentTaskId: parent.ID, SyncPathId: sp.ID, AccountId: account.ID, Status: models.StrmGenerationStatusFinalizing, FileId: "legacy-file", FileName: "legacy.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestGenerationService(t, sp, account)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || err != nil {
		t.Fatalf("legacy finalization with refresh disabled: n=%d err=%v", n, err)
	}
	if err := db.Db.First(child, child.ID).Error; err != nil {
		t.Fatal(err)
	}
	if child.Status != models.StrmGenerationStatusCompleted {
		t.Fatalf("legacy child: %+v", child)
	}
}

func TestStrmRefreshIdentityFailureRemainsRecoverable(t *testing.T) {
	for _, stage := range []string{"resolve", "submit", "batch-submit"} {
		t.Run(stage, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testStrmRefreshIdentityFailureRemainsRecoverable(t, account, sp, stage)
		})
	}
}

func testStrmRefreshIdentityFailureRemainsRecoverable(t *testing.T, account *models.Account, sp *models.SyncPath, stage string) {
	testStrmRefreshFailureForSource(t, account, sp, stage, models.StrmGenerationSourceWebhook, models.MediaServerEmby)
}

func TestStrmRefreshConfigReadFailureRetainsTargets(t *testing.T) {
	for _, stage := range []string{"finalizing", "parent"} {
		for _, failed := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
			t.Run(stage+"/"+string(failed), func(t *testing.T) {
				account, sp := setupStrmGenerationServiceTestDB(t)
				testStrmRefreshConfigReadFailureRetainsTargets(t, account, sp, stage, failed)
			})
		}
	}
}

func testStrmRefreshConfigReadFailureRetainsTargets(t *testing.T, account *models.Account, sp *models.SyncPath, stage string, failed models.MediaServerType) {
	t.Helper()
	if db.Db.Dialector.Name() == "sqlite" {
		pool, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
	}
	if err := db.Db.AutoMigrate(&models.JellyfinConfig{}, &models.EmbyIndexState{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	var targets []models.EmbyRefreshTarget
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		var config any = &models.EmbyConfig{Enabled: true, EmbyUrl: "http://unused.invalid", EmbyApiKey: "fixture", EnableRefreshLibrary: 1}
		if kind == models.MediaServerJellyfin {
			config = &models.JellyfinConfig{Enabled: true, EmbyUrl: "http://unused.invalid", EmbyApiKey: "fixture", EnableRefreshLibrary: 1}
		}
		if err := db.Db.Create(config).Error; err != nil {
			t.Fatal(err)
		}
		targets = append(targets, models.EmbyRefreshTarget{MediaServerType: kind, ServerID: "original-" + string(kind), TargetType: models.EmbyRefreshTargetTypeLibrary, FallbackLibraryId: "11111111111111111111111111111111"})
	}
	task, service, source := standaloneFinalizationFixture(t, account, sp)
	task.Status = models.StrmGenerationStatusFinalizing
	pending := task
	if stage == "parent" {
		pending = &models.StrmGenerationTask{SyncPathId: sp.ID, AccountId: account.ID, Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeBatchFiles, RefreshEmby: true, Status: models.StrmGenerationStatusCompleted, TotalItems: 1, AcceptedItems: 1, ChangedItems: 1}
		if err := db.Db.Create(pending).Error; err != nil {
			t.Fatal(err)
		}
		task.ParentTaskId = pending.ID
	}
	pending.SetRefreshTargets(targets)
	if err := db.Db.Save(pending).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Save(task).Error; err != nil {
		t.Fatal(err)
	}
	service.buildSyncer = func(*models.SyncPath, *models.Account, *SyncStrmConfig) (*SyncStrm, error) {
		t.Fatal("refresh recovery regenerated the STRM")
		return nil, nil
	}
	queryErr := errors.New("temporary configuration read failure")
	var submittingFailed, injected atomic.Bool
	conn := db.Db
	const callback = "test:refresh_submission_config_read"
	if err := conn.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if submittingFailed.Load() && tx.Statement.Table == string(failed)+"_config" && injected.CompareAndSwap(false, true) {
			tx.AddError(queryErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Query().Remove(callback) })
	service.requestEmbyRefreshTargets = func(id uint, selected []models.EmbyRefreshTarget) error {
		submittingFailed.Store(selected[0].MediaServerType == failed)
		defer submittingFailed.Store(false)
		return models.RequestMediaLibraryRefreshTargets(id, selected)
	}
	n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1)
	if err := conn.First(pending, pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	remaining := pending.GetRefreshTargets()
	if n != 1 || !injected.Load() || !errors.Is(err, queryErr) {
		t.Fatalf("configuration failure was not propagated: n=%d injected=%v err=%v submitted=%v remaining=%+v", n, injected.Load(), err, pending.RefreshSubmitted, remaining)
	}
	if pending.RefreshSubmitted || len(remaining) != 1 || remaining[0].MediaServerType != failed || remaining[0].ServerID != "original-"+string(failed) {
		t.Fatalf("configuration failure lost the original target: %+v", pending)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source cleaned before refresh submission: %v", err)
	}
	var queued []models.EmbyLibraryRefreshTask
	if err := conn.Find(&queued).Error; err != nil || len(queued) != 1 || queued[0].MediaServerType == failed {
		t.Fatalf("failed endpoint blocked healthy submission: tasks=%+v err=%v", queued, err)
	}
	due := time.Now().Unix() - 1
	if err := conn.Model(&queued[0]).Update("refresh_after_at", due).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(task).Update("last_retry_time", time.Now().Unix()-5).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || err != nil {
		t.Fatalf("configuration recovery did not finish: n=%d err=%v", n, err)
	}
	if err := conn.First(pending, pending.ID).Error; err != nil || !pending.RefreshSubmitted || len(pending.GetRefreshTargets()) != 0 {
		t.Fatalf("recovered targets did not finalize: %+v err=%v", pending, err)
	}
	var recovered models.EmbyLibraryRefreshTask
	if err := conn.Where("media_server_type = ?", failed).First(&recovered).Error; err != nil || recovered.ServerID != "original-"+string(failed) || recovered.Status != models.EmbyLibraryRefreshStatusPending {
		t.Fatalf("recovery lost original instance: %+v err=%v", recovered, err)
	}
	if err := conn.First(&queued[0], queued[0].ID).Error; err != nil || queued[0].RefreshAfterAt != due {
		t.Fatalf("recovery postponed the healthy endpoint: %+v err=%v", queued[0], err)
	}
}

func TestOrdinaryStrmRefreshFailureRemainsRecoverable(t *testing.T) {
	for _, source := range []models.StrmGenerationSource{models.StrmGenerationSourceUploadCompleted, models.StrmGenerationSourceRemoteExists} {
		for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
			for _, stage := range []string{"resolve", "submit", "local-offline"} {
				t.Run(string(source)+"/"+string(kind)+"/"+stage, func(t *testing.T) {
					account, sp := setupStrmGenerationServiceTestDB(t)
					testStrmRefreshFailureForSource(t, account, sp, stage, source, kind)
				})
			}
		}
	}
}

func TestOrdinaryStrmRefreshRecoveryPreservesUploadSource(t *testing.T) {
	for _, sourceType := range []models.StrmGenerationSource{models.StrmGenerationSourceUploadCompleted, models.StrmGenerationSourceRemoteExists} {
		t.Run(string(sourceType), func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			task, service, source := standaloneFinalizationFixture(t, account, sp)
			if err := db.Db.Model(task).Update("source", sourceType).Error; err != nil {
				t.Fatal(err)
			}
			builds, writes, submissions := 0, 0, 0
			build, write := service.buildSyncer, service.processStrmFile
			service.buildSyncer = func(path *models.SyncPath, account *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
				builds++
				return build(path, account, config)
			}
			service.processStrmFile = func(syncer *SyncStrm, file *SyncFileCache) error {
				writes++
				return write(syncer, file)
			}
			service.resolveRefreshTargets = func(*models.SyncFile) ([]models.EmbyRefreshTarget, error) {
				return []models.EmbyRefreshTarget{{MediaServerType: models.MediaServerEmby, ServerID: "original-server", TargetType: models.EmbyRefreshTargetTypeLibrary}}, nil
			}
			submitErr := errors.New("refresh unavailable")
			service.requestEmbyRefreshTargets = func(_ uint, targets []models.EmbyRefreshTarget) error {
				submissions++
				if len(targets) != 1 || targets[0].ServerID != "original-server" {
					t.Fatalf("original refresh target lost: %+v", targets)
				}
				if _, err := os.Stat(source); err != nil {
					t.Fatalf("source cleaned before successful submission: %v", err)
				}
				if submissions == 1 {
					return submitErr
				}
				return nil
			}
			if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || !errors.Is(err, submitErr) {
				t.Fatalf("initial failure: n=%d err=%v", n, err)
			}
			if err := db.Db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Status != models.StrmGenerationStatusFinalizing || len(task.GetRefreshTargets()) != 1 {
				t.Fatalf("refresh retry not persisted: %+v", task)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatalf("source cleaned after failed submission: %v", err)
			}
			if err := db.Db.Model(task).Update("last_retry_time", time.Now().Unix()-5).Error; err != nil {
				t.Fatal(err)
			}
			if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || err != nil {
				t.Fatalf("refresh recovery: n=%d err=%v", n, err)
			}
			if err := db.Db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if builds != 1 || writes != 1 || submissions != 2 || task.Status != models.StrmGenerationStatusCompleted {
				t.Fatalf("recovery repeated generation or lost completion: builds=%d writes=%d submissions=%d task=%+v", builds, writes, submissions, task)
			}
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source cleanup after recovery missing: %v", err)
			}
		})
	}
}

func TestOrdinaryStrmNoopOrDisabledRefreshWorksOffline(t *testing.T) {
	for _, state := range []string{"unchanged", "non-owner", "refresh-disabled"} {
		t.Run(state, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			if err := db.Db.AutoMigrate(&models.JellyfinConfig{}, &models.EmbyIndexState{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "offline", http.StatusServiceUnavailable)
			}))
			t.Cleanup(server.Close)
			config := &models.EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key", EnableRefreshLibrary: 1}
			if state == "refresh-disabled" {
				config.EnableRefreshLibrary = 0
			}
			if err := db.Db.Create(config).Error; err != nil {
				t.Fatal(err)
			}
			service := newTestGenerationService(t, sp, account)
			writes := 0
			service.processStrmFile = func(*SyncStrm, *SyncFileCache) error { writes++; return nil }
			service.compareStrm = func(*SyncStrm, *SyncFileCache) int {
				if state == "unchanged" {
					return 1
				}
				return 0
			}
			if state == "non-owner" {
				service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) {
					return false, nil
				}
			}
			result, err := service.Generate(t.Context(), StrmGenerationInput{Task: &models.StrmGenerationTask{Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, FileId: "file", PickCode: "pick", ParentId: "root", Path: "/remote", FileName: "movie.mkv", FileSize: 1024, Sha1: "sha", Mtime: 1}})
			wantWrites := 0
			if state == "refresh-disabled" {
				wantWrites = 1
			}
			if err != nil || result == nil || writes != wantWrites {
				t.Fatalf("offline noop/disabled generation blocked: result=%+v err=%v writes=%d", result, err, writes)
			}
			if state == "refresh-disabled" && requests.Load() != 0 {
				t.Fatalf("disabled refresh queried server %d times", requests.Load())
			}
		})
	}
}

func testStrmRefreshFailureForSource(t *testing.T, account *models.Account, sp *models.SyncPath, stage string, source models.StrmGenerationSource, kind models.MediaServerType) {
	t.Helper()
	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyLibrary{}, &models.EmbyLibrarySyncPath{}, &models.EmbyLibraryRefreshTask{}, &models.JellyfinConfig{}, &models.EmbyIndexState{}, &models.EmbyItemEvidence{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	var offline atomic.Bool
	offline.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" && offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprintf(w, `{"Id":"original-server","ProductName":%q}`, kind.DisplayName()+" Server")
		case "/emby/Items":
			fmt.Fprint(w, `{"Items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var configRow any = &models.EmbyConfig{Enabled: true, SyncEnabled: 1, EmbyUrl: server.URL, EmbyApiKey: "test-key", EnableRefreshLibrary: 1}
	if kind == models.MediaServerJellyfin {
		configRow = &models.JellyfinConfig{Enabled: true, SyncEnabled: 1, EmbyUrl: server.URL, EmbyApiKey: "test-key", EnableRefreshLibrary: 1}
	}
	if err := db.Db.Create(configRow).Error; err != nil {
		t.Fatal(err)
	}
	if stage == "local-offline" {
		config, err := models.ReadMediaLibraryConfigSnapshot(kind)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := models.BeginEmbyIndexRead("original-server", config); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Db.Create(&models.EmbyLibrarySyncPath{MediaServerType: kind, SyncPathId: sp.ID, LibraryId: "library-1"}).Error; err != nil {
		t.Fatal(err)
	}
	task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{Source: source, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, RefreshEmby: true, FileId: "recover-file", PickCode: "recover-pick", ParentId: "root", Path: "/remote", FileName: "recover.mkv", Mtime: 1, FileSize: 1024, Sha1: "sha", RequestHash: "refresh-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if stage == "batch-submit" {
		parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeBatchFiles, SyncPathId: sp.ID, AccountId: account.ID, RefreshEmby: true, TotalItems: 1, Status: models.StrmGenerationStatusWaitingChildren})
		if err != nil {
			t.Fatal(err)
		}
		task.ParentTaskId = parent.ID
		if err := db.Db.Model(task).Update("parent_task_id", parent.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := newTestGenerationService(t, sp, account)
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.Sync = &models.Sync{Logger: helpers.AppLogger}
		s.SyncDriver = &fakeDirectoryScanDriver{strmContent: "http://qms.local/115/url/video.mkv?pickcode=recover-pick&userid=user-1"}
		return s, err
	}
	writes := 0
	originalWrite := service.processStrmFile
	service.processStrmFile = func(s *SyncStrm, f *SyncFileCache) error { writes++; return originalWrite(s, f) }
	if stage == "submit" || stage == "batch-submit" {
		submit := service.requestEmbyRefreshTargets
		service.requestEmbyRefreshTargets = func(id uint, targets []models.EmbyRefreshTarget) error {
			if offline.Load() {
				return errors.New("refresh queue temporarily unavailable")
			}
			return submit(id, targets)
		}
		offline.Store(false)
		resolve := service.resolveRefreshTargets
		service.resolveRefreshTargets = func(f *models.SyncFile) ([]models.EmbyRefreshTarget, error) {
			targets, err := resolve(f)
			offline.Store(true)
			return targets, err
		}
	}
	_, _ = ProcessPendingStrmGenerationTasks(t.Context(), service, 1)
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stage != "resolve" && stage != "local-offline" {
		if task.Status != models.StrmGenerationStatusFinalizing {
			t.Fatalf("submission failure lost retry: %+v", task)
		}
		if stage == "submit" && (len(task.GetRefreshTargets()) != 1 || task.GetRefreshTargets()[0].ServerID != "original-server") {
			t.Fatalf("identity lost: %+v", task)
		}
	}
	if stage == "local-offline" {
		if task.Status != models.StrmGenerationStatusCompleted {
			t.Fatalf("known offline identity did not persist refresh: %+v", task)
		}
	} else {
		offline.Store(false)
		if task.Status == models.StrmGenerationStatusFinalizing {
			if err := db.Db.Model(task).Update("last_retry_time", time.Now().Unix()-5).Error; err != nil {
				t.Fatal(err)
			}
		} else {
			duplicate := *task
			duplicate.BaseModel = models.BaseModel{}
			duplicate.Status = models.StrmGenerationStatusPending
			task, err = models.EnqueueStrmGenerationTask(&duplicate)
			if err != nil {
				t.Fatal(err)
			}
		}
		service.resolveRefreshTargets = models.ResolveMediaLibraryRefreshTargets
		if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || err != nil {
			t.Fatalf("recovery n=%d err=%v", n, err)
		}
	}
	var count int64
	if err := db.Db.Model(&models.EmbyLibraryRefreshTask{}).Where("media_server_type = ? AND server_id = ?", kind, "original-server").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || writes != 1 {
		t.Fatalf("lost refresh or regenerated file: refreshes=%d writes=%d", count, writes)
	}
	if task.ParentTaskId > 0 {
		parent, err := models.GetStrmGenerationTaskByID(task.ParentTaskId)
		if err != nil || parent.AcceptedItems != 1 || parent.ChangedItems != 1 || !parent.RefreshSubmitted {
			t.Fatalf("parent progress duplicated: %+v %v", parent, err)
		}
	}
	var ledger models.SyncFile
	if err := db.Db.Where("file_id = ?", task.FileId).First(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(ledger.LocalFilePath); err != nil || string(data) != "http://qms.local/115/url/video.mkv?pickcode=recover-pick&userid=user-1" {
		t.Fatalf("STRM=%q %v", data, err)
	}
}
