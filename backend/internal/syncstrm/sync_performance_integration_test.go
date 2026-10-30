//go:build integration

package syncstrm

import (
	"context"
	"crypto/rand"
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
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/realtime"
	v115open "qmediasync/internal/v115/openapi"
)

const (
	strmPerformanceVideos      = 20_000
	strmPerformanceDirectories = 2_000
	strmPerformancePageSize    = 1_000
	strmPerformanceRootPath    = "Library/Movies"
)

type strmPerformanceSQLCounter struct {
	logger.Interface
	statements atomic.Int64
	failures   atomic.Int64
	elapsed    atomic.Int64
}

func (counter *strmPerformanceSQLCounter) Trace(_ context.Context, started time.Time, _ func() (string, int64), err error) {
	counter.statements.Add(1)
	counter.elapsed.Add(time.Since(started).Nanoseconds())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		counter.failures.Add(1)
	}
}

type strmPerformanceHTTPCounts struct {
	details        atomic.Int64
	lists          atomic.Int64
	writes         atomic.Int64
	counts         atomic.Int64
	directoryPages atomic.Int64
	filePages      atomic.Int64
}

// 只改测试账号请求的目的地址，响应由本地 HTTP 服务按真实分页参数返回。
type strmPerformanceTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport strmPerformanceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "proapi.115.com" {
		return nil, fmt.Errorf("unexpected STRM performance host: %s", request.URL.Host)
	}
	forward := request.Clone(request.Context())
	forward.URL.Scheme, forward.URL.Host = transport.target.Scheme, transport.target.Host
	forward.Host = transport.target.Host
	return transport.base.RoundTrip(forward)
}

// TestSTRMSyncPerformancePostgres 只测正常扫描、STRM 生成及真实后台账本，不预填 SyncFile。
// 两版必须独占进程串行测量；本地 HTTP 配额和磁盘条件不代表生产网盘耗时。
func TestSTRMSyncPerformancePostgres(t *testing.T) {
	if os.Getenv("QMS_STRM_PERFORMANCE") != "1" {
		t.Skip("requires QMS_STRM_PERFORMANCE=1 and QMS_TEST_POSTGRES_DSN")
	}
	previousSettings, previousConfig := models.SettingsGlobal, models.GlobalEmbyConfig
	previousLog, previous115Log, previousConfigDir := helpers.AppLogger, helpers.V115Log, helpers.ConfigDir
	previousNotifications := notificationmanager.GlobalEnhancedNotificationManager
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log, helpers.ConfigDir = helpers.AppLogger, t.TempDir()
	models.GlobalEmbyConfig, notificationmanager.GlobalEnhancedNotificationManager = nil, nil
	models.SettingsGlobal = &models.Settings{
		SettingThreads: models.SettingThreads{FileDetailThreads: 2, FileListPageSize: strmPerformancePageSize},
		SettingStrm:    models.SettingStrm{StrmBaseUrl: "http://qms.fixture", VideoExt: `[".mkv"]`, MetaExt: `[]`, Cron: "0 0 * * *"},
	}
	v115open.SetGlobalExecutorConfig(100, 1_000_000, 1_000_000)
	t.Cleanup(func() {
		v115open.SetGlobalExecutorConfig(3, 200, 12_000)
		models.SettingsGlobal, models.GlobalEmbyConfig = previousSettings, previousConfig
		helpers.AppLogger, helpers.V115Log, helpers.ConfigDir = previousLog, previous115Log, previousConfigDir
		notificationmanager.GlobalEnhancedNotificationManager = previousNotifications
	})
	counter, reopen := setupSTRMPerformancePostgres(t)
	account := models.Account{BaseModel: models.BaseModel{ID: uint(time.Now().UnixNano())}, SourceType: models.SourceType115, UserId: "strm-performance", Token: "fixture"}
	root := models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "10", RemotePath: strmPerformanceRootPath, LocalPath: t.TempDir()}
	for _, row := range []any{models.SettingsGlobal, &account, &root} {
		if err := db.Db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var initialRows int64
	if err := db.Db.Model(&models.SyncFile{}).Count(&initialRows).Error; err != nil || initialRows != 0 {
		t.Fatalf("performance scan must start with an empty ledger: rows=%d err=%v", initialRows, err)
	}
	counts := &strmPerformanceHTTPCounts{}
	files, directories := strmPerformanceRemoteFiles()
	server := newSTRMPerformanceServer(t, files, directories, counts)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	account.Get115Client().SetTransport(strmPerformanceTransport{target: target, base: transport})
	t.Logf("environment go=%s os=%s arch=%s gomaxprocs=%d videos=%d directories=%d page_size=%d cloud_qps=100 cloud_qpm=1000000 cloud_qph=1000000", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), len(files), len(directories), strmPerformancePageSize)
	var startedAt, finishedAt time.Time
	for _, phase := range []struct {
		name    string
		full    bool
		newSTRM int
	}{{"full_first", true, strmPerformanceVideos}, {"full_repeat", true, 0}, {"incremental_unchanged", false, 0}} {
		if !t.Run(phase.name, func(t *testing.T) {
			if err := db.Db.Model(&root).Update("is_full_sync", phase.full).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.First(&root, root.ID).Error; err != nil {
				t.Fatal(err)
			}
			startedAt, finishedAt = runSTRMPerformanceScan(t, &root, phase.newSTRM, counter, counts)
			assertSTRMPerformanceLedger(t, account, root, files, directories)
			assertSTRMPerformanceRootEvidence(t, account, root, startedAt, finishedAt)
		}) {
			return
		}
	}
	// 重开不计入测量，确认根链来自数据库而非本轮同步器缓存。
	reopen()
	assertSTRMPerformanceRootEvidence(t, account, root, startedAt, finishedAt)
}

