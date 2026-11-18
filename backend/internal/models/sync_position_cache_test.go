package models

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	syncscope "qmediasync/internal/strm/scope"
)

func TestSyncPositionCacheColdHotAndContentUpdate(t *testing.T) {
	sp := setupSyncScopeTest(t)
	testSyncPositionCacheColdHot(t, sp)
}

func TestSameSyncFilePosition(t *testing.T) {
	base := SyncFile{SyncPathId: 1, SourceType: SourceType115, AccountId: 2, Path: "/remote", LocalFilePath: "F:/STRM/Show/E01.strm"}
	for _, tt := range []struct {
		name        string
		change      func(*SyncFile)
		samePOSIX   bool
		sameWindows bool
	}{
		{"same", func(*SyncFile) {}, true, true},
		{"content", func(f *SyncFile) { f.MTime++; f.FileSize++; f.Sha1 = "new" }, true, true},
		{"native", func(f *SyncFile) { f.LocalFilePath = `F:\STRM\Show\E01.strm` }, false, true},
		{"repeated", func(f *SyncFile) { f.LocalFilePath = `F:\\STRM/\Show//E01.strm` }, false, true},
		{"move", func(f *SyncFile) { f.LocalFilePath = "F:/STRM/Other/E01.strm" }, false, false},
		{"case", func(f *SyncFile) { f.LocalFilePath = "F:/STRM/show/E01.strm" }, false, false},
		{"dots", func(f *SyncFile) { f.LocalFilePath = "F:/STRM/./Show/E01.strm" }, false, false},
		{"trailing", func(f *SyncFile) { f.LocalFilePath += "/" }, false, false},
		{"leading", func(f *SyncFile) { f.LocalFilePath = "/" + f.LocalFilePath }, false, false},
		{"namespace", func(f *SyncFile) { f.LocalFilePath = `\\?\F:\STRM\Show\E01.strm` }, false, false},
		{"root", func(f *SyncFile) { f.SyncPathId++ }, false, false},
		{"source", func(f *SyncFile) { f.SourceType = SourceTypeBaiduPan }, false, false},
		{"account", func(f *SyncFile) { f.AccountId++ }, false, false},
		{"remote", func(f *SyncFile) { f.Path = "/elsewhere" }, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next := base
			tt.change(&next)
			want := tt.samePOSIX
			if filepath.Separator == '\\' {
				want = tt.sameWindows
			}
			if SameSyncFilePosition(&base, &next) != want || SameSyncFilePosition(&next, &base) != want {
				t.Fatalf("position comparison=%+v, want same=%v", next, want)
			}
		})
	}
	if SameSyncFilePosition(nil, &base) || SameSyncFilePosition(&base, nil) || SameSyncFilePosition(nil, nil) {
		t.Fatal("missing position must not match")
	}
}

func TestSyncPositionCacheReusesSeparatorOnlySave(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("Windows 路径表示与实际范围保护")
	}
	sp := setupSyncScopeTest(t)
	outside := t.TempDir()
	file := SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
		Path: sp.RemotePath, LocalFilePath: filepath.Join(outside, "episode.strm")}
	if err := db.Db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	version := syncPositionVersion()
	file.LocalFilePath = filepath.ToSlash(file.LocalFilePath)
	if err := SaveSyncFilePosition(db.Db, &file); err != nil {
		t.Fatal(err)
	}
	if syncPositionVersion() != version {
		t.Fatal("separator-only save discarded warm positions")
	}
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: outside})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, release, err = AcquireSyncPathScope(ctx, sp.ID)
	if release != nil {
		release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cached old spelling no longer protects the same actual directory: %v", err)
	}
}

