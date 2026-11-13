//go:build integration

package emby

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
	v115open "qmediasync/internal/v115/openapi"
)

// 小规模重复测量使用 QMS_PERF_ITEMS=2000，默认仍保留两万媒体／两千目录。
func embyPerformanceSize(t *testing.T) (items, directories int) {
	t.Helper()
	items = 20_000
	if value := os.Getenv("QMS_PERF_ITEMS"); value != "" {
		var err error
		items, err = strconv.Atoi(value)
		if err != nil || items < 10 || items%10 != 0 {
			t.Fatal("QMS_PERF_ITEMS must be a positive multiple of 10")
		}
	}
	switch os.Getenv("QMS_PERF_ID_KIND") {
	case "", "numeric", "guid":
	default:
		t.Fatal("QMS_PERF_ID_KIND must be numeric or guid")
	}
	return items, items / 10
}

// GUID 只改变媒体身份；散列分布避免顺序 UUID 低估索引页访问成本。
func embyPerformanceItemID(number int) string {
	if os.Getenv("QMS_PERF_ID_KIND") == "guid" {
		sum := sha256.Sum256([]byte("qms-jellyfin-item-" + strconv.Itoa(number)))
		sum[6] = sum[6]&0x0f | 0x40
		sum[8] = sum[8]&0x3f | 0x80
		return hex.EncodeToString(sum[:16])
	}
	return strconv.Itoa(number)
}

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
	runEmbySyncPerformance(t, setupEmbyPerformancePostgres)
}

// TestEmbySyncPerformanceSQLite 使用同一批媒体与断言，数据库位于独立临时目录。
func TestEmbySyncPerformanceSQLite(t *testing.T) {
	if os.Getenv("QMS_EMBY_PERFORMANCE") != "1" {
		t.Skip("requires QMS_EMBY_PERFORMANCE=1")
	}
	runEmbySyncPerformance(t, setupEmbyPerformanceSQLite)
}

func runEmbySyncPerformance(t *testing.T, setup func(*testing.T) (*gorm.DB, *embyPerformanceSQLCounter)) {
	t.Helper()
	embyPerformanceItems, embyPerformanceDirectories := embyPerformanceSize(t)
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
			conn, sqlCounter := setup(t)
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
			config := models.EmbyConfig{MediaServerType: models.MediaServerEmby, EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, Enabled: true, SyncAllLibraries: 1, EnableDeleteNetdisk: tc.deleteEnabled}
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
				incrementalRound := tc.incremental && round > 1
				if incrementalRound {
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
				if sqlCounter.txCount.Load() < int64((embyPerformanceItems+3)/4) || sqlCounter.txElapsed.Load() <= 0 {
					t.Fatal("transaction timing did not cover snapshot commits")
				}
				wantCloud := int64(0)
				if cloudDelta != wantCloud {
					t.Fatalf("round %d: cloud HTTP=%d, want %d", round, cloudDelta, wantCloud)
				}
				if ancestorDelta != int64(embyPerformanceDirectories) {
					t.Fatalf("round %d: ancestors=%d, expected one shared observation per directory", round, ancestorDelta)
				}
				pageSize := counts.pageSize.Load()
				if pageSize <= 0 || pageDelta != (int64(embyPerformanceItems)+pageSize-1)/pageSize {
					t.Fatalf("round %d: pages=%d page size=%d", round, pageDelta, pageSize)
				}
				baseRequests := int64(3)
				if incrementalRound {
					baseRequests = 2
				}
				if embyDelta != baseRequests+pageDelta+ancestorDelta {
					t.Fatalf("round %d: unexpected Emby detail or other HTTP requests: %d", round, embyDelta)
				}
				assertEmbyPerformanceEvidence(t, conn)
				var after models.EmbyConfig
				if err := conn.First(&after, config.ID).Error; err != nil || after.LastSavedCursorAt == 0 || after.LastSavedCursorAt < config.LastSavedCursorAt || after.LastError != "" {
					t.Fatalf("sync did not persist its successful cursor: %+v err=%v", after, err)
				}
				config = after
				logEmbyPerformanceDatabase(t, conn, round)
			}
		})
	}
}

