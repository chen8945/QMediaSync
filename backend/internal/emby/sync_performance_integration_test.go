//go:build integration

package emby

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

const (
	embyPerformanceItems       = 20_000
	embyPerformanceDirectories = 2_000
)

type embyPerformanceSQLCounter struct {
	logger.Interface
	statements atomic.Int64
	failures   atomic.Int64
	elapsed    atomic.Int64
	txCount    atomic.Int64
	txElapsed  atomic.Int64
}

func (l *embyPerformanceSQLCounter) Trace(_ context.Context, started time.Time, _ func() (string, int64), err error) {
	l.statements.Add(1)
	l.elapsed.Add(time.Since(started).Nanoseconds())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		l.failures.Add(1)
	}
}

// embyPerformancePool 累计 BeginTx 调用至提交返回的耗时，并行事务区间可能重叠。
type embyPerformancePool struct {
	*sql.DB
	counter *embyPerformanceSQLCounter
}

func (p embyPerformancePool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

func (p embyPerformancePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	started := time.Now()
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &embyPerformanceTx{Tx: tx, counter: p.counter, started: started}, nil
}

type embyPerformanceTx struct {
	*sql.Tx
	counter *embyPerformanceSQLCounter
	started time.Time
}

func (tx *embyPerformanceTx) Commit() error {
	err := tx.Tx.Commit()
	tx.counter.txCount.Add(1)
	tx.counter.txElapsed.Add(time.Since(tx.started).Nanoseconds())
	return err
}

type embyPerformanceHTTPCounts struct {
	embyTotal atomic.Int64
	pages     atomic.Int64
	ancestors atomic.Int64
	cloud     atomic.Int64
	pageSize  atomic.Int64
}

// embyPerformanceTransport 只将测试账号请求转发到真实本地 HTTP 服务，不合成响应。
type embyPerformanceTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (p embyPerformanceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "proapi.115.com" {
		return nil, fmt.Errorf("unexpected 115 fixture host: %s", req.URL.Host)
	}
	forward := req.Clone(req.Context())
	forward.URL.Scheme, forward.URL.Host = p.target.Scheme, p.target.Host
	forward.Host = p.target.Host
	return p.base.RoundTrip(forward)
}

// TestEmbySyncPerformancePostgres 需要显式启用，并独占测试进程；耗时仅代表本地受控环境。
func TestEmbySyncPerformancePostgres(t *testing.T) {
	if os.Getenv("QMS_EMBY_PERFORMANCE") != "1" {
		t.Skip("requires QMS_EMBY_PERFORMANCE=1 and an isolated PostgreSQL verification database")
	}
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Fatal("performance fixture requires QMS_TEST_POSTGRES_DSN for an isolated verification database")
	}
	previousLog, previous115Log := helpers.AppLogger, helpers.V115Log
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log = helpers.AppLogger
	// 本地 HTTP 测量使用相同宽松配额，避免将线上 3 QPS 等待当作数据库耗时。
	v115open.SetGlobalExecutorConfig(100, 1_000_000, 1_000_000)
	t.Cleanup(func() {
		v115open.SetGlobalExecutorConfig(3, 200, 12_000)
		helpers.AppLogger, helpers.V115Log = previousLog, previous115Log
	})
	t.Logf("environment go=%s os=%s arch=%s gomaxprocs=%d items=%d directories=%d cloud_qps=100 cloud_qpm=1000000 cloud_qph=1000000", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), embyPerformanceItems, embyPerformanceDirectories)
	for _, tc := range []struct {
		name          string
		incremental   bool
		deleteEnabled int
	}{
		{name: "full_delete_off"},
		{name: "incremental_delete_on", incremental: true, deleteEnabled: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, sqlCounter := setupEmbyPerformancePostgres(t)
			items, directories := seedEmbyPerformanceData(t, conn)
			counts := &embyPerformanceHTTPCounts{}
			server := newEmbyPerformanceServer(t, items, counts)
			cloudServer := newEmbyPerformanceCloudServer(t, directories, counts)
			cloudURL, err := url.Parse(cloudServer.URL)
			if err != nil {
				t.Fatal(err)
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			var account models.Account
			if err := conn.First(&account).Error; err != nil {
				t.Fatal(err)
			}
			account.Get115Client().SetTransport(embyPerformanceTransport{target: cloudURL, base: transport})
			config := models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: tc.deleteEnabled}
			if err := conn.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			for round := 1; round <= 2; round++ {
				embyBefore, cloudBefore := counts.embyTotal.Load(), counts.cloud.Load()
				pagesBefore, ancestorsBefore := counts.pages.Load(), counts.ancestors.Load()
				sqlCounter.statements.Store(0)
				sqlCounter.failures.Store(0)
				sqlCounter.elapsed.Store(0)
				sqlCounter.txCount.Store(0)
				sqlCounter.txElapsed.Store(0)
				run := PerformEmbySync
				if tc.incremental {
					run = PerformEmbyIncrementalSync
				}
				startedAt := time.Now()
				processed, err := run()
				elapsed := time.Since(startedAt)
				sqlCount, sqlFailures := sqlCounter.statements.Load(), sqlCounter.failures.Load()
				embyDelta, cloudDelta := counts.embyTotal.Load()-embyBefore, counts.cloud.Load()-cloudBefore
				pageDelta, ancestorDelta := counts.pages.Load()-pagesBefore, counts.ancestors.Load()-ancestorsBefore
				t.Logf("measurement case=%s round=%d items=%d directories=%d delete_enabled=%d page_size=%d emby_http=%d emby_pages=%d emby_ancestors=%d cloud_http=%d sql_statements=%d sql_errors=%d elapsed_ns=%d elapsed=%s", tc.name, round, processed, embyPerformanceDirectories, tc.deleteEnabled, counts.pageSize.Load(), embyDelta, pageDelta, ancestorDelta, cloudDelta, sqlCount, sqlFailures, elapsed.Nanoseconds(), elapsed)
				t.Logf("phases case=%s round=%d transactions=%d begin_to_commit_cumulative_ns=%d sql_cumulative_ns=%d cloud_details=%d cloud_lists=0 cloud_writes=0 cloud_retries=0", tc.name, round, sqlCounter.txCount.Load(), sqlCounter.txElapsed.Load(), sqlCounter.elapsed.Load(), cloudDelta)
				if err != nil || processed != embyPerformanceItems || sqlFailures != 0 {
					t.Fatalf("round %d: processed=%d SQL errors=%d err=%v", round, processed, sqlFailures, err)
				}
				if sqlCounter.txCount.Load() < embyPerformanceItems || sqlCounter.txElapsed.Load() <= 0 {
					t.Fatal("transaction timing did not cover snapshot commits")
				}
				wantCloud := int64(0)
				if cloudDelta != wantCloud {
					t.Fatalf("round %d: cloud HTTP=%d, want %d", round, cloudDelta, wantCloud)
				}
				if ancestorDelta != embyPerformanceDirectories {
					t.Fatalf("round %d: ancestors=%d, expected one shared observation per directory", round, ancestorDelta)
				}
				pageSize := counts.pageSize.Load()
				if pageSize <= 0 || pageDelta != (embyPerformanceItems+pageSize-1)/pageSize {
					t.Fatalf("round %d: pages=%d page size=%d", round, pageDelta, pageSize)
				}
				baseRequests := int64(3)
				if tc.incremental {
					baseRequests = 2
				}
				if embyDelta != baseRequests+pageDelta+ancestorDelta {
					t.Fatalf("round %d: unexpected Emby detail or other HTTP requests: %d", round, embyDelta)
				}
			}
			assertEmbyPerformanceEvidence(t, conn)
		})
	}
}