func testSyncPositionCacheColdHot(t *testing.T, sp *SyncPath) {
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: sp.RemotePath, LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "movie.strm")}
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("count_positions", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			if _, ok := tx.Statement.Dest.(*[]SyncFile); ok {
				queries++
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("count_positions")
	acquire := func() {
		t.Helper()
		_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	acquire()
	if queries != 1 {
		t.Fatalf("cold queries=%d", queries)
	}
	acquire()
	if queries != 1 {
		t.Fatalf("hot queries=%d", queries)
	}
	file.MTime++
	file.FileSize++
	file.Sha1 = "changed"
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	acquire()
	if queries != 1 {
		t.Fatalf("content-only update discarded cache: %d", queries)
	}
	file.LocalFilePath = filepath.Join(t.TempDir(), "moved.strm")
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	acquire()
	if queries != 2 {
		t.Fatalf("move must rebuild: %d", queries)
	}
	finish := BeginSyncPositionMutation()
	err := db.Db.Delete(file).Error
	finish()
	if err != nil {
		t.Fatal(err)
	}
	acquire()
	if queries != 3 {
		t.Fatalf("delete must rebuild: %d", queries)
	}
}

func TestSyncPositionCacheRevalidatesSymlinkAcrossRounds(t *testing.T) {
	sp := setupSyncScopeTest(t)
	root := sp.GetFullLocalPath()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Mkdir(link, 0755); err != nil {
		t.Fatal(err)
	}
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: sp.RemotePath, LocalFilePath: filepath.Join(link, "file.strm")}
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: outside})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, release, err = AcquireSyncPathScope(ctx, sp.ID)
	if release != nil {
		release()
	}
	if err == nil {
		t.Fatal("cached logical path skipped moved symlink")
	}
}

