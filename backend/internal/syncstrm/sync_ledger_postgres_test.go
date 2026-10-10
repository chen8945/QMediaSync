//go:build integration

package syncstrm

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func setupSyncLedgerPostgres(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for an isolated PostgreSQL test database")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil || parsed.Host == "" {
		t.Fatal("QMS_TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgres.New(postgres.Config{DriverName: "pgx", DSN: dsn}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	schema := fmt.Sprintf("qms_ledger_apply_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	query.Set("statement_timeout", "10000")
	parsed.RawQuery = query.Encode()
	setupLocalEvidenceDatabase(t, postgres.New(postgres.Config{DriverName: "pgx", DSN: parsed.String()}))
	return schema
}

func TestSyncLedgerPostgresSerializesBeforeSourceRows(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("position_changed=%t", changed), func(t *testing.T) {
			schema := setupSyncLedgerPostgres(t)
			conn := db.Db
			account := models.Account{BaseModel: models.BaseModel{ID: 7}, SourceType: models.SourceType115, UserId: "ledger-owner"}
			root := models.SyncPath{BaseModel: models.BaseModel{ID: 1}, SourceType: account.SourceType, AccountId: account.ID,
				BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/movies"}
			config := models.EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "test-key", SyncEnabled: 1}
			for _, row := range []any{&account, &root, &config} {
				if err := conn.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			directory := models.SyncFile{BaseModel: models.BaseModel{CreatedAt: 100, UpdatedAt: 100},
				SourceType: account.SourceType, AccountId: account.ID, SyncPathId: root.ID,
				FileId: "movie-directory", ParentId: "root", FileType: v115open.TypeDir, Path: "/movies", FileName: "Movie",
				LocalFilePath: filepath.Join(root.GetFullLocalPath(), "Movie")}
			video := models.SyncFile{BaseModel: models.BaseModel{CreatedAt: 100, UpdatedAt: 100},
				SourceType: account.SourceType, AccountId: account.ID, SyncPathId: root.ID,
				FileId: "video", ParentId: directory.FileId, FileType: v115open.TypeFile, Path: "/movies/Movie", FileName: "movie.mkv",
				LocalFilePath: filepath.Join(directory.LocalFilePath, "movie.strm"), PickCode: "movie-pick", Sha1: "old-sha", FileSize: 42, MTime: 123, IsVideo: true}
			for _, row := range []any{&directory, &video} {
				if err := conn.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			rootEvidence := models.SyncRootEvidence{SyncPathID: root.ID, SourceType: root.SourceType, AccountID: root.AccountId,
				AccountIdentity: models.EmbyAccountIdentity(account), RootFileID: root.BaseCid, RootPath: "/movies", ObservedAt: 123,
				Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: "/movies"}}}
			if err := models.SaveSyncRootEvidenceTx(conn, rootEvidence, root.BaseCid); err != nil {
				t.Fatal(err)
			}
			token, err := models.BeginEmbyIndexRead("ledger-server", &config)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := models.EmbyItemSnapshot{
				Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", LibraryId: "lib", Path: video.LocalFilePath},
				Sources: []models.EmbySnapshotSource{{ID: "video-source", ItemID: "101", PickCode: video.PickCode,
					Path: "http://qms.fixture/stream?pickcode=" + video.PickCode}},
				MembersComplete: true,
			}
			collector := models.EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &snapshot, []models.EmbyMediaDirectory{{ItemID: "101", MediaType: "Movie", LocalPath: directory.LocalFilePath}}); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.DirectoryScopes) != 1 {
				t.Fatalf("fixture has no validated directory: %+v", snapshot.DirectoryScopes)
			}
			cachedDirectory, cachedVideo := ledgerTestCache(directory, false), ledgerTestCache(video, false)
			cachedVideo.Sha1 = "new-sha"
			if changed {
				cachedVideo.LocalFilePath = filepath.Join(directory.LocalFilePath, "moved.strm")
			}
			syncer := ledgerTestSync(t, []*SyncFileCache{cachedDirectory, cachedVideo})
			if models.SameSyncFilePosition(&video, cachedVideo.GetSyncFile(syncer, "")) == changed {
				t.Fatal("fixture did not exercise the intended position-change branch")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			syncer.Context = ctx
			conn.NowFunc = func() time.Time { return time.Unix(300, 0) }
			held := make(chan int, 1)
			released := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(released) }) }
			var paused atomic.Bool
			var sourceWrites atomic.Int32
			const callback = "test:ledger_source_lock_order"
			if err := conn.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				files, ok := tx.Statement.Dest.(*[]models.SyncFile)
				if tx.Error != nil || !ok || !slices.ContainsFunc(*files, func(file models.SyncFile) bool { return file.ID == video.ID }) || !paused.CompareAndSwap(false, true) {
					return
				}
				if !strings.Contains(tx.Statement.SQL.String(), "FOR SHARE") {
					tx.AddError(fmt.Errorf("snapshot has not locked the video row"))
					return
				}
				var pid int
				if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					tx.AddError(err)
					return
				}
				held <- pid
				select {
				case <-released:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := conn.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
				if tx.Error == nil && tx.Statement.Table == "sync_files" {
					sourceWrites.Add(1)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				conn.Callback().Query().Remove(callback)
				conn.Callback().Update().Remove(callback)
			})
			finished := make(chan struct{}, 2)
			started := 0
			defer func() {
				release()
				cancel()
				for range started {
					select {
					case <-finished:
					case <-time.After(5 * time.Second):
						t.Error("database operation did not exit during cleanup")
					}
				}
			}()
			applyResult := make(chan error, 1)
			started++
			go func() {
				defer func() { finished <- struct{}{} }()
				applyResult <- models.ApplyEmbySnapshotsContext(ctx, token, []models.EmbyItemSnapshot{snapshot})
			}()
			var blockerPID int
			select {
			case blockerPID = <-held:
			case err := <-applyResult:
				t.Fatalf("snapshot did not reach its video lock: %v", err)
			case <-ctx.Done():
				t.Fatal("snapshot did not reach its video lock")
			}
			ledgerResult := make(chan error, 1)
			started++
			go func() {
				defer func() { finished <- struct{}{} }()
				ledgerResult <- syncer.handleTempTableDiff()
			}()
			waitSyncLedgerPostgresGuard(t, conn, schema, blockerPID, ledgerResult)
			if sourceWrites.Load() != 0 {
				t.Fatal("ledger changed source rows before acquiring the shared snapshot guard")
			}
			release()
			for _, result := range []<-chan error{applyResult, ledgerResult} {
				select {
				case err := <-result:
					if err != nil {
						t.Fatalf("concurrent transaction did not commit successfully: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("concurrent transactions did not finish after releasing the snapshot")
				}
			}
			var savedVideo, savedDirectory models.SyncFile
			if err := conn.First(&savedVideo, video.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.First(&savedDirectory, directory.ID).Error; err != nil {
				t.Fatal(err)
			}
			if sourceWrites.Load() != 2 || savedVideo.Sha1 != "new-sha" || savedVideo.LocalFilePath != cachedVideo.LocalFilePath || savedDirectory.UpdatedAt != 300 || syncer.memSyncCache.Count() != 0 {
				t.Fatalf("ledger did not commit both its unchanged directory and changed video: writes=%d video=%+v directory=%+v", sourceWrites.Load(), savedVideo, savedDirectory)
			}
			var guard models.EmbyIndexState
			if err := conn.First(&guard, 1).Error; err != nil || guard.Revision != token.Revision {
				t.Fatalf("source mutation changed the Emby revision: guard=%+v err=%v", guard, err)
			}
			var item models.EmbyMediaItem
			if err := conn.Where("item_id = ?", snapshot.Item.ItemId).First(&item).Error; err != nil || item.SnapshotID == 0 {
				t.Fatalf("snapshot was not committed: item=%+v err=%v", item, err)
			}
		})
	}
}

func waitSyncLedgerPostgresGuard(t *testing.T, conn *gorm.DB, schema string, blockerPID int, result <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-result:
			t.Fatalf("ledger finished before the snapshot released its source locks: %v", err)
		case <-deadline.C:
			t.Fatal("PostgreSQL did not report the ledger waiting for the snapshot guard")
		case <-tick.C:
			var query string
			if err := conn.Raw(`SELECT query FROM pg_stat_activity
				WHERE application_name = ? AND wait_event_type = 'Lock'
				AND ? = ANY(pg_blocking_pids(pid)) LIMIT 1`, schema, blockerPID).Scan(&query).Error; err != nil {
				t.Fatal(err)
			}
			if query == "" {
				continue
			}
			if !strings.Contains(query, "emby_index_states") {
				t.Fatal("ledger waited for source rows before acquiring the snapshot guard")
			}
			return
		}
	}
}
