package emby

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/emby/client"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

const (
	jellyfinVersionA = "a1111111111111111111111111111111"
	jellyfinVersionB = "b2222222222222222222222222222222"
	jellyfinPart     = "c3333333333333333333333333333333"
	jellyfinLibrary  = "d4444444444444444444444444444444"
)

func jellyfinVersionItems(mediaType string) map[string]embyclientrestgo.BaseItemDtoV2 {
	items := make(map[string]embyclientrestgo.BaseItemDtoV2)
	for _, id := range []string{jellyfinVersionA, jellyfinVersionB, jellyfinPart} {
		item := embyclientrestgo.BaseItemDtoV2{Id: id, Type: mediaType, Name: "same name", DateCreated: "2026-06-29T00:00:00Z", Path: "/jellyfin/" + id + ".strm"}
		if id == jellyfinPart {
			item.MediaSources = []embyclientrestgo.MediaSource{{ID: id, Type: "Default", Path: "http://qms/stream?pickcode=" + id}}
		} else {
			// Jellyfin 静态来源使用物理版本 GUID，没有 Emby 的 ItemId；STRM 来源可以是 URL。
			item.MediaSources = []embyclientrestgo.MediaSource{
				{ID: strings.ToUpper(jellyfinVersionB), Type: "Grouping", Path: "http://qms/stream?pickcode=" + jellyfinVersionB},
				{ID: "a1111111-1111-1111-1111-111111111111", Type: "Default", Path: "http://qms/stream?pickcode=" + jellyfinVersionA},
			}
		}
		items[id] = item
	}
	return items
}