func setupEmbyPerformancePostgres(t *testing.T) (*gorm.DB, *embyPerformanceSQLCounter) {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	admin, err := gorm.Open(postgres.New(postgres.Config{DriverName: "pgx", DSN: dsn}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	conn, err := gorm.Open(postgres.New(postgres.Config{DriverName: "pgx", DSN: parsed.String()}), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(25)
	setupEmbyPerformanceDatabase(t, conn, sqlDB, counter)
	t.Logf("isolated_schema=%s postgres_connections=25 lookup_indexes=idx_sync_files_sibling_path,idx_sync_files_file_name", schema)
	return conn, counter
}

func setupEmbyPerformanceSQLite(t *testing.T) (*gorm.DB, *embyPerformanceSQLCounter) {
	t.Helper()
	counter := &embyPerformanceSQLCounter{Interface: logger.Default.LogMode(logger.Silent)}
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "emby.sqlite")), &gorm.Config{
		Logger: counter, SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	setupEmbyPerformanceDatabase(t, conn, sqlDB, counter)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=10000"} {
		if err := conn.Exec(pragma).Error; err != nil {
			t.Fatal(err)
		}
	}
	return conn, counter
}

func setupEmbyPerformanceDatabase(t *testing.T, conn *gorm.DB, sqlDB *sql.DB, counter *embyPerformanceSQLCounter) {
	t.Helper()
	pool := embyPerformancePool{DB: sqlDB, counter: counter}
	conn.ConnPool, conn.Statement.ConnPool = pool, pool
	previousDB := db.Db
	db.Db = conn
	SetEmbySyncRunning(false)
	t.Cleanup(func() {
		SetEmbySyncRunning(false)
		db.Db = previousDB
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
}

func seedEmbyPerformanceData(t *testing.T, conn *gorm.DB) ([]embyclientrestgo.BaseItemDtoV2, map[string]string) {
	t.Helper()
	embyPerformanceItems, embyPerformanceDirectories := embyPerformanceSize(t)
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
		numericID, parentID := strconv.Itoa(100_000+i), strconv.Itoa(200_000+folder)
		id := embyPerformanceItemID(100_000 + i)
		directory := "movie-" + strconv.Itoa(folder)
		localPath := filepath.Join(root.GetFullLocalPath(), directory, numericID+".strm")
		playback := "http://qms/stream?pickcode=pc-" + numericID
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
		files = append(files, models.SyncFile{SourceType: root.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileId: strconv.Itoa(1_000_000 + i), ParentId: parentID, FileName: numericID + ".mkv", Path: "/movies/" + directory, LocalFilePath: localPath, PickCode: "pc-" + numericID, Sha1: "hash-" + numericID, FileSize: 100, MTime: 123, IsVideo: true})
		items = append(items, embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Name: "movie " + numericID, Path: localPath, ParentId: parentID, PartCount: 1, MediaSources: []embyclientrestgo.MediaSource{{ID: "source-" + numericID, ItemID: id, Path: playback}}})
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
		case r.URL.Path == "/System/Info/Public" || r.URL.Path == "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"performance-server","ProductName":"Emby","Version":"4.8.0"}`)
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
	embyPerformanceItems, embyPerformanceDirectories := embyPerformanceSize(t)
	numberByID := make(map[string]int, embyPerformanceItems)
	for i := range embyPerformanceItems {
		numberByID[embyPerformanceItemID(100_000+i)] = 100_000 + i
	}
	var fileCount int64
	if err := conn.Model(&models.SyncFile{}).Count(&fileCount).Error; err != nil || fileCount != int64(embyPerformanceItems+embyPerformanceDirectories) {
		t.Fatalf("sync files changed: rows=%d err=%v", fileCount, err)
	}
	for _, table := range []string{"emby_media_items", "emby_item_states", "emby_media_sync_files", "emby_item_evidences", "emby_item_memberships"} {
		var count int64
		if err := conn.Table(table).Count(&count).Error; err != nil || count != int64(embyPerformanceItems) {
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
	var memberships []models.EmbyItemMembership
	if err := conn.Find(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	membershipByItem := make(map[string]models.EmbyItemMembership, len(memberships))
	for _, member := range memberships {
		membershipByItem[member.ItemID] = member
	}
	for _, row := range evidence {
		var persistedItem models.EmbyMediaItem
		if err := json.Unmarshal([]byte(row.ItemJSON), &persistedItem); err != nil {
			t.Fatal(err)
		}
		member, ok := membershipByItem[row.ItemID]
		if !ok || member.ServerID != row.ServerID || member.ServerConfigKey != row.ServerConfigKey ||
			member.SnapshotID != row.ID || member.ItemType != persistedItem.Type ||
			member.PartOfItemID != persistedItem.PartOfItemID || member.SeasonID != persistedItem.SeasonId ||
			member.SeriesID != persistedItem.SeriesId {
			t.Fatalf("membership differs from persisted evidence for item %s", row.ItemID)
		}
		itemID, exists := numberByID[row.ItemID]
		if !exists {
			t.Fatalf("unexpected current item ID: %q", row.ItemID)
		}
		index := itemID - 100_000
		parentID := strconv.Itoa(200_000 + index/(embyPerformanceItems/embyPerformanceDirectories))
		metadata, err := models.DecodeEmbyMetadata(row.SidecarsJSON)
		if err != nil {
			t.Fatalf("item %s lost directory evidence: scopes=%d err=%v", row.ItemID, len(metadata.DirectoryScopes), err)
		}
		if os.Getenv("QMS_PERF_ID_KIND") == "guid" {
			// GUID 索引不产生原 Emby 数字条目的目录删除权限。
			if len(metadata.DirectoryScopes) != 0 {
				t.Fatalf("GUID item %s gained directory deletion authority", row.ItemID)
			}
		} else {
			if len(metadata.DirectoryScopes) != 1 {
				t.Fatalf("item %s lost its directory evidence: scopes=%d", row.ItemID, len(metadata.DirectoryScopes))
			}
			scope := metadata.DirectoryScopes[0]
			if scope.ItemID != row.ItemID || scope.Root.FileID != parentID {
				t.Fatalf("item %s inherited another item's directory: scope item=%s directory=%s", row.ItemID, scope.ItemID, scope.Root.FileID)
			}
		}
		var files []models.EmbyFrozenFile
		if err := json.Unmarshal([]byte(row.FilesJSON), &files); err != nil || len(files) != 1 || files[0].Reason != "" {
			t.Fatalf("item %s lost file evidence: files=%+v err=%v", row.ItemID, files, err)
		}
		if files[0].FileID != strconv.Itoa(1_000_000+index) || files[0].ParentID != parentID || files[0].PickCode != "pc-"+strconv.Itoa(itemID) {
			t.Fatalf("item %s inherited another file identity: %+v", row.ItemID, files[0])
		}
	}
}

func TestEmbyIndexPerformanceSQLite(t *testing.T) {
	runEmbyIndexPerformance(t, setupEmbyPerformanceSQLite)
}

func TestEmbyIndexPerformancePostgres(t *testing.T) {
	if os.Getenv("QMS_EMBY_PERFORMANCE") == "1" && os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Fatal("performance fixture requires an isolated QMS_TEST_POSTGRES_DSN")
	}
	runEmbyIndexPerformance(t, setupEmbyPerformancePostgres)
}

// 每次进程只测一份独立夹具，重复次数由外层串行交错运行控制。
func runEmbyIndexPerformance(t *testing.T, setup func(*testing.T) (*gorm.DB, *embyPerformanceSQLCounter)) {
	t.Helper()
	if os.Getenv("QMS_EMBY_PERFORMANCE") != "1" {
		t.Skip("requires QMS_EMBY_PERFORMANCE=1")
	}
	itemCount, _ := embyPerformanceSize(t)
	previousLog := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLog })
	conn, counter := setup(t)
	config := models.EmbyConfig{MediaServerType: models.MediaServerEmby, EmbyUrl: "http://cleanup-fixture", EmbyApiKey: "fixture", SyncEnabled: 1, Enabled: true, SyncAllLibraries: 1}
	if err := conn.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	items := make([]models.EmbyMediaItem, 0, itemCount+2)
	links := make([]models.EmbyMediaSyncFile, 0, itemCount+2)
	files := make([]models.SyncFile, 0, itemCount+2)
	evidence := make([]models.EmbyItemEvidence, 0, itemCount+2)
	states := make([]models.EmbyItemState, 0, itemCount+2)
	members := make([]models.EmbyItemMembership, 0, itemCount+2)
	for i := range itemCount + 2 {
		number, rowID := 100_000+i, uint(i+1)
		itemID, library, run := embyPerformanceItemID(number), "cleanup-lib", "previous-run"
		if i == itemCount {
			run = "current-run"
		} else if i == itemCount+1 {
			library = "other-lib"
		}
		legacyID := int64(number)
		if os.Getenv("QMS_PERF_ID_KIND") == "guid" {
			legacyID = 0
		}
		items = append(items, models.EmbyMediaItem{MediaServerType: models.MediaServerEmby, BaseModel: models.BaseModel{ID: rowID}, ItemId: itemID, ItemIdInt: legacyID, ServerId: "cleanup-server", LibraryId: library, LastSeenSyncRun: run, SnapshotID: rowID})
		links = append(links, models.EmbyMediaSyncFile{BaseModel: models.BaseModel{ID: rowID}, EmbyItemId: itemID, SyncFileId: rowID, SyncPathId: 1, SnapshotID: rowID, SourceID: "source-" + strconv.Itoa(number)})
		files = append(files, models.SyncFile{BaseModel: models.BaseModel{ID: rowID}, SyncPathId: 1, FileId: strconv.Itoa(1_000_000 + i), FileName: strconv.Itoa(number) + ".mkv", Path: "/movies", IsVideo: true})
		evidence = append(evidence, models.EmbyItemEvidence{BaseModel: models.BaseModel{ID: rowID}, ServerID: "cleanup-server", ItemID: itemID, Generation: 1, ItemJSON: fmt.Sprintf(`{"item_id":%q}`, itemID), SourcesJSON: "[]", FilesJSON: "[]", SidecarsJSON: `{"sentinel":true}`})
		states = append(states, models.EmbyItemState{BaseModel: models.BaseModel{ID: rowID}, ServerID: "cleanup-server", ItemID: itemID, Generation: 1, SnapshotID: rowID})
		members = append(members, models.EmbyItemMembership{BaseModel: models.BaseModel{ID: rowID}, ServerID: "cleanup-server", ItemID: itemID, SnapshotID: rowID})
	}
	for _, rows := range []any{&items, &links, &files, &evidence, &states, &members} {
		if err := conn.CreateInBatches(rows, 500).Error; err != nil {
			t.Fatal(err)
		}
	}
	token, err := models.BeginEmbyIndexRead("cleanup-server", &config)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("reads", func(t *testing.T) {
		for _, query := range []struct {
			name    string
			offsets []int
			loops   int
		}{
			{name: "single", offsets: []int{itemCount / 2}, loops: 500},
			{name: "batch4", offsets: []int{0, itemCount / 4, itemCount / 2, itemCount - 1}, loops: 200},
		} {
			keys := make([]string, len(query.offsets))
			expected := make([]uint, len(query.offsets))
			for i, offset := range query.offsets {
				keys[i], expected[i] = embyPerformanceItemID(100_000+offset), uint(offset+1)
			}
			counter.statements.Store(0)
			counter.failures.Store(0)
			var found []models.EmbyMediaSyncFile
			started := time.Now()
			for range query.loops {
				// 与生产关联预加载相同的过滤和顺序；每次重新分配结果切片。
				found = nil
				read := conn.Where("emby_item_id IN ?", keys)
				if len(keys) == 1 {
					read = conn.Where("emby_item_id = ?", keys[0])
				}
				if err := read.Order("id").Find(&found).Error; err != nil {
					t.Fatal(err)
				}
			}
			elapsed := time.Since(started)
			t.Logf("read_measurement engine=%s id_kind=%s query=%s items=%d loops=%d sql=%d sql_errors=%d elapsed_ns=%d", conn.Dialector.Name(), os.Getenv("QMS_PERF_ID_KIND"), query.name, itemCount, query.loops, counter.statements.Load(), counter.failures.Load(), elapsed.Nanoseconds())
			actual := make([]uint, len(found))
			for i, row := range found {
				actual[i] = row.SyncFileId
			}
			if !slices.Equal(actual, expected) || counter.failures.Load() != 0 {
				t.Fatalf("wrong %s association result: %v, expected %v", query.name, actual, expected)
			}
			read := conn.Session(&gorm.Session{DryRun: true}).Where("emby_item_id IN ?", keys)
			if len(keys) == 1 {
				read = conn.Session(&gorm.Session{DryRun: true}).Where("emby_item_id = ?", keys[0])
			}
			statement := read.Order("id").Find(&[]models.EmbyMediaSyncFile{}).Statement
			logEmbyPerformanceReadPlan(t, conn, query.name, statement)
		}
	})
	logEmbyPerformanceDatabase(t, conn, 1)
	t.Run("cleanup", func(t *testing.T) {
		counter.statements.Store(0)
		counter.failures.Store(0)
		started := time.Now()
		err := models.CleanupEmbyLibrarySnapshot(token, "cleanup-lib", "current-run")
		elapsed := time.Since(started)
		t.Logf("cleanup_measurement engine=%s id_kind=%s items=%d sql=%d sql_errors=%d elapsed_ns=%d", conn.Dialector.Name(), os.Getenv("QMS_PERF_ID_KIND"), itemCount, counter.statements.Load(), counter.failures.Load(), elapsed.Nanoseconds())
		if err != nil || counter.failures.Load() != 0 {
			t.Fatalf("cleanup failed: %v SQL errors=%d", err, counter.failures.Load())
		}
		var remainingItems []models.EmbyMediaItem
		var remainingLinks []models.EmbyMediaSyncFile
		if err := conn.Order("id").Find(&remainingItems).Error; err != nil {
			t.Fatal(err)
		}
		if err := conn.Order("id").Find(&remainingLinks).Error; err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(remainingItems, items[itemCount:]) || !slices.Equal(remainingLinks, links[itemCount:]) {
			t.Fatalf("cleanup changed its frozen target: media=%d links=%d", len(remainingItems), len(remainingLinks))
		}
		var afterFiles []models.SyncFile
		var afterEvidence []models.EmbyItemEvidence
		var afterStates []models.EmbyItemState
		var afterMembers []models.EmbyItemMembership
		for _, rows := range []any{&afterFiles, &afterEvidence, &afterStates, &afterMembers} {
			if err := conn.Order("id").Find(rows).Error; err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(afterFiles, files) || !slices.Equal(afterEvidence, evidence) || !slices.Equal(afterStates, states) || !slices.Equal(afterMembers, members) {
			t.Fatal("local cleanup changed sync files, immutable evidence, states or memberships")
		}
	})
}

// 查询计划在业务计时后检查，避免用准备期 ANALYZE 掩盖索引退化。
func logEmbyPerformanceReadPlan(t *testing.T, conn *gorm.DB, query string, statement *gorm.Statement) {
	t.Helper()
	var plan string
	if conn.Dialector.Name() == "sqlite" {
		var rows []struct{ Detail string }
		if err := conn.Raw("EXPLAIN QUERY PLAN "+statement.SQL.String(), statement.Vars...).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			plan += row.Detail + "\n"
		}
		if !strings.Contains(plan, "USING INDEX") && !strings.Contains(plan, "USING COVERING INDEX") {
			t.Fatalf("association query does not use an index: %s", plan)
		}
	} else {
		var rows []string
		if err := conn.Raw("EXPLAIN "+statement.SQL.String(), statement.Vars...).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		plan = strings.Join(rows, "\n")
		if !strings.Contains(plan, "Index") {
			t.Fatalf("association query does not use an index: %s", plan)
		}
	}
	t.Logf("read_plan engine=%s query=%s plan=%s", conn.Dialector.Name(), query, plan)
}

func logEmbyPerformanceDatabase(t *testing.T, conn *gorm.DB, round int) {
	t.Helper()
	if conn.Dialector.Name() == "sqlite" {
		var pages, pageSize int64
		if err := conn.Raw("PRAGMA page_count").Scan(&pages).Error; err != nil {
			t.Fatal(err)
		}
		if err := conn.Raw("PRAGMA page_size").Scan(&pageSize).Error; err != nil {
			t.Fatal(err)
		}
		t.Logf("database round=%d engine=sqlite allocated_bytes=%d pages=%d page_size=%d", round, pages*pageSize, pages, pageSize)
		return
	}
	var sizes []struct {
		TableName                         string
		HeapBytes, IndexBytes, TotalBytes int64
	}
	if err := conn.Raw(`SELECT c.relname AS table_name, pg_relation_size(c.oid) AS heap_bytes, pg_indexes_size(c.oid) AS index_bytes, pg_total_relation_size(c.oid) AS total_bytes FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname IN ('emby_media_items','emby_media_sync_files') ORDER BY c.relname`).Scan(&sizes).Error; err != nil {
		t.Fatal(err)
	}
	for _, size := range sizes {
		t.Logf("database round=%d engine=postgres table=%s heap_bytes=%d index_bytes=%d total_bytes=%d", round, size.TableName, size.HeapBytes, size.IndexBytes, size.TotalBytes)
	}
}