func TestSyncPositionCacheRejectsBuildDuringMutation(t *testing.T) {
	sp := setupSyncScopeTest(t)
	count := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("mutate_during_positions", func(tx *gorm.DB) {
		if tx.Statement.Table != "sync_files" {
			return
		}
		count++
		if count == 1 {
			finish := BeginSyncPositionMutation()
			finish()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("mutate_during_positions")
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if count != 2 {
		t.Fatalf("must discard interrupted build, queries=%d", count)
	}
}

func TestSyncPositionCachePostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	schema := fmt.Sprintf("qms_scope_%d", time.Now().UnixNano())
	if err := conn.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	previous := db.Db
	db.Db = conn
	t.Cleanup(func() { db.Db = previous; conn.Exec("DROP SCHEMA " + schema + " CASCADE"); sqlDB.Close() })
	if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&SyncPath{}, &SyncFile{}); err != nil {
		t.Fatal(err)
	}
	sp := &SyncPath{SourceType: SourceType115, AccountId: 1, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/movies"}
	if err := conn.Create(sp).Error; err != nil {
		t.Fatal(err)
	}
	testSyncPositionCacheColdHot(t, sp)
}

func TestSyncPositionCacheResetPartialFailure(t *testing.T) {
	sp := setupSyncScopeTest(t)
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: "/old", LocalFilePath: filepath.Join(t.TempDir(), "old.strm")}
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	before, err := readSyncLogicalPositions(t.Context(), db.Db, sp.ID, true, "", "")
	if err != nil || len(before) != 1 {
		t.Fatalf("warm: %v %v", before, err)
	}
	tables := AllTables
	AllTables = []any{&SyncFile{}, &SyncPath{}}
	t.Cleanup(func() { AllTables = tables })
	if err := db.Db.Callback().Raw().Before("gorm:raw").Register("fail_second_drop", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "DROP TABLE") && strings.Contains(tx.Statement.SQL.String(), "sync_paths") {
			tx.AddError(errors.New("drop failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = BatchDropTable()
	db.Db.Callback().Raw().Remove("fail_second_drop")
	if err == nil {
		t.Fatal("expected partial reset error")
	}
	if err := db.Db.AutoMigrate(&SyncFile{}); err != nil {
		t.Fatal(err)
	}
	after, err := readSyncLogicalPositions(t.Context(), db.Db, sp.ID, true, "", "")
	if err != nil || len(after) != 0 {
		t.Fatalf("reset reused deleted positions: %v %v", after, err)
	}
}

func TestSyncPositionCacheChecksVersionAfterAcquire(t *testing.T) {
	sp := setupSyncScopeTest(t)
	reads := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("mutate_after_scope_acquired", func(tx *gorm.DB) {
		if tx.Statement.Table != "sync_paths" {
			return
		}
		reads++
		// 无实际等待时，取得许可后的配置重读期间仍可能发生位置提交。
		if reads == 2 {
			finish := BeginSyncPositionMutation()
			finish()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("mutate_after_scope_acquired")
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if reads != 4 {
		t.Fatalf("acquired scope must retry version change: reads=%d", reads)
	}
}

func TestSaveSyncFilePositionLocalPathFormat(t *testing.T) {
	for _, tt := range []struct {
		name, path, windows string
	}{
		{"native", `F:\STRM\Show\E01.strm`, "F:/STRM/Show/E01.strm"},
		{"repeated-backslash", `F:\\STRM\\Show\\E01.strm`, "F:/STRM/Show/E01.strm"},
		{"repeated-slash", "F://STRM//Show//E01.strm", "F:/STRM/Show/E01.strm"},
		{"mixed", `F:\/STRM/\Show\\E01.strm`, "F:/STRM/Show/E01.strm"},
		{"slash", "F:/STRM/Show/E01.strm", "F:/STRM/Show/E01.strm"},
		{"names", `f:\STRM\剧集\Episode A.strm`, "f:/STRM/剧集/Episode A.strm"},
		{"unc", `\\NAS\Media\Show\E01.strm`, "//NAS/Media/Show/E01.strm"},
		{"unc-repeated", `\\NAS\\Media\\Show\\E01.strm`, "//NAS/Media/Show/E01.strm"},
		{"single-root", `\STRM\\Show\E01.strm`, "/STRM/Show/E01.strm"},
		{"nonstandard-leading", `\\\NAS\\Media\E01.strm`, "///NAS/Media/E01.strm"},
		{"extended", `\\?\F:\\STRM\\Show\E01.strm`, `\\?\F:\\STRM\\Show\E01.strm`},
		{"device", `\\.\F:\\STRM\E01.strm`, `\\.\F:\\STRM\E01.strm`},
		{"nt-namespace", `\??\F:\\STRM\E01.strm`, `\??\F:\\STRM\E01.strm`},
		{"namespace-variant", `//?\F:\\STRM\E01.strm`, `//?\F:\\STRM\E01.strm`},
		{"trailing", `F:\STRM\\`, "F:/STRM/"},
		{"drive-root", `F:\\`, "F:/"},
		{"drive-relative", `F:Show\\E01.strm`, "F:Show/E01.strm"},
		{"dots", `F:\\STRM\.\Show\\..\\Other\E01.strm`, "F:/STRM/./Show/../Other/E01.strm"},
		{"relative", `Show\\E01.strm`, "Show/E01.strm"},
		{"posix-backslash", `/data/STRM/Show/Episode\A.strm`, "/data/STRM/Show/Episode/A.strm"},
		{"posix-repeated-slash", "/data/STRM//Show/E01.strm", "/data/STRM/Show/E01.strm"},
		{"empty", "", ""},
	} {
		t.Run("insert/"+tt.name, func(t *testing.T) {
			sp := setupSyncScopeTest(t)
			file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
				FileId: "video", FileName: "E01.mkv", Path: sp.RemotePath, LocalFilePath: tt.path, Uploaded: true}
			if err := SaveSyncFilePosition(db.Db, file); err != nil {
				t.Fatal(err)
			}
			want := tt.path
			if filepath.Separator == '\\' {
				want = tt.windows
			}
			var got SyncFile
			if err := db.Db.First(&got, file.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.LocalFilePath != want || file.LocalFilePath != want || got.ID == 0 || !got.Uploaded || got.FileName != "E01.mkv" {
				t.Fatalf("saved=%+v object path=%q, want path=%q", got, file.LocalFilePath, want)
			}
		})
	}
	for _, tt := range []struct {
		name, previous, next, windows string
		sameWindows                   bool
	}{
		{"native-to-slash", `F:\STRM\Show\E01.strm`, "F:/STRM/Show/E01.strm", "F:/STRM/Show/E01.strm", true},
		{"slash-to-native", "F:/STRM/Show/E01.strm", `F:\STRM\Show\E01.strm`, "F:/STRM/Show/E01.strm", true},
		{"repeated-to-slash", `F:\\STRM\\Show\\E01.strm`, "F:/STRM/Show/E01.strm", "F:/STRM/Show/E01.strm", true},
		{"slash-to-repeated", "F:/STRM/Show/E01.strm", `F:\\STRM\\Show\\E01.strm`, "F:/STRM/Show/E01.strm", true},
		{"unc-alias", `\\NAS\\Media\\E01.strm`, "//NAS/Media/E01.strm", "//NAS/Media/E01.strm", true},
		{"move", `F:\STRM\Show\E01.strm`, `F:\\STRM\\Other\\E02.strm`, "F:/STRM/Other/E02.strm", false},
		{"case-change", `F:\STRM\Show\Movie.strm`, `F:\STRM\Show\movie.strm`, "F:/STRM/Show/movie.strm", false},
		{"namespace-alias-keeps-next", `\\?\F:\STRM\E01.strm`, "//?/F:/STRM/E01.strm", "//?/F:/STRM/E01.strm", false},
	} {
		t.Run("update/"+tt.name, func(t *testing.T) {
			sp := setupSyncScopeTest(t)
			old := SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
				FileId: "video", FileName: "E01.mkv", Path: sp.RemotePath, LocalFilePath: tt.previous, Uploaded: true}
			if err := db.Db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			frozen := EmbyFrozenFile{SyncFileID: old.ID, FileCreatedAt: old.CreatedAt, SyncPathID: old.SyncPathId,
				SourceType: old.SourceType, AccountID: old.AccountId, FileID: old.FileId, FileName: old.FileName,
				Path: old.Path, LocalFilePath: old.LocalFilePath}
			if _, err := readSyncLogicalPositions(t.Context(), db.Db, sp.ID, true, "", ""); err != nil {
				t.Fatal(err)
			}
			version := syncPositionVersion()
			next := old
			next.LocalFilePath, next.ThumbUrl = tt.next, "new-thumb"
			if err := SaveSyncFilePosition(db.Db, &next); err != nil {
				t.Fatal(err)
			}
			want := tt.next
			if filepath.Separator == '\\' {
				want = tt.windows
			}
			var got SyncFile
			if err := db.Db.First(&got, old.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.LocalFilePath != want || next.LocalFilePath != want || got.CreatedAt != old.CreatedAt || got.ThumbUrl != "new-thumb" || !got.Uploaded {
				t.Fatalf("saved=%+v object path=%q, want path=%q", got, next.LocalFilePath, want)
			}
			positionChanged := want != tt.previous
			if filepath.Separator == '\\' {
				positionChanged = !tt.sameWindows
			}
			if (syncPositionVersion() != version) != positionChanged {
				t.Fatalf("position cache invalidation: changed=%v", positionChanged)
			}
			cachedPath := want
			if !positionChanged {
				cachedPath = tt.previous
			}
			positions, err := readSyncLogicalPositions(t.Context(), db.Db, sp.ID, true, "", "")
			if err != nil || len(positions) != 1 || positions[0].LocalFilePath != cachedPath {
				t.Fatalf("positions=%+v err=%v", positions, err)
			}
			if embyFrozenMatchesLedger(frozen, got) != !positionChanged {
				t.Fatalf("directory evidence match: positionChanged=%v", positionChanged)
			}
		})
	}
}
