package syncstrm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	embyclientrestgo "qmediasync/internal/emby/client"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	v115open "qmediasync/internal/v115/openapi"
)

// 使用真正的 HTTP server；客户端请求只改目的地址，路径、方法和编码仍走生产实现。
type localEvidenceHTTPTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport localEvidenceHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme, cloned.URL.Host = transport.target.Scheme, transport.target.Host
	cloned.Host = transport.target.Host
	return transport.base.RoundTrip(cloned)
}

func setupLocalEvidenceDatabase(t *testing.T, dialector gorm.Dialector) func() {
	t.Helper()
	oldDB, oldSettings, oldConfig := db.Db, models.SettingsGlobal, models.GlobalEmbyConfig
	oldLog, old115Log, oldConfigDir := helpers.AppLogger, helpers.V115Log, helpers.ConfigDir
	t.Cleanup(func() {
		db.Db, models.SettingsGlobal, models.GlobalEmbyConfig = oldDB, oldSettings, oldConfig
		helpers.AppLogger, helpers.V115Log, helpers.ConfigDir = oldLog, old115Log, oldConfigDir
		emby.SetEmbySyncRunning(false)
	})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log, helpers.ConfigDir = helpers.AppLogger, t.TempDir()
	models.GlobalEmbyConfig = nil
	models.SettingsGlobal = &models.Settings{
		SettingThreads: models.SettingThreads{FileDetailThreads: 2, FileListPageSize: 100},
		SettingStrm: models.SettingStrm{StrmBaseUrl: "http://qms.fixture", VideoExt: `[".mkv"]`,
			MetaExt: `[".jpg",".nfo"]`, DownloadMeta: 1, Cron: "0 0 * * *"},
	}
	open := func() {
		conn, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		if dialector.Name() == "sqlite" {
			sqlDB.SetMaxOpenConns(1)
		} else {
			sqlDB.SetMaxOpenConns(4)
			sqlDB.SetMaxIdleConns(4)
		}
		db.Db = conn
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	open()
	if err := db.Db.AutoMigrate(&models.Account{}, &models.SyncPath{}, &models.Sync{}, &models.SyncFile{},
		&models.DbDownloadTask{}, &models.DbUploadTask{}, &models.Settings{}, &models.EmbyConfig{},
		&models.EmbyLibrary{}, &models.EmbyLibrarySyncPath{}, &models.EmbyLibraryRefreshTask{},
		&models.Media{}, &models.ScrapePath{}, &models.SyncPathScrapePath{}); err != nil {
		t.Fatal(err)
	}
	if err := models.MigrateEmbyDeletionSchema(db.Db); err != nil {
		t.Fatal(err)
	}
	if err := models.MigrateEmbySyncEvidenceSchema(db.Db); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(models.SettingsGlobal).Error; err != nil {
		t.Fatal(err)
	}
	return func() {
		sqlDB, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		models.GlobalEmbyConfig = nil
		open()
	}
}

func runLocalEvidenceScan(t *testing.T, root *models.SyncPath, full bool) models.Sync {
	t.Helper()
	if err := db.Db.Model(root).Update("is_full_sync", full).Error; err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncStrmFromSyncPath(root)
	if syncer == nil {
		t.Fatal("failed to create real STRM scanner")
	}
	syncer.Sync.Logger.Close()
	syncer.Sync.Logger, syncer.ownsLogger = helpers.AppLogger, false
	t.Cleanup(syncer.Cancel)
	syncer.backgroundService = newSyncBackgroundService()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := syncer.backgroundService.stop(ctx); err != nil {
			t.Error(err)
		}
	})
	// 走完整 Start；下载队列不派发任务，避免元数据传输混入扫描请求计数。
	oldDownload, oldUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
	download, upload := models.NewDq(0), models.NewUq(1)
	models.GlobalDownloadQueue, models.GlobalUploadQueue = download, upload
	defer func() {
		download.Stop()
		upload.Stop()
		models.GlobalDownloadQueue, models.GlobalUploadQueue = oldDownload, oldUpload
	}()
	if err := syncer.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, syncer.backgroundService)
	record := readBackgroundTestSync(t, syncer.Sync.ID)
	if record.LedgerStatus == nil || *record.LedgerStatus != realtime.SyncLedgerCompleted {
		t.Fatalf("background ledger did not complete: %+v", record)
	}
	return record
}