func setupEmbyPerformancePostgres(t *testing.T) (*gorm.DB, *embyPerformanceSQLCounter) {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	admin, err := gorm.Open(postgres.New(postgres.Config{DriverName: "postgres", DSN: dsn}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	schema := "qms_emby_perf_" + strings.ToLower(rand.Text())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	query.Set("statement_timeout", "30000")
	parsed.RawQuery = query.Encode()
	counter := &embyPerformanceSQLCounter{Interface: logger.Default.LogMode(logger.Silent)}
	conn, err := gorm.Open(postgres.New(postgres.Config{DriverName: "postgres", DSN: parsed.String()}), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	pool := embyPerformancePool{DB: sqlDB, counter: counter}
	conn.ConnPool, conn.Statement.ConnPool = pool, pool
	previousDB, previousConfig := db.Db, models.GlobalEmbyConfig
	db.Db, models.GlobalEmbyConfig = conn, nil
	SetEmbySyncRunning(false)
	t.Cleanup(func() {
		SetEmbySyncRunning(false)
		db.Db, models.GlobalEmbyConfig = previousDB, previousConfig
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	setupSnapshotTestTables(t)
	if err := conn.AutoMigrate(&models.Media{}, &models.ScrapePath{}, &models.SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	if err := models.EnsureSyncFileLookupIndexes(conn); err != nil {
		t.Fatal(err)
	}
	if err := models.EnsureEmbySyncFileLookupIndexes(conn); err != nil {
		t.Fatal(err)
	}
	if err := models.EnsureEmbyMediaLookupIndexes(conn); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"idx_sync_files_sibling_path", "idx_sync_files_file_name"} {
		if !conn.Migrator().HasIndex(&models.SyncFile{}, index) {
			t.Fatalf("performance fixture is missing production index %s", index)
		}
	}
	t.Logf("isolated_schema=%s postgres_connections=4 lookup_indexes=idx_sync_files_sibling_path,idx_sync_files_file_name", schema)
	return conn, counter
}

func seedEmbyPerformanceData(t *testing.T, conn *gorm.DB) ([]embyclientrestgo.BaseItemDtoV2, map[string]string) {
	t.Helper()
	account := models.Account{BaseModel: models.BaseModel{ID: uint(90_000_000 + cleanupSourceAccountID.Add(1))}, SourceType: models.SourceType115, UserId: "performance-fixture", Token: "fixture"}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root := models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "1", RemotePath: "/movies", LocalPath: t.TempDir()}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	seedEmbyPerformanceRootEvidence(t, conn, account, root)
	items := make([]embyclientrestgo.BaseItemDtoV2, 0, embyPerformanceItems)
	files := make([]models.SyncFile, 0, embyPerformanceItems+embyPerformanceDirectories)
	directories := make(map[string]string, embyPerformanceDirectories)
	for i := range embyPerformanceItems {
		folder := i / (embyPerformanceItems / embyPerformanceDirectories)
		id, parentID := strconv.Itoa(100_000+i), strconv.Itoa(200_000+folder)
		directory := "movie-" + strconv.Itoa(folder)
		localPath := filepath.Join(root.GetFullLocalPath(), directory, id+".strm")
		playback := "http://qms/stream?pickcode=pc-" + id
		if _, exists := directories[parentID]; !exists {
			if err := os.MkdirAll(filepath.Dir(localPath), 0700); err != nil {
				t.Fatal(err)
			}
			directories[parentID] = directory
			files = append(files, models.SyncFile{SourceType: root.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileId: parentID, ParentId: "1", FileName: directory, Path: "/movies", FileType: v115open.TypeDir, LocalFilePath: filepath.Dir(localPath)})
		}
		if err := os.WriteFile(localPath, []byte(playback), 0600); err != nil {
			t.Fatal(err)
		}
		files = append(files, models.SyncFile{SourceType: root.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileId: strconv.Itoa(1_000_000 + i), ParentId: parentID, FileName: id + ".mkv", Path: "/movies/" + directory, LocalFilePath: localPath, PickCode: "pc-" + id, Sha1: "hash-" + id, FileSize: 100, MTime: 123, IsVideo: true})
		items = append(items, embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Name: "movie " + id, Path: localPath, ParentId: parentID, PartCount: 1, MediaSources: []embyclientrestgo.MediaSource{{ID: "source-" + id, ItemID: id, Path: playback}}})
	}
	if err := conn.CreateInBatches(&files, 500).Error; err != nil {
		t.Fatal(err)
	}
	return items, directories
}

func seedEmbyPerformanceRootEvidence(t *testing.T, conn *gorm.DB, account models.Account, root models.SyncPath) {
	t.Helper()
	evidence := models.SyncRootEvidence{SyncPathID: root.ID, SourceType: root.SourceType, AccountID: account.ID, AccountIdentity: models.EmbyAccountIdentity(account), RootFileID: root.BaseCid, RootPath: "/movies", ObservedAt: 123, Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "1", ParentID: "0", Path: "/movies"}}}
	if err := conn.Transaction(func(tx *gorm.DB) error { return models.SaveSyncRootEvidenceTx(tx, evidence, root.BaseCid) }); err != nil {
		t.Fatal(err)
	}
}

func newEmbyPerformanceServer(t *testing.T, items []embyclientrestgo.BaseItemDtoV2, counts *embyPerformanceHTTPCounts) *httptest.Server {
	t.Helper()
	byID := make(map[string]embyclientrestgo.BaseItemDtoV2, len(items))
	for _, item := range items {
		byID[item.Id] = item
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts.embyTotal.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"performance-server"}`)
		case r.URL.Path == "/emby/Users":
			fmt.Fprint(w, `[{"Id":"user","Policy":{"EnableAllFolders":true}}]`)
		case r.URL.Path == "/emby/Library/MediaFolders":
			fmt.Fprint(w, `{"Items":[{"Id":"lib","Name":"Movies"}]}`)
		case r.URL.Path == "/emby/Items":
			start, startErr := strconv.Atoi(r.URL.Query().Get("StartIndex"))
			limit, limitErr := strconv.Atoi(r.URL.Query().Get("Limit"))
			if startErr != nil || limitErr != nil || start < 0 || start >= len(items) || limit <= 0 || r.URL.Query().Get("Ids") != "" {
				t.Errorf("unexpected Emby pagination request: %s", r.URL)
				http.Error(w, "invalid pagination", http.StatusBadRequest)
				return
			}
			counts.pageSize.CompareAndSwap(0, int64(limit))
			if counts.pageSize.Load() != int64(limit) {
				t.Errorf("Emby page size changed: %d", limit)
			}
			counts.pages.Add(1)
			if err := json.NewEncoder(w).Encode(map[string]any{"Items": items[start:min(start+limit, len(items))], "TotalRecordCount": len(items)}); err != nil {
				t.Error(err)
			}
		case strings.HasSuffix(r.URL.Path, "/Ancestors"):
			counts.ancestors.Add(1)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/emby/Items/"), "/Ancestors")
			item, ok := byID[id]
			if !ok {
				t.Errorf("unknown Emby item: %s", id)
				http.NotFound(w, r)
				return
			}
			if err := json.NewEncoder(w).Encode([]map[string]any{{"Id": item.ParentId, "Type": "Folder", "Path": filepath.Dir(item.Path), "IsFolder": true}}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected Emby HTTP request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newEmbyPerformanceCloudServer(t *testing.T, directories map[string]string, counts *embyPerformanceHTTPCounts) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts.cloud.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/open/folder/get_info" {
			t.Errorf("unexpected 115 HTTP request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		id := r.URL.Query().Get("file_id")
		name, ok := directories[id]
		if !ok {
			t.Errorf("unknown 115 directory: %s", id)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"state": true, "code": 0, "data": map[string]any{"file_id": id, "file_name": name, "file_category": "0", "paths": []map[string]string{{"file_id": "0", "file_name": ""}, {"file_id": "1", "file_name": "movies"}}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func assertEmbyPerformanceEvidence(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, table := range []string{"emby_media_items", "emby_item_states", "emby_media_sync_files"} {
		var count int64
		if err := conn.Table(table).Count(&count).Error; err != nil || count != embyPerformanceItems {
			t.Fatalf("%s: rows=%d err=%v", table, count, err)
		}
	}
	var evidence []models.EmbyItemEvidence
	if err := conn.Table("emby_item_evidences AS e").Select("e.*").Joins("JOIN emby_item_states AS s ON s.snapshot_id = e.id").Find(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if len(evidence) != embyPerformanceItems {
		t.Fatalf("current evidence rows=%d", len(evidence))
	}
	for _, row := range evidence {
		itemID, err := strconv.Atoi(row.ItemID)
		if err != nil || itemID < 100_000 || itemID >= 100_000+embyPerformanceItems {
			t.Fatalf("unexpected current item ID: %q", row.ItemID)
		}
		index := itemID - 100_000
		parentID := strconv.Itoa(200_000 + index/(embyPerformanceItems/embyPerformanceDirectories))
		metadata, err := models.DecodeEmbyMetadata(row.SidecarsJSON)
		if err != nil || len(metadata.DirectoryScopes) != 1 {
			t.Fatalf("item %s lost directory evidence: scopes=%d err=%v", row.ItemID, len(metadata.DirectoryScopes), err)
		}
		scope := metadata.DirectoryScopes[0]
		if scope.ItemID != row.ItemID || scope.Root.FileID != parentID {
			t.Fatalf("item %s inherited another item's directory: scope item=%s directory=%s", row.ItemID, scope.ItemID, scope.Root.FileID)
		}
		var files []models.EmbyFrozenFile
		if err := json.Unmarshal([]byte(row.FilesJSON), &files); err != nil || len(files) != 1 || files[0].Reason != "" {
			t.Fatalf("item %s lost file evidence: files=%+v err=%v", row.ItemID, files, err)
		}
		if files[0].FileID != strconv.Itoa(1_000_000+index) || files[0].ParentID != parentID || files[0].PickCode != "pc-"+row.ItemID {
			t.Fatalf("item %s inherited another file identity: %+v", row.ItemID, files[0])
		}
	}
}