func setupSTRMPerformancePostgres(t *testing.T) (*strmPerformanceSQLCounter, func()) {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil || parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("QMS_TEST_POSTGRES_DSN must explicitly name an isolated PostgreSQL test database using a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	schema := "qms_strm_perf_" + strings.ToLower(rand.Text())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	query.Set("statement_timeout", "30000")
	parsed.RawQuery = query.Encode()
	counter := &strmPerformanceSQLCounter{Interface: logger.Default.LogMode(logger.Silent)}
	open := func() *gorm.DB {
		conn, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: counter})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetMaxIdleConns(4)
		return conn
	}
	previousDB := db.Db
	db.Db = open()
	t.Cleanup(func() {
		sqlDB, err := db.Db.DB()
		db.Db = previousDB
		if err != nil {
			t.Error(err)
		} else if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.Db.AutoMigrate(&models.Account{}, &models.SyncPath{}, &models.Sync{}, &models.SyncFile{},
		&models.DbDownloadTask{}, &models.DbUploadTask{}, &models.Settings{}, &models.EmbyConfig{},
		&models.EmbyLibrary{}, &models.EmbyLibrarySyncPath{}, &models.EmbyLibraryRefreshTask{},
		&models.Media{}, &models.ScrapePath{}, &models.SyncPathScrapePath{}); err != nil {
		t.Fatal(err)
	}
	if err := models.MigrateEmbyDeletionSchema(db.Db); err != nil {
		t.Fatal(err)
	}
	setupSTRMPerformanceRootEvidence(t)
	if err := models.EnsureSyncFileLookupIndexes(db.Db); err != nil {
		t.Fatal(err)
	}
	if err := models.EnsureEmbySyncFileLookupIndexes(db.Db); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"idx_sync_files_sibling_path", "idx_sync_files_file_name"} {
		if !db.Db.Migrator().HasIndex(&models.SyncFile{}, index) {
			t.Fatalf("performance fixture is missing production index %s", index)
		}
	}
	t.Logf("isolated_schema=%s postgres_connections=4 lookup_indexes=idx_sync_files_sibling_path,idx_sync_files_file_name", schema)
	return counter, func() {
		sqlDB, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		db.Db = open()
	}
}

func strmPerformanceRemoteFiles() ([]v115open.File, []v115open.File) {
	files := make([]v115open.File, strmPerformanceVideos)
	directories := make([]v115open.File, strmPerformanceDirectories)
	for i := range directories {
		directories[i] = v115open.File{FileId: strconv.Itoa(200_000 + i), Pid: "10", Aid: "1", FileCategory: v115open.TypeDir, FileName: fmt.Sprintf("movie-%04d", i), Utime: 1_700_000_000}
	}
	for i := range files {
		id := strconv.Itoa(1_000_000 + i)
		files[i] = v115open.File{FileId: id, Pid: directories[i/(strmPerformanceVideos/strmPerformanceDirectories)].FileId, Aid: "1", FileCategory: v115open.TypeFile, FileName: fmt.Sprintf("video-%05d.mkv", i), PickCode: "pc-" + id, Sha1: "sha1-" + id, FileSize: 1 << 30, Utime: 1_700_000_000}
	}
	return files, directories
}