func TestSTRMLocalEvidenceSurvivesReopenAndEmbySync(t *testing.T) {
	testSTRMLocalEvidenceSurvivesReopenAndEmbySync(t, sqlite.Open(filepath.Join(t.TempDir(), "strm-emby.db")))
}

func testSTRMLocalEvidenceSurvivesReopenAndEmbySync(t *testing.T, dialector gorm.Dialector) {
	t.Helper()
	reopen := setupLocalEvidenceDatabase(t, dialector)
	account := models.Account{BaseModel: models.BaseModel{ID: uint(time.Now().UnixNano())}, SourceType: models.SourceType115, UserId: "local-evidence-user", Token: "fixture"}
	root := models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "10", RemotePath: "Media", LocalPath: t.TempDir(),
		SettingStrm: models.SettingStrm{DownloadMeta: 1}}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	paths := map[string][]map[string]string{
		"10": {{"file_id": "0", "file_name": ""}, {"file_id": "10", "file_name": "Media"}},
		"20": {{"file_id": "0", "file_name": ""}, {"file_id": "10", "file_name": "Media"}, {"file_id": "20", "file_name": "Show"}},
		"30": {{"file_id": "0", "file_name": ""}, {"file_id": "10", "file_name": "Media"}, {"file_id": "20", "file_name": "Show"}, {"file_id": "30", "file_name": "Season 1"}},
		"40": {{"file_id": "0", "file_name": ""}, {"file_id": "40", "file_name": "Empty"}},
	}
	files := []v115open.File{
		{FileId: "301", Pid: "30", Aid: "1", FileCategory: v115open.TypeFile, FileName: "episode.mkv", PickCode: "episode-pick", Sha1: "episode-hash", FileSize: 100, Utime: 123},
		{FileId: "302", Pid: "30", Aid: "1", FileCategory: v115open.TypeFile, FileName: "episode.nfo", PickCode: "nfo-pick", Sha1: "nfo-hash", FileSize: 30, Utime: 123},
		{FileId: "201", Pid: "20", Aid: "1", FileCategory: v115open.TypeFile, FileName: "season01-poster.jpg", PickCode: "poster-pick", Sha1: "poster-hash", FileSize: 50, Utime: 123},
	}
	var cloudHTTP, directoryWrites, posterWrites atomic.Int64
	var seasonDeleted, posterDeleted atomic.Bool
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHTTP.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/folder/get_info":
			id := r.URL.Query().Get("file_id")
			if r.Method == http.MethodPost {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				switch r.Form.Get("path") {
				case "/Media":
					id = "10"
				case "/Empty":
					id = "40"
				default:
					t.Errorf("unexpected root lookup: %s", r.Form.Get("path"))
					return
				}
			}
			if seasonDeleted.Load() && (id == "30" || id == "301" || id == "302") || posterDeleted.Load() && id == "201" {
				fmt.Fprint(w, `{"state":false,"code":430004}`)
				return
			}
			detail := v115open.File{FileId: id, FileCategory: v115open.TypeDir}
			switch id {
			case "10":
				detail.FileName, detail.Pid = "Media", "0"
			case "20":
				detail.FileName, detail.Pid = "Show", "10"
			case "30":
				detail.FileName, detail.Pid = "Season 1", "20"
			case "40":
				detail.FileName, detail.Pid = "Empty", "0"
			default:
				index := slices.IndexFunc(files, func(file v115open.File) bool { return file.FileId == id })
				if index < 0 {
					t.Errorf("unexpected detail read: %s", r.URL)
					return
				}
				detail = files[index]
			}
			ancestors := paths[detail.Pid]
			if detail.Pid == "0" {
				ancestors = []map[string]string{{"file_id": "0", "file_name": ""}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "code": 0, "data": map[string]any{
				"file_id": id, "file_name": detail.FileName, "file_category": detail.FileCategory, "paths": ancestors,
				"size_byte": detail.FileSize, "pick_code": detail.PickCode, "sha1": detail.Sha1, "utime": strconv.FormatInt(detail.Utime, 10),
			}})
		case "/open/ufile/files":
			id := r.URL.Query().Get("cid")
			entries := []v115open.File{}
			for _, file := range files {
				if id == "40" || r.URL.Query().Get("cur") == "1" && file.Pid != id ||
					seasonDeleted.Load() && file.Pid == "30" || posterDeleted.Load() && file.FileId == "201" {
					continue
				}
				entries = append(entries, file)
			}
			if r.URL.Query().Get("cur") == "1" {
				switch id {
				case "10":
					entries = append(entries, v115open.File{FileId: "20", Pid: "10", Aid: "1", FileCategory: v115open.TypeDir, FileName: "Show"})
				case "20":
					if !seasonDeleted.Load() {
						entries = append(entries, v115open.File{FileId: "30", Pid: "20", Aid: "1", FileCategory: v115open.TypeDir, FileName: "Season 1"})
					}
				case "30", "40":
				default:
					t.Errorf("unexpected directory listing: %s", r.URL)
				}
			}
			count := len(entries)
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit > 0 && len(entries) > limit {
				entries = entries[:limit]
			}
			parentPath := make([]map[string]string, 0, len(paths[id]))
			for _, entry := range paths[id] {
				parentPath = append(parentPath, map[string]string{"cid": entry["file_id"], "name": entry["file_name"]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "code": 0, "count": count, "data": entries, "path": parentPath})
		case "/open/ufile/delete":
			if r.Method != http.MethodPost {
				t.Errorf("unexpected delete method: %s", r.Method)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			switch ids := r.Form.Get("file_ids"); {
			case ids == "30" && r.Form.Get("parent_id") == "20":
				directoryWrites.Add(1)
				seasonDeleted.Store(true)
			case ids == "201" && r.Form.Get("parent_id") == "20":
				posterWrites.Add(1)
				posterDeleted.Store(true)
			default:
				t.Errorf("unexpected deletion scope: %v", r.Form)
				http.Error(w, "unexpected deletion scope", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"state":true,"code":0}`)
		default:
			t.Errorf("unexpected cloud HTTP: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(cloud.Close)
	cloudURL, err := url.Parse(cloud.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	account.Get115Client().SetTransport(localEvidenceHTTPTransport{target: cloudURL, base: transport})

	for round := range 2 {
		before := cloudHTTP.Load()
		record := runLocalEvidenceScan(t, &root, round == 0)
		wantHTTP, wantSTRM := int64(6), 1
		if round == 1 {
			wantHTTP, wantSTRM = 3, 0
		}
		if cloudHTTP.Load()-before != wantHTTP || record.NewStrm != wantSTRM {
			t.Fatalf("STRM round=%d HTTP=%d new_strm=%d, want %d/%d", round+1, cloudHTTP.Load()-before, record.NewStrm, wantHTTP, wantSTRM)
		}
		t.Logf("STRM round=%d cloud_http=%d new_strm=%d", round+1, cloudHTTP.Load()-before, record.NewStrm)
	}
	empty := root
	empty.BaseModel, empty.BaseCid, empty.RemotePath = models.BaseModel{}, "40", "Empty"
	if err := db.Db.Create(&empty).Error; err != nil {
		t.Fatal(err)
	}
	beforeEmpty := cloudHTTP.Load()
	runLocalEvidenceScan(t, &empty, true)
	if got := cloudHTTP.Load() - beforeEmpty; got != 2 {
		t.Fatalf("empty STRM root added cloud requests: got %d, want 2", got)
	}
	reopen()
	roots, err := models.LoadSyncRootEvidenceBatch(t.Context(), []uint{root.ID, empty.ID})
	if err != nil || len(roots) != 2 || roots[root.ID].InvalidatedAt != 0 || roots[empty.ID].InvalidatedAt != 0 {
		t.Fatalf("root evidence lost after reopen: %#v %v", roots, err)
	}
	var ledger []models.SyncFile
	if err := db.Db.Where("sync_path_id = ?", root.ID).Find(&ledger).Error; err != nil || len(ledger) != 5 {
		t.Fatalf("expected two directories, video, NFO and poster from actual STRM responses: %#v %v", ledger, err)
	}
	localEpisode := filepath.Join(root.GetFullLocalPath(), "Show", "Season 1", "episode.strm")
	playback, err := os.ReadFile(localEpisode)
	if err != nil || !strings.Contains(string(playback), "pickcode=episode-pick") {
		t.Fatalf("STRM not generated: %q %v", playback, err)
	}
	item := embyclientrestgo.BaseItemDtoV2{Id: "101", Type: "Episode", Name: "episode", ParentId: "202", SeriesId: "201", SeasonId: "202", ParentIndexNumber: 1, Path: localEpisode, PartCount: 1,
		MediaSources: []embyclientrestgo.MediaSource{{ID: "source-101", ItemID: "101", Path: string(playback)}}}
	var embyHTTP atomic.Int64
	var itemDeleted atomic.Bool
	embyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embyHTTP.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"strm-local-server"}`)
		case r.URL.Path == "/emby/Users":
			fmt.Fprint(w, `[{"Id":"user","Policy":{"EnableAllFolders":true}}]`)
		case r.URL.Path == "/emby/Library/MediaFolders":
			fmt.Fprint(w, `{"Items":[{"Id":"lib","Name":"Series"}]}`)
		case r.URL.Path == "/emby/Library/VirtualFolders":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"ItemId": "lib", "Name": "Series", "Locations": []string{root.GetFullLocalPath()}},
			})
		case r.URL.Path == "/emby/Items":
			items := []embyclientrestgo.BaseItemDtoV2{}
			if !itemDeleted.Load() {
				items = append(items, item)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
		case r.URL.Path == "/emby/Items/101/Ancestors":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "201", "Type": "Series", "Path": filepath.Dir(filepath.Dir(localEpisode)), "IsFolder": true},
				{"Id": "202", "Type": "Season", "Path": filepath.Dir(localEpisode), "IsFolder": true, "IndexNumber": 1},
				{"Id": "lib", "Type": "CollectionFolder", "Name": "Series", "Path": root.GetFullLocalPath(), "IsFolder": true},
			})
		default:
			t.Errorf("unexpected Emby HTTP: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(embyServer.Close)
	config := models.EmbyConfig{EmbyUrl: embyServer.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"full", "incremental", "single"} {
		for round := range 2 {
			beforeCloud, beforeEmby := cloudHTTP.Load(), embyHTTP.Load()
			if mode == "single" {
				changed, err := emby.SyncEmbyItemByID("101")
				if err != nil || !changed {
					t.Fatalf("single sync changed=%t error=%v", changed, err)
				}
			} else {
				run := emby.PerformEmbySync
				if mode == "incremental" {
					run = emby.PerformEmbyIncrementalSync
				}
				if count, err := run(); err != nil || count != 1 {
					t.Fatalf("%s round=%d count=%d error=%v", mode, round+1, count, err)
				}
			}
			if cloudHTTP.Load() != beforeCloud {
				t.Fatalf("%s round=%d queried cloud %d times", mode, round+1, cloudHTTP.Load()-beforeCloud)
			}
			assertLocalEvidenceSnapshot(t)
			t.Logf("Emby mode=%s round=%d cloud_http=0 emby_http=%d", mode, round+1, embyHTTP.Load()-beforeEmby)
		}
	}
	after, err := models.LoadSyncRootEvidenceBatch(t.Context(), []uint{root.ID})
	if err != nil || after[root.ID].ObservedAt != roots[root.ID].ObservedAt {
		t.Fatalf("Emby sync renewed the remote observation time: %#v %v", after, err)
	}
	if err := emby.StartWebhookWorker(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := emby.StopWebhookWorker(ctx); err != nil {
			t.Error(err)
		}
	})
	beforeWebhook := cloudHTTP.Load()
	modified, err := emby.ReceiveWebhook(t.Context(), models.EmbyWebhookEnvelope{
		Event: "library.modified", ServerID: "strm-local-server", ItemID: "101", ItemType: "Episode", ItemPath: localEpisode, Source: "official",
	})
	if err != nil {
		t.Fatal(err)
	}
	modified = waitLocalEvidenceWebhook(t, modified.ID)
	if modified.ObservationJSON == "" || cloudHTTP.Load() != beforeWebhook {
		t.Fatalf("webhook observation missing or queried cloud: observation=%q cloud_http=%d", modified.ObservationJSON, cloudHTTP.Load()-beforeWebhook)
	}
	frozen := assertLocalEvidenceSnapshot(t)
	if err := os.Remove(localEpisode); err != nil {
		t.Fatal(err)
	}
	itemDeleted.Store(true)
	deletion, err := emby.ReceiveWebhook(t.Context(), models.EmbyWebhookEnvelope{
		Event: "library.deleted", ServerID: "strm-local-server", ItemID: "202", ItemType: "Season",
		ItemPath: filepath.Dir(localEpisode), SeriesID: "201", SeasonID: "202", Source: "official",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitLocalEvidenceWebhook(t, deletion.ID)
	var plan models.EmbyDeletionPlan
	if err := json.Unmarshal([]byte(completed.PlanJSON), &plan); err != nil || len(plan.Supplements) != 0 || completed.InputJSON != deletion.InputJSON {
		t.Fatalf("complete STRM evidence required supplementation or changed frozen input: plan=%+v error=%v", plan, err)
	}
	if directoryWrites.Load() != 1 || posterWrites.Load() != 1 || !seasonDeleted.Load() || !posterDeleted.Load() {
		t.Fatalf("season directory and independent poster were not deleted once: directory=%d poster=%d", directoryWrites.Load(), posterWrites.Load())
	}
	targets, err := models.LoadEmbyWebhookTargets(t.Context(), deletion.ID)
	if err != nil {
		t.Fatal(err)
	}
	var deletedIDs []string
	for _, row := range targets {
		var target models.EmbyDeletionTarget
		if err := json.Unmarshal([]byte(row.TargetJSON), &target); err != nil {
			t.Fatal(err)
		}
		if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
			t.Fatalf("known deletion target did not complete: %+v", row)
		}
		deletedIDs = append(deletedIDs, target.File.FileID)
	}
	slices.Sort(deletedIDs)
	if !slices.Equal(deletedIDs, []string{"201", "30", "301", "302"}) {
		t.Fatalf("directory, video, NFO or season poster deletion result missing: %v", deletedIDs)
	}
	var historical models.EmbyItemEvidence
	if err := db.Db.First(&historical, frozen.ID).Error; err != nil || historical.FilesJSON != frozen.FilesJSON || historical.SidecarsJSON != frozen.SidecarsJSON || historical.SourcesJSON != frozen.SourcesJSON {
		t.Fatalf("deletion changed immutable sync evidence: %+v error=%v", historical, err)
	}
	t.Logf("STRM → reopen → Emby → webhook deletion: cloud_http=%d directory_writes=1 poster_writes=1 supplements=0", cloudHTTP.Load()-beforeWebhook)
}