func TestJellyfinSnapshotsKeepStablePhysicalGroups(t *testing.T) {
	for _, mediaType := range []string{"Movie", "Episode"} {
		for _, failure := range []string{"", "missing_version", "different_group", "duplicate_version", "missing_part", "duplicate_part", "unexpected_part_detail", "dynamic_source"} {
			t.Run(mediaType+"/"+failure, func(t *testing.T) {
				items := jellyfinVersionItems(mediaType)
				root := items[jellyfinVersionA]
				root.PartCount = 2
				if failure == "duplicate_part" {
					root.PartCount = 3
				}
				items[root.Id] = root
				var currentEntry string
				var fixtureMu sync.RWMutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fixtureMu.RLock()
					defer fixtureMu.RUnlock()
					var result []embyclientrestgo.BaseItemDtoV2
					switch r.URL.Path {
					case "/System/Info/Public":
						fmt.Fprint(w, `{"Id":"jellyfin-test","ProductName":"Jellyfin","Version":"12.4"}`)
						return
					case "/Items":
						for _, id := range strings.Split(r.URL.Query().Get("Ids"), ",") {
							item := items[id]
							if failure == "missing_version" && id != currentEntry {
								continue
							}
							if failure == "different_group" && id != currentEntry {
								item.MediaSources = []embyclientrestgo.MediaSource{
									{ID: id, Type: "Default", Path: "http://qms/stream?pickcode=" + id},
									{ID: jellyfinPart, Type: "Default", Path: "http://qms/stream?pickcode=" + jellyfinPart},
								}
							}
							if failure == "dynamic_source" && id == jellyfinVersionB {
								item.SourceType = "Channel"
							}
							if failure == "unexpected_part_detail" && id == jellyfinPart {
								item = items[jellyfinVersionB]
							}
							result = append(result, item)
							if failure == "duplicate_version" && id == jellyfinVersionB {
								result = append(result, item)
							}
						}
					case "/Videos/" + jellyfinVersionA + "/AdditionalParts":
						if failure != "missing_part" {
							part := items[jellyfinPart]
							if failure == "unexpected_part_detail" {
								part.Path, part.MediaSources = "", nil
							}
							result = append(result, part)
							if failure == "duplicate_part" {
								result = append(result, part)
							}
						}
					default:
						http.NotFound(w, r)
						return
					}
					if err := json.NewEncoder(w).Encode(embyclientrestgo.QueryResultBaseItemDto{Items: result, TotalRecordCount: int32(len(result))}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				for _, entry := range []string{jellyfinVersionA, jellyfinVersionB} {
					fixtureMu.Lock()
					currentEntry = entry
					fixtureMu.Unlock()
					client := embyclientrestgo.NewClient(server.URL, "test")
					initial, err := client.GetSnapshotItems(t.Context(), entry)
					if err == nil && len(initial) == 1 {
						var snapshots []models.EmbyItemSnapshot
						snapshots, err = collectEmbySnapshots(t.Context(), client, initial[0], jellyfinLibrary, "library", "run", 1)
						if failure == "" && err == nil {
							if len(snapshots) != 3 {
								t.Fatalf("entry=%s: snapshots=%d, want 3", entry, len(snapshots))
							}
							for _, snapshot := range snapshots {
								item := snapshot.Item
								if item.ItemId == jellyfinVersionB && item.VersionOfItemID != jellyfinVersionA || item.ItemId == jellyfinVersionA && item.VersionOfItemID != "" || item.ItemId == jellyfinPart && item.PartOfItemID != jellyfinVersionA {
									t.Fatalf("entry=%s: incorrect physical group: %+v", entry, item)
								}
								if len(snapshot.Sources) != 1 || snapshot.Sources[0].ItemID != item.ItemId || snapshot.Sources[0].PickCode != item.ItemId {
									t.Fatalf("entry=%s: source belongs to another item: %+v", entry, snapshot)
								}
							}
							continue
						}
					}
					if failure == "" || err == nil {
						t.Fatalf("entry=%s: error=%v, expected failure=%q", entry, err, failure)
					}
				}
			})
		}
	}
}

func TestJellyfinLegacyHiddenVersionKeepsPhysicalOwnership(t *testing.T) {
	items := jellyfinVersionItems("Movie")
	hidden := items[jellyfinVersionB]
	hidden.Type = "Video"
	hidden.MediaSources = []embyclientrestgo.MediaSource{{ID: hidden.Id, Type: "Default", Path: "http://qms/stream?pickcode=" + hidden.Id}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprint(w, `{"Id":"legacy-jellyfin","ProductName":"Jellyfin Server","Version":"10.11.11"}`)
		case "/Items":
			result := []embyclientrestgo.BaseItemDtoV2{}
			if r.URL.Query().Get("Ids") == jellyfinVersionA {
				result = append(result, items[jellyfinVersionA])
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"Items": result, "TotalRecordCount": len(result)}); err != nil {
				t.Error(err)
			}
		case "/Users":
			fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true}}]`, jellyfinLibrary)
		case "/Items/" + jellyfinVersionB:
			if r.URL.Query().Get("UserId") != jellyfinLibrary {
				t.Error("隐藏版本未使用库用户读取详情")
			}
			if err := json.NewEncoder(w).Encode(hidden); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := embyclientrestgo.NewClient(server.URL, "test")
	initial, err := client.GetSnapshotItems(t.Context(), jellyfinVersionA)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial=%v error=%v", initial, err)
	}
	snapshots, err := collectEmbySnapshots(t.Context(), client, initial[0], jellyfinLibrary, "library", "run", 1)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("隐藏 Video 版本未完整补查：snapshots=%v error=%v", snapshots, err)
	}
	for _, snapshot := range snapshots {
		if snapshot.Item.ItemId == hidden.Id && (snapshot.Item.Type != "Video" || snapshot.Item.VersionOfItemID != jellyfinVersionA || snapshot.Item.Path != hidden.Path) {
			t.Fatalf("隐藏版本改变物理身份：%+v", snapshot.Item)
		}
		if len(snapshot.Sources) != 1 || snapshot.Sources[0].ItemID != snapshot.Item.ItemId || snapshot.Sources[0].PickCode != snapshot.Item.ItemId {
			t.Fatalf("版本来源归属错误：%+v", snapshot)
		}
	}
}

func TestJellyfinSyncPersistsGUIDLinksAndPreservesFailedRun(t *testing.T) {
	previousDB, previousLogger, previousConfig := db.Db, helpers.AppLogger, models.GlobalEmbyConfig
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "jellyfin.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		SetEmbySyncRunning(false)
		db.Db, helpers.AppLogger, models.GlobalEmbyConfig = previousDB, previousLogger, previousConfig
		_ = sqlDB.Close()
	})
	db.Db, models.GlobalEmbyConfig = conn, nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	SetEmbySyncRunning(false)
	setupSnapshotTestTables(t)
	items := jellyfinVersionItems("Episode")
	entryIDs := []string{jellyfinVersionB, jellyfinVersionA}
	failParts := false
	duplicatePage := false
	malformedPage := false
	pageQueries := make(chan url.Values, 10)
	var fixtureMu sync.RWMutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixtureMu.RLock()
		defer fixtureMu.RUnlock()
		var result []embyclientrestgo.BaseItemDtoV2
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprint(w, `{"Id":"jellyfin-test","ProductName":"Jellyfin","Version":"12.4"}`)
			return
		case "/Users":
			fmt.Fprint(w, `[{"Id":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","Policy":{"EnableAllFolders":true}}]`)
			return
		case "/Library/MediaFolders":
			fmt.Fprintf(w, `{"Items":[{"Id":%q,"Name":"library"}]}`, jellyfinLibrary)
			return
		case "/Items":
			ids := entryIDs
			if requested := r.URL.Query().Get("Ids"); requested != "" {
				ids = strings.Split(requested, ",")
			} else {
				pageQueries <- r.URL.Query()
				if malformedPage {
					fmt.Fprint(w, `{}`)
					return
				}
			}
			for _, id := range ids {
				item := items[id]
				if id == jellyfinVersionA {
					item.PartCount = 2
				}
				result = append(result, item)
			}
		case "/Videos/" + jellyfinVersionA + "/AdditionalParts":
			if !failParts {
				result = append(result, items[jellyfinPart])
			}
		default:
			http.NotFound(w, r)
			return
		}
		total := int32(len(result))
		if duplicatePage && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "" {
			total++
		}
		if err := json.NewEncoder(w).Encode(embyclientrestgo.QueryResultBaseItemDto{Items: result, TotalRecordCount: total}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	config := models.EmbyConfig{
		EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, LastSavedCursorAt: 100,
		PathMappingsJSON: `[{"emby_root":"/jellyfin","local_root":"/local"}]`,
	}
	if err := conn.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	files := make(map[string]uint)
	for _, id := range []string{jellyfinVersionA, jellyfinVersionB, jellyfinPart} {
		file := models.SyncFile{PickCode: id, SyncPathId: 1, LocalFilePath: "/local/" + id + ".strm"}
		if err := conn.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		files[id] = file.ID
		if err := conn.Create(&models.SyncFile{PickCode: id, SyncPathId: 1, LocalFilePath: "/other/" + id + ".strm"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []struct {
		name string
		sync func() (int, error)
	}{{"full", PerformEmbySync}, {"incremental", PerformEmbyIncrementalSync}, {"repeat_full", PerformEmbySync}} {
		t.Run(run.name, func(t *testing.T) {
			var before models.EmbyConfig
			if err := conn.First(&before).Error; err != nil {
				t.Fatal(err)
			}
			fixtureMu.Lock()
			if run.name == "incremental" {
				changed := items[jellyfinVersionB]
				changed.Name = "updated by incremental scan"
				items[jellyfinVersionB] = changed
			}
			slices.Reverse(entryIDs)
			fixtureMu.Unlock()
			startedAt := time.Now().Unix()
			if count, err := run.sync(); err != nil || count != 3 {
				t.Fatalf("sync count=%d error=%v", count, err)
			}
			query := <-pageQueries
			wantMinDate := ""
			if run.name == "incremental" {
				wantMinDate = time.Unix(before.LastSavedCursorAt-600, 0).UTC().Format(time.RFC3339)
			}
			if query.Get("MinDateLastSaved") != wantMinDate || query.Get("SortBy") != "DateCreated,SortName" {
				t.Fatalf("Jellyfin scan query = %v, want MinDateLastSaved=%q with supported sorting", query, wantMinDate)
			}
			var after models.EmbyConfig
			if err := conn.First(&after).Error; err != nil || after.LastSavedCursorAt < startedAt || after.LastSavedCursorAt > time.Now().Unix() {
				t.Fatalf("success cursor outside scan-start window: %+v error=%v", after, err)
			}
			var links []models.EmbyMediaSyncFile
			if err := conn.Find(&links).Error; err != nil || len(links) != 3 {
				t.Fatalf("GUID associations=%d error=%v", len(links), err)
			}
			for _, link := range links {
				if link.SyncFileId != files[link.EmbyItemId] || link.SnapshotID == 0 {
					t.Fatalf("association lost remote GUID: %+v", link)
				}
			}
			var records []models.EmbyMediaItem
			if err := conn.Find(&records).Error; err != nil || len(records) != 3 {
				t.Fatalf("GUID records=%d error=%v", len(records), err)
			}
			for _, record := range records {
				if record.ItemIdInt != 0 || record.LibraryId != jellyfinLibrary || record.Path != "/jellyfin/"+record.ItemId+".strm" || record.Name != items[record.ItemId].Name {
					t.Fatalf("GUID/path semantics changed: %+v", record)
				}
			}
		})
	}
	var before models.EmbyConfig
	if err := conn.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	old := models.EmbyMediaItem{ItemId: "f5555555555555555555555555555555", LibraryId: jellyfinLibrary, LastSeenSyncRun: "old"}
	if err := conn.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"missing_part", "duplicate_page", "malformed_page"} {
		fixtureMu.Lock()
		failParts, duplicatePage = failure == "missing_part", failure == "duplicate_page"
		malformedPage = failure == "malformed_page"
		fixtureMu.Unlock()
		for _, run := range []struct {
			name string
			sync func() (int, error)
		}{{"full", PerformEmbySync}, {"incremental", PerformEmbyIncrementalSync}} {
			t.Run(failure+"/"+run.name, func(t *testing.T) {
				if _, err := run.sync(); err == nil {
					t.Fatal("incomplete Jellyfin scan reported success")
				}
				var after models.EmbyConfig
				if err := conn.First(&after).Error; err != nil || after.LastSavedCursorAt != before.LastSavedCursorAt || after.LastFullSyncAt != before.LastFullSyncAt || after.LastError == "" {
					t.Fatalf("failure advanced success state: %+v error=%v", after, err)
				}
				var remaining int64
				if err := conn.Model(&models.EmbyMediaItem{}).Count(&remaining).Error; err != nil || remaining != 4 {
					t.Fatalf("failure cleaned existing index: count=%d error=%v", remaining, err)
				}
			})
		}
	}
}