func newSTRMPerformanceServer(t *testing.T, files, directories []v115open.File, counts *strmPerformanceHTTPCounts) *httptest.Server {
	t.Helper()
	byID := map[string]v115open.File{"10": {FileId: "10", Pid: "5", FileName: "Movies", FileCategory: v115open.TypeDir}}
	for _, entries := range [][]v115open.File{files, directories} {
		for _, file := range entries {
			byID[file.FileId] = file
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch r.URL.Path {
		case "/open/folder/get_info":
			// 路径定位使用 POST，但它是详情读取，不能计为云端写操作。
			counts.details.Add(1)
			id := r.URL.Query().Get("file_id")
			if r.Method == http.MethodPost {
				if err := r.ParseForm(); err != nil || r.Form.Get("path") != "/"+strmPerformanceRootPath {
					t.Errorf("unexpected STRM root lookup: %v", err)
					http.Error(w, "unexpected root", http.StatusBadRequest)
					return
				}
				id = "10"
			} else if r.Method != http.MethodGet {
				t.Errorf("unexpected detail method: %s", r.Method)
			}
			file, found := byID[id]
			if !found {
				t.Errorf("unexpected detail ID: %s", id)
				http.NotFound(w, r)
				return
			}
			ancestors := []v115open.FileDetailPath{{FileId: "0"}, {FileId: "5", Name: "Library"}}
			if id != "10" {
				ancestors = append(ancestors, v115open.FileDetailPath{FileId: "10", Name: "Movies"})
			}
			if file.FileCategory == v115open.TypeFile {
				ancestors = append(ancestors, v115open.FileDetailPath{FileId: file.Pid, Name: byID[file.Pid].FileName})
			}
			response = map[string]any{"state": true, "code": 0, "data": v115open.FileDetail{FileId: id, FileName: file.FileName, FileCategory: file.FileCategory, Paths: ancestors, PickCode: file.PickCode, Sha1: file.Sha1, FileSizeByte: file.FileSize, Utime: strconv.FormatInt(file.Utime, 10)}}
		case "/open/ufile/files":
			counts.lists.Add(1)
			query := r.URL.Query()
			offset, limit := 0, 0
			var err error
			if query.Get("offset") != "" {
				offset, err = strconv.Atoi(query.Get("offset"))
			}
			limit, limitErr := strconv.Atoi(query.Get("limit"))
			entries := files
			if query.Get("cur") == "1" && query.Get("stdir") == "1" && query.Get("show_dir") == "1" {
				entries = directories
				counts.directoryPages.Add(1)
			} else if query.Get("cur") == "" && query.Get("stdir") == "" && query.Get("show_dir") == "" {
				if limit == 1 && offset == 0 {
					counts.counts.Add(1)
				} else {
					counts.filePages.Add(1)
				}
			} else {
				t.Errorf("unexpected STRM listing flags")
			}
			if err != nil || limitErr != nil || r.Method != http.MethodGet || query.Get("cid") != "10" || offset < 0 || offset >= len(entries) || (limit != 1 && limit != strmPerformancePageSize) {
				t.Errorf("invalid STRM pagination: offset=%d limit=%d", offset, limit)
				http.Error(w, "invalid pagination", http.StatusBadRequest)
				return
			}
			response = map[string]any{"state": true, "code": 0, "count": len(entries), "offset": offset, "limit": limit, "data": entries[offset:min(offset+limit, len(entries))], "path": []v115open.FileParentPath{{FileId: "0"}, {FileId: "5", Name: "Library"}, {FileId: "10", Name: "Movies"}}}
		default:
			counts.writes.Add(1)
			t.Errorf("unexpected cloud operation: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected cloud operation", http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func runSTRMPerformanceScan(t *testing.T, root *models.SyncPath, wantNewSTRM int, counter *strmPerformanceSQLCounter, counts *strmPerformanceHTTPCounts) (time.Time, time.Time) {
	t.Helper()
	previousDownload, previousUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
	download, upload := models.NewDq(0), models.NewUq(1)
	models.GlobalDownloadQueue, models.GlobalUploadQueue = download, upload
	t.Cleanup(func() {
		download.Stop()
		upload.Stop()
		models.GlobalDownloadQueue, models.GlobalUploadQueue = previousDownload, previousUpload
	})
	before := []int64{counts.details.Load(), counts.lists.Load(), counts.writes.Load(), counts.counts.Load(), counts.directoryPages.Load(), counts.filePages.Load()}
	counter.statements.Store(0)
	counter.failures.Store(0)
	counter.elapsed.Store(0)
	startedAt := time.Now()
	syncer := NewSyncStrmFromSyncPath(root)
	if syncer == nil {
		t.Fatal("failed to create normal STRM scanner")
	}
	syncer.Sync.Logger.Close()
	syncer.Sync.Logger, syncer.ownsLogger = helpers.AppLogger, false
	service := newSyncBackgroundService()
	syncer.backgroundService = service
	t.Cleanup(func() {
		syncer.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.stop(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	stopWatch := context.AfterFunc(ctx, func() {
		syncer.Cancel()
		service.cancel()
	})
	defer stopWatch()
	err := syncer.Start()
	generationElapsed := time.Since(startedAt)
	// Start 已按正常流程恢复队列；夹具无传输任务，立即停止空闲轮询以隔离账本 SQL。
	download.Stop()
	upload.Stop()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		service.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("STRM background ledger did not finish within its measurement budget")
	}
	finishedAt := time.Now()
	sqlStatements, sqlErrors, sqlElapsed := counter.statements.Load(), counter.failures.Load(), counter.elapsed.Load()
	details, lists, writes := counts.details.Load()-before[0], counts.lists.Load()-before[1], counts.writes.Load()-before[2]
	countLists, directoryPages, filePages := counts.counts.Load()-before[3], counts.directoryPages.Load()-before[4], counts.filePages.Load()-before[5]
	elapsed := finishedAt.Sub(startedAt)
	t.Logf("measurement case=%s full=%t videos=%d directories=%d cloud_details=%d cloud_lists=%d cloud_writes=%d count_lists=%d directory_pages=%d file_pages=%d sql_statements=%d sql_errors=%d sql_cumulative_ns=%d generation_ns=%d ledger_tail_ns=%d elapsed_ns=%d elapsed=%s", t.Name(), root.IsFullSync, strmPerformanceVideos, strmPerformanceDirectories, details, lists, writes, countLists, directoryPages, filePages, sqlStatements, sqlErrors, sqlElapsed, generationElapsed.Nanoseconds(), (elapsed - generationElapsed).Nanoseconds(), elapsed.Nanoseconds(), elapsed)
	wantDetails, wantDirectoryPages := int64(1), int64(0)
	if root.IsFullSync {
		wantDetails, wantDirectoryPages = 2, (strmPerformanceDirectories+strmPerformancePageSize-1)/strmPerformancePageSize
	}
	wantFilePages := int64((strmPerformanceVideos + strmPerformancePageSize - 1) / strmPerformancePageSize)
	if sqlErrors != 0 || details != wantDetails || directoryPages != wantDirectoryPages || filePages != wantFilePages || countLists != 1 || lists != 1+wantDirectoryPages+wantFilePages || writes != 0 {
		t.Fatal("scan added SQL failures, duplicate pages or cloud precollection requests; see measurement")
	}
	var record models.Sync
	if err := db.Db.First(&record, syncer.Sync.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != models.SyncStatusCompleted || record.LedgerStatus == nil || *record.LedgerStatus != realtime.SyncLedgerCompleted || record.LedgerFinishedAt == nil || record.LedgerError != "" || record.NewStrm != wantNewSTRM || record.ScanResult == nil || record.ScanResult.SucceededFiles != strmPerformanceVideos || record.ScanResult.FailedFiles != 0 || len(record.ScanResult.Failures) != 0 {
		t.Fatalf("normal STRM generation/background result is incomplete: %+v", record)
	}
	return startedAt, finishedAt
}

func assertSTRMPerformanceLedger(t *testing.T, account models.Account, root models.SyncPath, files, directories []v115open.File) {
	t.Helper()
	expected := make(map[string]v115open.File, len(files)+len(directories))
	for _, entries := range [][]v115open.File{files, directories} {
		for _, file := range entries {
			expected[file.FileId] = file
		}
	}
	var rows []models.SyncFile
	if err := db.Db.Where("sync_path_id = ?", root.ID).Find(&rows).Error; err != nil || len(rows) != len(expected) {
		t.Fatalf("STRM ledger rows=%d want=%d err=%v", len(rows), len(expected), err)
	}
	for _, row := range rows {
		file, found := expected[row.FileId]
		if !found {
			t.Fatalf("unexpected or duplicate ledger ID: %s", row.FileId)
		}
		parent := strmPerformanceRootPath
		if file.FileCategory == v115open.TypeFile {
			index, err := strconv.Atoi(file.Pid)
			if err != nil {
				t.Fatal(err)
			}
			parent = filepath.Join(parent, directories[index-200_000].FileName)
		}
		local := filepath.Join(root.LocalPath, parent, file.FileName)
		if file.FileCategory == v115open.TypeFile {
			local = strings.TrimSuffix(local, ".mkv") + ".strm"
		}
		if row.SourceType != models.SourceType115 || row.AccountId != account.ID || row.SyncPathId != root.ID || row.ParentId != file.Pid || row.FileName != file.FileName || row.FileType != file.FileCategory || strings.TrimPrefix(row.Path, "/") != filepath.ToSlash(parent) || row.LocalFilePath != local || row.MTime != file.ModifiedAt() || row.IsVideo != (file.FileCategory == v115open.TypeFile) || row.PickCode != file.PickCode || row.Sha1 != file.Sha1 || row.FileSize != file.FileSize {
			t.Fatalf("ledger lost normal response identity: %+v", row)
		}
		info, err := os.Stat(local)
		if err != nil || info.IsDir() != (file.FileCategory == v115open.TypeDir) {
			t.Fatalf("generated path missing or wrong kind: %s err=%v", local, err)
		}
		delete(expected, row.FileId)
	}
	for _, index := range []int{0, len(files) - 1} {
		file := files[index]
		name := filepath.Join(root.GetFullLocalPath(), directories[index/(strmPerformanceVideos/strmPerformanceDirectories)].FileName, strings.TrimSuffix(file.FileName, ".mkv")+".strm")
		content, err := os.ReadFile(name)
		if err != nil || !strings.Contains(string(content), "pickcode="+file.PickCode) || !strings.Contains(string(content), "userid="+account.UserId) {
			t.Fatalf("STRM playback content is incomplete: %s err=%v", name, err)
		}
	}
}

// HEAD 基线只适配本 helper 与下面的根资料断言；其他扫描、HTTP、SQL 和计时代码保持一致。
func setupSTRMPerformanceRootEvidence(t *testing.T) {
	t.Helper()
	if err := models.MigrateEmbySyncEvidenceSchema(db.Db); err != nil {
		t.Fatal(err)
	}
}

func assertSTRMPerformanceRootEvidence(t *testing.T, account models.Account, root models.SyncPath, startedAt, finishedAt time.Time) {
	t.Helper()
	rows, err := models.LoadSyncRootEvidenceBatch(t.Context(), []uint{root.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("normal STRM root evidence did not persist: count=%d err=%v", len(rows), err)
	}
	evidence := rows[root.ID]
	if evidence.SourceType != root.SourceType || evidence.AccountID != account.ID || evidence.AccountIdentity != models.EmbyAccountIdentity(account) || evidence.RootFileID != "10" || evidence.RootPath != "/"+strmPerformanceRootPath || evidence.InvalidatedAt != 0 || evidence.ObservedAt < startedAt.Unix() || evidence.ObservedAt > finishedAt.Unix() {
		t.Fatalf("root evidence does not match actual scan/account/read time: %+v", evidence)
	}
	want := []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "5", ParentID: "0", Path: "/Library"}, {FileID: "10", ParentID: "5", Path: "/Library/Movies"}}
	if len(evidence.Ancestors) != len(want) {
		t.Fatalf("incomplete persisted root chain: %+v", evidence.Ancestors)
	}
	for i, ancestor := range want {
		if evidence.Ancestors[i] != ancestor {
			t.Fatalf("wrong persisted ancestor: %+v", evidence.Ancestors[i])
		}
	}
}