func waitLocalEvidenceWebhook(t *testing.T, id uint) models.EmbyWebhookRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var record models.EmbyWebhookRecord
		if err := db.Db.WithContext(ctx).First(&record, id).Error; err != nil {
			t.Fatal(err)
		}
		switch record.Status {
		case models.EmbyWebhookDone:
			return record
		case models.EmbyWebhookRetry, models.EmbyWebhookUnresolved:
			t.Fatalf("webhook did not complete: status=%s reason=%s", record.Status, record.Reason)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("webhook did not finish: %+v", record)
		case <-ticker.C:
		}
	}
}

func assertLocalEvidenceSnapshot(t *testing.T) models.EmbyItemEvidence {
	t.Helper()
	var evidence models.EmbyItemEvidence
	if err := db.Db.Table("emby_item_evidences AS e").Select("e.*").Joins("JOIN emby_item_states AS s ON s.snapshot_id = e.id").Where("s.item_id = ?", "101").First(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	var files []models.EmbyFrozenFile
	if err := json.Unmarshal([]byte(evidence.FilesJSON), &files); err != nil || len(files) != 1 || files[0].Reason != "" || files[0].FileID != "301" || files[0].SHA1 != "episode-hash" {
		t.Fatalf("physical video binding lost: %#v %v", files, err)
	}
	metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil || len(metadata.ExclusiveFiles) != 1 || metadata.ExclusiveFiles[0].FileID != "302" || metadata.ExclusiveFiles[0].Reason != "" ||
		len(metadata.DirectoryScopes) != 2 || len(metadata.ScopedFiles) != 1 || metadata.ScopedFiles[0].File.FileID != "201" || metadata.ScopedFiles[0].ScopeType != "Season" || metadata.ScopedFiles[0].File.Reason != "" {
		t.Fatalf("local directory/season poster evidence lost: %#v %v", metadata, err)
	}
	for _, scope := range metadata.DirectoryScopes {
		if scope.Root.FileID != "20" && scope.Root.FileID != "30" || len(scope.Ancestors) < 2 || scope.Ancestors[0].FileID != "0" {
			t.Fatalf("physical directory identity or root ancestors missing: %#v", scope)
		}
	}
	return evidence
}
