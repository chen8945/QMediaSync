package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type snapshotRecoveryFixture struct {
	config        models.EmbyConfig
	oldToken      models.EmbyIndexToken
	records       []models.EmbyWebhookRecord
	provider      *webhookTestProvider
	strictQueries atomic.Int32
	freshQueries  atomic.Int32
	partsAbsent   atomic.Bool
	hook          func(bool)
	requestHook   func(http.ResponseWriter, *http.Request) bool
	itemQueries   []string
}

func setupSnapshotRecoveryFixture(t *testing.T, absentBarriers int) *snapshotRecoveryFixture {
	t.Helper()
	previousDB, previousLogger := db.Db, helpers.AppLogger
	t.Cleanup(func() { db.Db = previousDB; helpers.AppLogger = previousLogger; SetEmbySyncRunning(false) })
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	setupSnapshotTestTables(t)
	f := &snapshotRecoveryFixture{provider: &webhookTestProvider{t: t, files: map[string]models.EmbyRemoteFile{}}}
	root := models.SyncPath{SourceType: models.SourceType115, AccountId: 1, BaseCid: "root", RemotePath: "/movies", LocalPath: t.TempDir()}
	parentItems := []embyclientrestgo.BaseItemDtoV2{}
	partItems := map[string]embyclientrestgo.BaseItemDtoV2{}
	snapshots := []models.EmbyItemSnapshot{}
	for _, id := range []string{"101", "102", "201", "202"} {
		file := models.SyncFile{SourceType: models.SourceType115, AccountId: 1, SyncPathId: 1, FileId: "file-" + id, ParentId: "root", FileName: id + ".mkv", Path: "/movies", LocalFilePath: filepath.Join(root.GetFullLocalPath(), id+".strm"), PickCode: "pick-" + id, Sha1: "hash-" + id, FileSize: 100, MTime: 123, IsVideo: true}
		if err := conn.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		f.provider.files[file.FileId] = models.EmbyRemoteFile{FileID: file.FileId, ParentID: file.ParentId, FileName: file.FileName, Path: file.Path, PickCode: file.PickCode, SHA1: file.Sha1, FileSize: file.FileSize, MTime: file.MTime}
		item := embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Path: file.LocalFilePath, Name: "initial", MediaSources: []embyclientrestgo.MediaSource{{ID: "source-" + id, ItemID: id, Path: "http://qms/stream?pickcode=" + file.PickCode}}}
		saved := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: id, Type: item.Type, Path: file.LocalFilePath, LibraryId: "99", Name: item.Name, PickCode: file.PickCode}, Sources: []models.EmbySnapshotSource{{ID: "source-" + id, ItemID: id, Path: item.MediaSources[0].Path, PickCode: file.PickCode}}, MembersComplete: true}
		if strings.HasSuffix(id, "01") {
			item.PartCount = 2
			saved.Item.PartCount = 2
			parentItems = append(parentItems, item)
		} else {
			item.Type = "Video"
			saved.Item.Type = "Video"
			saved.MembersComplete = false
			parent := strconv.Itoa(helpers.StringToInt(id) - 1)
			saved.Item.PartOfItemID = parent
			partItems[parent] = item
		}
		snapshots = append(snapshots, saved)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.requestHook != nil && f.requestHook(w, r) {
			return
		}
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"server-a"}`)
		case "/emby/Users":
			fmt.Fprint(w, `[{"Id":"u","Policy":{"EnableAllFolders":true}}]`)
		case "/emby/Library/MediaFolders":
			fmt.Fprint(w, `{"Items":[{"Id":"99","Name":"fixture"}]}`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprintf(w, `[{"ItemId":"99","Name":"fixture","Locations":[%q]}]`, root.GetFullLocalPath())
		case "/emby/Items/101/Ancestors":
			fmt.Fprintf(w, `[{"Id":"99","Type":"Folder","Path":%q}]`, root.GetFullLocalPath())
		case "/emby/Items":
			f.itemQueries = append(f.itemQueries, r.URL.Query().Get("Ids"))
			strict := r.URL.Query().Get("SortBy") == "Id"
			if strict {
				f.strictQueries.Add(1)
			} else {
				f.freshQueries.Add(1)
			}
			if f.hook != nil {
				f.hook(strict)
			}
			// 原始条目 DateLastSaved 没有因失败删除变化；恢复增量必须从零重新读取。
			if cursor := r.URL.Query().Get("MinDateLastSaved"); cursor != "" && cursor != "1970-01-01T00:00:00Z" && !f.partsAbsent.Load() {
				fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
				return
			}
			response := []embyclientrestgo.BaseItemDtoV2{}
			ids := r.URL.Query().Get("Ids")
			for _, item := range parentItems {
				if ids != "" && !slices.Contains(strings.Split(ids, ","), item.Id) {
					continue
				}
				if f.partsAbsent.Load() {
					item.PartCount = 1
				}
				if strict && f.strictQueries.Load() == 1 {
					item.Name = "must-discard-recovery-read"
				} else {
					item.Name = "fresh-after-admission"
				}
				response = append(response, item)
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"Items": response, "TotalRecordCount": len(response)}); err != nil {
				t.Error(err)
			}
		case "/emby/Videos/101/AdditionalParts", "/emby/Videos/201/AdditionalParts":
			id := strings.Split(r.URL.Path, "/")[3]
			if err := json.NewEncoder(w).Encode(map[string]any{"Items": []embyclientrestgo.BaseItemDtoV2{partItems[id]}, "TotalRecordCount": 1}); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.config = models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1, LastSavedCursorAt: 10000, LastFullSyncAt: 50}
	if err := conn.Create(&f.config).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.Account{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115, UserId: "fixture-owner"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	f.oldToken, err = models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	if absentBarriers > 0 {
		ids := make([]string, absentBarriers)
		for i := range ids {
			ids[i] = strconv.Itoa(5000 + i)
		}
		if err := conn.Transaction(func(tx *gorm.DB) error { _, err := models.RegisterEmbyDeletionTx(tx, "server-a", ids); return err }); err != nil {
			t.Fatal(err)
		}
		f.oldToken, err = models.BeginEmbyIndexRead("server-a", &f.config)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := models.ApplyEmbySnapshots(f.oldToken, snapshots); err != nil {
		t.Fatal(err)
	}
	for _, item := range parentItems {
		record, err := models.SaveEmbyWebhook(t.Context(), models.EmbyWebhookEnvelope{Event: "deep.delete", ServerID: "server-a", ItemID: item.Id, ItemType: item.Type, ItemPath: item.Path, Source: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		f.records = append(f.records, record)
	}
	worker := &webhookWorker{verify: func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		return errors.New("temporary Emby API outage")
	}, provider: func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return f.provider, nil }}
	for range webhookMaxAttempts * len(f.records) {
		worker.process(t.Context(), claimWebhookTest(t))
	}
	for _, record := range f.records {
		if current := readWebhookTest(t, record.ID); current.Status != models.EmbyWebhookUnresolved || current.Attempts != webhookMaxAttempts {
			t.Fatalf("delete not exhausted: %+v", current)
		}
	}
	return f
}

func TestSnapshotRecoveryOrdinarySyncRestoresExhaustedDeleteSurvivors(t *testing.T) {
	for _, mode := range []string{"full", "incremental", "single"} {
		t.Run(mode, func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			var err error
			switch mode {
			case "full":
				_, err = PerformEmbySync()
			case "incremental":
				_, err = PerformEmbyIncrementalSync()
			case "single":
				_, err = SyncEmbyItemByID("101")
			}
			if err != nil {
				t.Fatal(err)
			}
			var states []models.EmbyItemState
			if err := db.Db.Find(&states).Error; err != nil {
				t.Fatal(err)
			}
			if len(states) != 4 {
				t.Fatalf("states=%d", len(states))
			}
			for _, state := range states {
				wantBlocked := mode == "single" && (state.ItemID == "201" || state.ItemID == "202")
				if state.Deleted != wantBlocked {
					t.Fatalf("survivor scope changed: %s deleted=%t want=%t", state.ItemID, state.Deleted, wantBlocked)
				}
			}
			var item models.EmbyMediaItem
			if err := db.Db.Where("item_id = ?", "101").First(&item).Error; err != nil {
				t.Fatal(err)
			}
			wantStrict, wantFresh := int32(1), int32(1)
			if mode == "single" {
				wantStrict, wantFresh = 2, 0
				if !slices.Equal(f.itemQueries, []string{"101", "101"}) {
					t.Fatalf("single sync queried unrelated items: %v", f.itemQueries)
				}
			}
			if item.Name != "fresh-after-admission" || f.strictQueries.Load() != wantStrict || f.freshQueries.Load() != wantFresh {
				t.Fatalf("old response reused or unnecessary rescans: item=%s strict=%d fresh=%d", item.Name, f.strictQueries.Load(), f.freshQueries.Load())
			}
			if err := models.ApplyEmbySnapshots(f.oldToken, nil); !errors.Is(err, models.ErrEmbySnapshotStale) {
				t.Fatal("pre-delete query became current", err)
			}
			for _, record := range f.records {
				fresh := readWebhookTest(t, record.ID)
				if fresh.Status != models.EmbyWebhookUnresolved || fresh.Attempts != webhookMaxAttempts || fresh.Authorized != record.Authorized {
					t.Fatal("recovery reopened or authorized old delete")
				}
			}
			if f.provider.calls != 0 {
				t.Fatal("index recovery invoked cloud deletion")
			}
		})
	}
}

func TestSnapshotRecoveryPaginatesPastAbsentBarriers(t *testing.T) {
	f := setupSnapshotRecoveryFixture(t, 105)
	if _, err := PerformEmbySync(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Db.Model(&models.EmbyItemState{}).Where("deleted = ?", true).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 105 || f.strictQueries.Load() != 2 || f.freshQueries.Load() != 1 {
		t.Fatalf("tail survivors starved: blocked=%d strict=%d fresh=%d", count, f.strictQueries.Load(), f.freshQueries.Load())
	}
}

func TestSnapshotRecoveryConcurrentDeleteRejectsOldReads(t *testing.T) {
	for _, phase := range []string{"recovery", "normal"} {
		t.Run(phase, func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			var injected atomic.Bool
			f.hook = func(strict bool) {
				if strict != (phase == "recovery") || !injected.CompareAndSwap(false, true) {
					return
				}
				if err := db.Db.Transaction(func(tx *gorm.DB) error {
					_, err := models.RegisterEmbyDeletionTx(tx, "server-a", []string{"101", "102", "201", "202"})
					return err
				}); err != nil {
					t.Error(err)
				}
			}
			if _, err := PerformEmbySync(); !errors.Is(err, models.ErrEmbySnapshotStale) {
				t.Fatalf("competing delete did not reject old read: %v", err)
			}
			var config models.EmbyConfig
			if err := db.Db.First(&config).Error; err != nil {
				t.Fatal(err)
			}
			if config.LastFullSyncAt != 50 || config.LastSavedCursorAt != 10000 || config.LastError == "" {
				t.Fatal("stale recovery advanced successful watermark")
			}
			var states []models.EmbyItemState
			if err := db.Db.Find(&states).Error; err != nil {
				t.Fatal(err)
			}
			for _, state := range states {
				if !state.Deleted {
					t.Fatal("partial admission survived competing deletion")
				}
			}
			var items []models.EmbyMediaItem
			if err := db.Db.Find(&items).Error; err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Name == "fresh-after-admission" || item.Name == "must-discard-recovery-read" {
					t.Fatal("old response written under replacement token")
				}
			}
		})
	}
}

func TestSnapshotRecoveryHiddenPartsAfterRootSurvivorAdmission(t *testing.T) {
	for _, cleanIndex := range []bool{false, true} {
		t.Run(fmt.Sprintf("current-index-cleaned=%t", cleanIndex), func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			admitSnapshotRecoveryRoots(t)
			if cleanIndex {
				for _, id := range []string{"101", "102", "201", "202"} {
					if err := models.DeleteLocalEmbyItemByID(id); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := PerformEmbySync(); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Db.Model(&models.EmbyItemState{}).Where("deleted = ?", true).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 || f.strictQueries.Load() != 1 || f.freshQueries.Load() != 1 {
				t.Fatalf("hidden parts stayed blocked or were not reread: blocked=%d strict=%d fresh=%d", count, f.strictQueries.Load(), f.freshQueries.Load())
			}
			var items []models.EmbyMediaItem
			if err := db.Db.Find(&items).Error; err != nil {
				t.Fatal(err)
			}
			if len(items) != 4 {
				t.Fatalf("survivors not restored: %d", len(items))
			}
			for _, item := range items {
				if item.Type == "Movie" && item.Name != "fresh-after-admission" {
					t.Fatal("recovery response became current index")
				}
			}
			for _, record := range f.records {
				if current := readWebhookTest(t, record.ID); current.Status != models.EmbyWebhookUnresolved || current.Authorized != record.Authorized || current.InputJSON != record.InputJSON {
					t.Fatal("index recovery changed old deletion")
				}
			}
		})
	}
}

func admitSnapshotRecoveryRoots(t *testing.T) {
	t.Helper()
	// 模拟 worker 仅确认主项存活后的状态：主项已准入，隐藏分段仍在删除屏障下。
	for _, id := range []string{"101", "201"} {
		if err := db.Db.Transaction(func(tx *gorm.DB) error {
			var state models.EmbyItemState
			if err := tx.Where("server_id = ? AND item_id = ?", "server-a", id).First(&state).Error; err != nil {
				return err
			}
			return models.AdmitEmbyItemGenerationTx(tx, "server-a", id, state.Revision)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotRecoveryAbsentHiddenPartPreservesDeletionEvidence(t *testing.T) {
	for _, mode := range []string{"single", "incremental", "full"} {
		t.Run(mode, func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			admitSnapshotRecoveryRoots(t)
			f.partsAbsent.Store(true)
			var before models.EmbyItemState
			if err := db.Db.Where("item_id = ?", "102").First(&before).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "single":
				_, err = SyncEmbyItemByID("101")
			case "incremental":
				_, err = PerformEmbyIncrementalSync()
			case "full":
				_, err = PerformEmbySync()
			}
			if err != nil {
				t.Fatal(err)
			}
			var after models.EmbyItemState
			if err := db.Db.Where("item_id = ?", "102").First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if !after.Deleted || after.SnapshotID != before.SnapshotID || after.Generation != before.Generation {
				t.Fatal("absent child was admitted or lost deletion evidence")
			}
			var current models.EmbyMediaItem
			err = db.Db.Where("item_id = ?", "102").First(&current).Error
			if mode == "full" {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("absent child survived complete full scan: %v", err)
				}
			} else if err != nil || current.PartOfItemID != "" || current.SnapshotID != before.SnapshotID {
				t.Fatalf("current membership not detached safely: %+v, %v", current, err)
			}
			var evidence models.EmbyItemEvidence
			if err := db.Db.First(&evidence, before.SnapshotID).Error; err != nil {
				t.Fatal(err)
			}
			var historical models.EmbyMediaItem
			if err := json.Unmarshal([]byte(evidence.ItemJSON), &historical); err != nil || historical.PartOfItemID != "101" {
				t.Fatal("historical child membership changed")
			}
			for _, record := range f.records {
				if current := readWebhookTest(t, record.ID); current.Status != models.EmbyWebhookUnresolved || current.InputJSON != record.InputJSON {
					t.Fatal("membership cleanup changed deletion work")
				}
			}
			if f.provider.calls != 0 {
				t.Fatal("index recovery invoked cloud deletion")
			}
		})
	}
}
