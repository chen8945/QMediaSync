package emby

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type pipelineSQLCounter struct {
	logger.Interface
	statements atomic.Int64
}

func (l *pipelineSQLCounter) Trace(_ context.Context, _ time.Time, _ func() (string, int64), _ error) {
	l.statements.Add(1)
}

func TestEmbySnapshotPipelineFullAndIncrementalUseOnlyLocalCloudEvidence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		distinct      bool
		deleteEnabled int
		incremental   bool
	}{
		{"full_shared_folder_delete_off", false, 0, false},
		{"full_distinct_folders_delete_on", true, 1, false},
		{"incremental_distinct_folders_delete_off", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldDB, oldConfig := db.Db, models.GlobalEmbyConfig
			oldLog, old115Log := helpers.AppLogger, helpers.V115Log
			t.Cleanup(func() {
				db.Db, models.GlobalEmbyConfig = oldDB, oldConfig
				helpers.AppLogger, helpers.V115Log = oldLog, old115Log
				SetEmbySyncRunning(false)
			})
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			helpers.V115Log = helpers.AppLogger
			sqlLog := &pipelineSQLCounter{Interface: logger.Default.LogMode(logger.Silent)}
			conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: sqlLog})
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
			models.GlobalEmbyConfig = nil
			SetEmbySyncRunning(false)
			setupSnapshotTestTables(t)
			if err := conn.AutoMigrate(&models.Media{}, &models.ScrapePath{}, &models.SyncRootEvidence{}); err != nil {
				t.Fatal(err)
			}
			account := models.Account{BaseModel: models.BaseModel{ID: uint(90_000_000 + cleanupSourceAccountID.Add(1))}, SourceType: models.SourceType115, UserId: "diagnosis-fixture", Token: "fixture"}
			if err := conn.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			syncRoot := models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "1", RemotePath: "/movies", LocalPath: t.TempDir()}
			if err := conn.Create(&syncRoot).Error; err != nil {
				t.Fatal(err)
			}
			rootEvidence := models.SyncRootEvidence{
				SyncPathID: syncRoot.ID, SourceType: syncRoot.SourceType, AccountID: account.ID,
				AccountIdentity: models.EmbyAccountIdentity(account), RootFileID: "1", RootPath: "/movies", ObservedAt: 123,
				Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "1", ParentID: "0", Path: "/movies"}},
			}
			if err := conn.Transaction(func(tx *gorm.DB) error { return models.SaveSyncRootEvidenceTx(tx, rootEvidence, "1") }); err != nil {
				t.Fatal(err)
			}
			var items []embyclientrestgo.BaseItemDtoV2
			directoryNames := map[string]string{}
			byID := map[string]embyclientrestgo.BaseItemDtoV2{}
			for i := range 6 {
				folder := 0
				if tc.distinct {
					folder = i
				}
				id, parentID := strconv.Itoa(101+i), strconv.Itoa(201+folder)
				directory := "movie-" + strconv.Itoa(folder)
				localPath := filepath.Join(syncRoot.GetFullLocalPath(), directory, id+".strm")
				playback := "http://qms/stream?pickcode=pc-" + id
				if err := os.MkdirAll(filepath.Dir(localPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(localPath, []byte(playback), 0600); err != nil {
					t.Fatal(err)
				}
				file := models.SyncFile{SourceType: models.SourceType115, AccountId: account.ID, SyncPathId: syncRoot.ID, FileId: "file-" + id, ParentId: parentID, FileName: id + ".mkv", Path: "/movies/" + directory, LocalFilePath: localPath, PickCode: "pc-" + id, Sha1: "hash-" + id, FileSize: 100, MTime: 123, IsVideo: true}
				if err := conn.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
				item := embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Name: "movie " + id, Path: localPath, ParentId: parentID, PartCount: 1, MediaSources: []embyclientrestgo.MediaSource{{ID: "source-" + id, ItemID: id, Path: playback}}}
				items = append(items, item)
				byID[id] = item
				if _, ok := directoryNames[parentID]; !ok {
					directoryRow := models.SyncFile{SourceType: syncRoot.SourceType, AccountId: account.ID, SyncPathId: syncRoot.ID,
						FileId: parentID, ParentId: "1", FileName: directory, Path: "/movies", FileType: v115open.TypeDir,
						LocalFilePath: filepath.Dir(localPath)}
					if err := conn.Create(&directoryRow).Error; err != nil {
						t.Fatal(err)
					}
				}
				directoryNames[parentID] = directory
			}
			var cloudRequests, embyRequests, ancestorRequests atomic.Int64
			account.Get115Client().SetTransport(cleanupSourceHTTPTransport(func(w http.ResponseWriter, r *http.Request) {
				cloudRequests.Add(1)
				t.Errorf("同步不应查询网盘：%s %s", r.Method, r.URL.Path)
				http.Error(w, "unexpected cloud request", http.StatusBadRequest)
			}))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				embyRequests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/emby/System/Info/Public":
					fmt.Fprint(w, `{"Id":"diagnosis-server"}`)
				case r.URL.Path == "/emby/Users":
					fmt.Fprint(w, `[{"Id":"user","Policy":{"EnableAllFolders":true}}]`)
				case r.URL.Path == "/emby/Library/MediaFolders":
					fmt.Fprint(w, `{"Items":[{"Id":"lib","Name":"Movies"}]}`)
				case r.URL.Path == "/emby/Items":
					json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
				case strings.HasSuffix(r.URL.Path, "/Ancestors"):
					ancestorRequests.Add(1)
					id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/emby/Items/"), "/Ancestors")
					item := byID[id]
					json.NewEncoder(w).Encode([]map[string]any{{"Id": item.ParentId, "Type": "Folder", "Path": filepath.Dir(item.Path), "IsFolder": true}})
				default:
					t.Errorf("unexpected Emby request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			config := models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: tc.deleteEnabled}
			if err := conn.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			for round := 1; round <= 2; round++ {
				cloudBefore, embyBefore, ancestorsBefore := cloudRequests.Load(), embyRequests.Load(), ancestorRequests.Load()
				sqlLog.statements.Store(0)
				startedAt := time.Now()
				run := PerformEmbySync
				if tc.incremental {
					run = PerformEmbyIncrementalSync
				}
				processed, err := run()
				elapsed := time.Since(startedAt)
				sqlCount := sqlLog.statements.Load()
				if err != nil || processed != len(items) {
					t.Fatalf("round %d: processed=%d err=%v", round, processed, err)
				}
				cloudDelta, embyDelta, ancestorDelta := cloudRequests.Load()-cloudBefore, embyRequests.Load()-embyBefore, ancestorRequests.Load()-ancestorsBefore
				if cloudDelta != 0 {
					t.Fatalf("round %d: cloud=%d, want 0", round, cloudDelta)
				}
				if tc.distinct && ancestorDelta != int64(len(items)) || !tc.distinct && ancestorDelta != 1 {
					t.Fatalf("round %d: ancestors=%d, workers must share each directory observation", round, ancestorDelta)
				}
				baseRequests := int64(4)
				if tc.incremental {
					baseRequests = 3
				}
				if embyDelta != baseRequests+ancestorDelta {
					t.Fatalf("round %d: Emby HTTP=%d, want %d", round, embyDelta, baseRequests+ancestorDelta)
				}
				var evidence models.EmbyItemEvidence
				if err := conn.Table("emby_item_evidences AS e").Select("e.*").Joins("JOIN emby_item_states AS s ON s.snapshot_id = e.id").Where("s.item_id = ?", "101").First(&evidence).Error; err != nil {
					t.Fatal(err)
				}
				metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
				if err != nil || len(metadata.DirectoryScopes) != 1 {
					t.Fatalf("failed to persist valid scope: scopes=%d err=%v", len(metadata.DirectoryScopes), err)
				}
				t.Logf("round=%d items=%d delete_enabled=%d cloud_http=%d emby_http=%d emby_ancestors=%d sql_statements=%d elapsed=%s", round, processed, tc.deleteEnabled, cloudDelta, embyDelta, ancestorDelta, sqlCount, elapsed)
			}
		})
	}
}
