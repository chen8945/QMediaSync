package emby

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 真实响应的字段筛选、脱敏和测试补充见 testdata/audit-corpus-provenance.md。
type auditCorpus struct {
	Version         string                                      `json:"version"`
	Libraries       []embyclientrestgo.EmbyLibrary              `json:"libraries"`
	ItemsByLibrary  map[string][]embyclientrestgo.BaseItemDtoV2 `json:"items_by_library"`
	AdditionalParts map[string][]embyclientrestgo.BaseItemDtoV2 `json:"additional_parts"`
	SyncFiles       []models.SyncFile                           `json:"sync_files"`
}

type auditCorpusRequest struct {
	Path  string
	Query url.Values
}

type auditCorpusReplay struct {
	fixture  auditCorpus
	mu       sync.Mutex
	requests []auditCorpusRequest
}

func setupAuditCorpus(t *testing.T) *auditCorpusReplay {
	t.Helper()
	data, err := os.ReadFile("testdata/audit-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	replay := &auditCorpusReplay{}
	if err := json.Unmarshal(data, &replay.fixture); err != nil {
		t.Fatal(err)
	}
	if replay.fixture.Version != "4.10.1.0" || len(replay.fixture.SyncFiles) != 157 {
		t.Fatal("unexpected audited fixture version or physical file count")
	}

	previousDB, previousConfig, previousLogger := db.Db, models.GlobalEmbyConfig, helpers.AppLogger
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Db, models.GlobalEmbyConfig = testDB, nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	SetEmbySyncRunning(false)
	t.Cleanup(func() {
		SetEmbySyncRunning(false)
		db.Db, models.GlobalEmbyConfig, helpers.AppLogger = previousDB, previousConfig, previousLogger
		_ = sqlDB.Close()
	})
	setupSnapshotTestTables(t)
	account := models.Account{
		BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115, UserId: "fixture-owner",
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	for _, root := range []models.SyncPath{
		{
			BaseModel: models.BaseModel{ID: 2}, SourceType: models.SourceType115, AccountId: 1,
			BaseCid: "fixture-movie-root", LocalPath: "/audit/local", RemotePath: "movies",
		},
		{
			BaseModel: models.BaseModel{ID: 3}, SourceType: models.SourceType115, AccountId: 1,
			BaseCid: "fixture-tv-root", LocalPath: "/audit/local", RemotePath: "tv",
		},
	} {
		if err := db.Db.Create(&root).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Db.CreateInBatches(&replay.fixture.SyncFiles, 50).Error; err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(replay.serveHTTP))
	t.Cleanup(server.Close)
	if err := db.Db.Create(&models.EmbyConfig{
		EmbyUrl: server.URL, EmbyApiKey: "offline-fixture-only", SyncEnabled: 1, SyncAllLibraries: 1,
		LastSavedCursorAt: 1782698400,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return replay
}

func (replay *auditCorpusReplay) serveHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	replay.mu.Lock()
	replay.requests = append(replay.requests, auditCorpusRequest{Path: r.URL.Path, Query: query})
	replay.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/emby/System/Info/Public":
		_, _ = io.WriteString(w, `{"Id":"server-a","Version":"4.10.1.0"}`)
	case "/emby/Users":
		_, _ = io.WriteString(w, `[{"Id":"fixture-user","Policy":{"EnableAllFolders":true}}]`)
	case "/emby/Library/MediaFolders":
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": replay.fixture.Libraries})
	case "/emby/Items":
		if query.Get("Ids") != "" {
			// 已捕获的分段响应具有完整路径和源，不应无条件再查询详情。
			http.Error(w, "unexpected detail request for complete captured fields", http.StatusBadRequest)
			return
		}
		items, ok := replay.fixture.ItemsByLibrary[query.Get("ParentId")]
		if !ok {
			http.Error(w, "unknown library", http.StatusBadRequest)
			return
		}
		start, startErr := strconv.Atoi(query.Get("StartIndex"))
		limit, limitErr := strconv.Atoi(query.Get("Limit"))
		if startErr != nil || limitErr != nil || start < 0 || start > len(items) || limit <= 0 {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		page := slices.Clone(items[start:min(start+limit, len(items))])
		fields := strings.Split(query.Get("Fields"), ",")
		for i := range page {
			if !slices.Contains(fields, "Path") {
				page[i].Path = ""
			}
			if !slices.Contains(fields, "PartCount") {
				page[i].PartCount = 0
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"TotalRecordCount": len(items), "Items": page})
	default:
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/emby/Videos/"), "/AdditionalParts")
		parts, ok := replay.fixture.AdditionalParts[id]
		if !ok || r.URL.Path != "/emby/Videos/"+id+"/AdditionalParts" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"TotalRecordCount": len(parts), "Items": parts})
	}
}

func TestEmbyAuditCorpusIndexesEveryPhysicalFile(t *testing.T) {
	for _, mode := range []struct {
		name string
		run  func() (int, error)
	}{
		{name: "full", run: PerformEmbySync},
		{name: "incremental", run: PerformEmbyIncrementalSync},
	} {
		t.Run(mode.name, func(t *testing.T) {
			// 生产包使用全局数据库和同步状态，不能并行重设这些依赖。
			replay := setupAuditCorpus(t)
			before := time.Now().Unix()
			count, err := mode.run()
			if err != nil || count != 157 {
				t.Fatalf("%s replay: processed=%d error=%v, want 157 physical files", mode.name, count, err)
			}
			replay.assertMappings(t)
			replay.assertRequests(t, mode.name)
			var config models.EmbyConfig
			if err := db.Db.First(&config).Error; err != nil {
				t.Fatal(err)
			}
			if config.IsRunning || config.LastError != "" || config.LastProcessedCount != 157 {
				t.Fatalf("incorrect completion state: running=%v error=%q count=%d", config.IsRunning, config.LastError, config.LastProcessedCount)
			}
			if mode.name == "incremental" && (config.LastSavedCursorAt < before || config.LastSavedCursorAt > time.Now().Unix()) {
				t.Fatalf("cursor=%d outside scan-start window", config.LastSavedCursorAt)
			}
			// 同一真实响应重放不能累加当前关联或改变物理文件代际。
			var firstItems []models.EmbyMediaItem
			if err := db.Db.Find(&firstItems).Error; err != nil {
				t.Fatal(err)
			}
			count, err = mode.run()
			if err != nil || count != 157 {
				t.Fatalf("repeat %s replay: processed=%d error=%v", mode.name, count, err)
			}
			replay.assertMappings(t)
			for _, previous := range firstItems {
				var current models.EmbyMediaItem
				if err := db.Db.Where("item_id = ?", previous.ItemId).First(&current).Error; err != nil {
					t.Fatal(err)
				}
				if current.Generation != previous.Generation || current.CreatedAt != previous.CreatedAt || current.ID != previous.ID {
					t.Fatalf("unchanged item %s lost generation or local identity", previous.ItemId)
				}
			}
		})
	}
}

func (replay *auditCorpusReplay) assertMappings(t *testing.T) {
	t.Helper()
	var items []models.EmbyMediaItem
	var links []models.EmbyMediaSyncFile
	if err := db.Db.Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Find(&links).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 157 || len(links) != 157 {
		t.Fatalf("items=%d links=%d; want one item and link for each of 157 files", len(items), len(links))
	}
	byID := make(map[string]models.EmbyMediaItem, len(items))
	linksByID := make(map[string][]models.EmbyMediaSyncFile, len(links))
	for _, item := range items {
		byID[item.ItemId] = item
	}
	for _, link := range links {
		id := strconv.FormatUint(uint64(link.EmbyItemId), 10)
		linksByID[id] = append(linksByID[id], link)
	}
	expected := make(map[string]embyclientrestgo.BaseItemDtoV2, 157)
	libraries := make(map[string]string, 157)
	parents := map[string]string{"20": "19", "21": "19", "22": "19", "1195": "1194"}
	for library, rows := range replay.fixture.ItemsByLibrary {
		for _, item := range rows {
			if item.Type == "Movie" || item.Type == "Episode" {
				expected[item.Id], libraries[item.Id] = item, library
			}
		}
	}
	for parent, parts := range replay.fixture.AdditionalParts {
		for _, item := range parts {
			expected[item.Id], libraries[item.Id] = item, libraries[parent]
		}
	}
	filesByCode := make(map[string]models.SyncFile, 157)
	for _, file := range replay.fixture.SyncFiles {
		filesByCode[file.PickCode] = file
	}
	seenFiles := make(map[uint]bool, 157)
	for id, want := range expected {
		got, found := byID[id]
		if !found || len(linksByID[id]) != 1 || len(want.MediaSources) != 1 {
			t.Fatalf("item %s: found=%v links=%d expected sources=%d", id, found, len(linksByID[id]), len(want.MediaSources))
		}
		source := want.MediaSources[0]
		parsed, err := url.Parse(source.Path)
		if err != nil {
			t.Fatal(err)
		}
		code := parsed.Query().Get("pickcode")
		file, found := filesByCode[code]
		link := linksByID[id][0]
		if !found || link.SyncFileId != file.ID || link.SyncPathId != file.SyncPathId || seenFiles[file.ID] {
			t.Fatalf("item %s does not map to its unique captured SyncFile", id)
		}
		seenFiles[file.ID] = true
		if got.Path != want.Path || got.Path != file.LocalFilePath || got.MediaSourcePath != source.Path || got.PickCode != code {
			t.Fatalf("item %s lost physical STRM path or playback source separation", id)
		}
		if got.Type != want.Type || got.LibraryId != libraries[id] || got.PartOfItemID != parents[id] || got.VersionOfItemID != "" {
			t.Fatalf("item %s has incorrect type/library/part/version membership: %+v", id, got)
		}
		if parents[id] == "" && (got.SeriesId != want.SeriesId || got.SeasonId != want.SeasonId) {
			t.Fatalf("item %s changed captured season/series membership", id)
		}
		if got.ServerId != "server-a" || got.Generation <= 0 || got.SnapshotID == 0 || link.SnapshotID != got.SnapshotID || link.SourceID != source.ID {
			t.Fatalf("item %s lost server/source/snapshot identity", id)
		}
		var evidence models.EmbyItemEvidence
		if err := db.Db.First(&evidence, got.SnapshotID).Error; err != nil {
			t.Fatal(err)
		}
		var sources []models.EmbySnapshotSource
		if err := json.Unmarshal([]byte(evidence.SourcesJSON), &sources); err != nil {
			t.Fatal(err)
		}
		if evidence.ServerID != "server-a" || evidence.ItemID != id || evidence.ConfigKey == "" || len(sources) != 1 || sources[0].ID != source.ID || sources[0].ItemID != source.ItemID {
			t.Fatalf("item %s lost frozen server/source item identity", id)
		}
		var frozen []models.EmbyFrozenFile
		if err := json.Unmarshal([]byte(evidence.FilesJSON), &frozen); err != nil {
			t.Fatal(err)
		}
		if len(frozen) != 1 || frozen[0].Reason != "" || frozen[0].AccountID != 1 || frozen[0].AccountIdentity == "" || frozen[0].FileID != file.FileId || frozen[0].SHA1 != file.Sha1 {
			t.Fatalf("item %s frozen identity incomplete: %+v", id, frozen)
		}
		if parent := parents[id]; parent != "" {
			owner := byID[parent]
			if got.SeriesId != owner.SeriesId || got.SeasonId != owner.SeasonId {
				t.Fatalf("part %s did not inherit parent %s series/season context", id, parent)
			}
		}
	}
	if len(expected) != 157 || len(seenFiles) != 157 {
		t.Fatalf("expected=%d unique files=%d, want 157", len(expected), len(seenFiles))
	}
	if byID["19"].PartCount != 4 || byID["1194"].PartCount != 2 {
		t.Fatal("multipart main item counts were not retained")
	}
	// 16/17/18 是同片的三个物理版本，不能按名称合并或交叉绑定。
	for id, fileID := range map[string]uint{"16": 526, "17": 505, "18": 544} {
		if linksByID[id][0].SyncFileId != fileID || byID[id].Name != byID["16"].Name {
			t.Fatalf("physical version %s lost its captured file %d or common title", id, fileID)
		}
	}
}

func (replay *auditCorpusReplay) assertRequests(t *testing.T, mode string) {
	t.Helper()
	replay.mu.Lock()
	defer replay.mu.Unlock()
	pageCounts := map[string]int{}
	parts := map[string]int{}
	for _, request := range replay.requests {
		if strings.HasSuffix(request.Path, "/AdditionalParts") {
			parts[request.Path]++
		}
		if request.Path != "/emby/Items" && !strings.HasSuffix(request.Path, "/AdditionalParts") {
			continue
		}
		fields := strings.Split(request.Query.Get("Fields"), ",")
		for _, field := range []string{"Path", "MediaSources", "PartCount"} {
			if !slices.Contains(fields, field) {
				t.Errorf("%s did not request required field %s", request.Path, field)
			}
		}
		if request.Path != "/emby/Items" {
			continue
		}
		pageCounts[request.Query.Get("ParentId")]++
		wantSort, wantMinimum := "DateCreated", ""
		if mode == "incremental" {
			wantSort, wantMinimum = "DateLastSaved", "2026-06-29T01:50:00Z"
		}
		if request.Query.Get("SortBy") != wantSort || request.Query.Get("MinDateLastSaved") != wantMinimum || request.Query.Get("SortOrder") != "Descending" {
			t.Errorf("unexpected %s paging query: %v", mode, request.Query)
		}
		if request.Query.Get("UserId") != "" || request.Query.Get("Limit") != "100" {
			t.Errorf("physical enumeration must remain ungrouped and paginated: %v", request.Query)
		}
	}
	if pageCounts["3"] != 1 || pageCounts["1152"] != 2 || pageCounts["595"] != 1 {
		t.Fatalf("incomplete physical library pagination: %v", pageCounts)
	}
	for _, id := range []string{"19", "1194"} {
		if parts[fmt.Sprintf("/emby/Videos/%s/AdditionalParts", id)] != 1 {
			t.Errorf("main item %s did not fetch its hidden parts exactly once", id)
		}
	}
	if len(parts) != 2 {
		t.Errorf("unexpected additional-part requests: %v", parts)
	}
}
