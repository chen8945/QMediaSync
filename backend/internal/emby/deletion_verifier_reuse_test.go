package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

type deletionVerificationFixture struct {
	conn           *gorm.DB
	input          models.EmbyDeletionInput
	target         models.EmbyDeletionTarget
	config         models.EmbyConfig
	clock          atomic.Int64
	pages          atomic.Int64
	parts          atomic.Int64
	ids            atomic.Int64
	servers        atomic.Int64
	alive          atomic.Bool
	sharedPart     atomic.Bool
	badPart        atomic.Bool
	badPage        atomic.Bool
	scanDelay      atomic.Int64
	changeRevision atomic.Bool
}

func newDeletionVerificationFixture(t *testing.T) *deletionVerificationFixture {
	t.Helper()
	previous := db.Db
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Db = conn
	t.Cleanup(func() { db.Db = previous; _ = sqlDB.Close() })
	if err := conn.AutoMigrate(&models.EmbyConfig{}, &models.EmbyIndexState{}, &models.EmbyItemEvidence{}, &models.SyncFile{}); err != nil {
		t.Fatal(err)
	}
	f := &deletionVerificationFixture{conn: conn}
	f.clock.Store(time.Now().UnixNano())
	root := t.TempDir()
	original := filepath.Join(root, "movie", "original.strm")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			f.servers.Add(1)
			fmt.Fprint(w, `{"Id":"server-a"}`)
		case "/emby/Items":
			if r.URL.Query().Get("Ids") != "" {
				f.ids.Add(1)
				if f.alive.Load() {
					fmt.Fprint(w, `{"Items":[{"Id":"1","Type":"Movie"}],"TotalRecordCount":1}`)
					return
				}
				fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
				return
			}
			f.pages.Add(1)
			if f.changeRevision.Load() {
				if err := f.conn.Model(&models.EmbyIndexState{}).Where("id = 1").UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
			if r.URL.Query().Get("ParentId") != "" {
				t.Error("full verification was restricted to selected libraries")
			}
			if r.URL.Query().Get("StartIndex") == "0" {
				f.clock.Add(f.scanDelay.Load())
				fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Episode","Path":"/other/main.strm","SeasonId":"10","PartCount":2,"MediaSources":[{"Id":"other","Path":"http://qms/stream?pickcode=other"}]}],"TotalRecordCount":2}`)
			} else if f.badPage.Load() {
				fmt.Fprint(w, `{"Items":[],"TotalRecordCount":2}`)
			} else {
				fmt.Fprint(w, `{"Items":[{"Id":"4","Type":"Movie","Path":"/other/direct.mkv","MediaSources":[{"Id":"direct","Path":"/other/direct.mkv"}]}],"TotalRecordCount":2}`)
			}
		case "/emby/Videos/2/AdditionalParts":
			f.parts.Add(1)
			if f.badPart.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			code := "other-part"
			if f.sharedPart.Load() {
				code = "original"
			}
			fmt.Fprintf(w, `{"Items":[{"Id":"3","Type":"Video","Path":"/other/part.strm","MediaSources":[{"Id":"part","Path":"http://qms/stream?pickcode=%s"}]}],"TotalRecordCount":1}`, code)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.config = models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, EnableDeleteNetdisk: 1}
	if err := conn.Create(&f.config).Error; err != nil {
		t.Fatal(err)
	}
	state := models.EmbyIndexState{BaseModel: models.BaseModel{ID: 1}, ServerID: "server-a", ServerConfigKey: models.EmbyServerConfigIdentity(&f.config), ConfigKey: models.EmbyConfigIdentity(&f.config), Revision: 1}
	if err := conn.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	sources, err := json.Marshal([]models.EmbySnapshotSource{{ID: "original-source", Path: "http://qms/stream?pickcode=original"}})
	if err != nil {
		t.Fatal(err)
	}
	owner := models.EmbyDeletionOwner{
		Item:     models.EmbyMediaItem{ItemId: "1", Path: original},
		Evidence: models.EmbyItemEvidence{BaseModel: models.BaseModel{ID: 1}, Generation: 1, SourcesJSON: string(sources)},
		Files:    []models.EmbyFrozenFile{{LocalRoot: root, LocalFilePath: original, PickCode: "original", SourceID: "original-source"}},
	}
	f.input = models.EmbyDeletionInput{ServerID: "server-a", ServerConfigKey: state.ServerConfigKey, ItemID: "1", ItemType: "Movie", Owners: []models.EmbyDeletionOwner{owner}, CleanupPolicy: models.CurrentEmbyCleanupPolicy()}
	f.target = models.EmbyDeletionTarget{Owners: []models.EmbyDeletionOwnerRef{{ItemID: "1", SnapshotID: 1, Generation: 1}}}
	return f
}

func (f *deletionVerificationFixture) attempt(ctx context.Context) context.Context {
	ctx = newEmbyDeletionVerificationContext(ctx)
	cache := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	cache.now = func() time.Time { return time.Unix(0, f.clock.Load()) }
	return models.WithEmbyDirectoryProvider(ctx, &webhookTestProvider{})
}

func TestEmbyDeletionVerificationReuseCountsCompleteHTTP(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	ctx := f.attempt(t.Context())
	for range 3 {
		if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
			t.Fatal(err)
		}
	}
	if f.pages.Load() != 2 || f.parts.Load() != 1 || f.ids.Load() != 3 || f.servers.Load() != 3 {
		t.Fatalf("pages=%d parts=%d ids=%d servers=%d", f.pages.Load(), f.parts.Load(), f.ids.Load(), f.servers.Load())
	}
}

func TestEmbyDeletionVerificationRebuildsExpiredOrChangedEvidence(t *testing.T) {
	for _, change := range []string{"expiry", "revision", "evidence", "new attempt"} {
		t.Run(change, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			ctx := f.attempt(t.Context())
			if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
				t.Fatal(err)
			}
			f.sharedPart.Store(true)
			switch change {
			case "expiry":
				f.clock.Add(int64(embyDeletionVerificationWindow))
			case "revision":
				if err := f.conn.Model(&models.EmbyIndexState{}).Where("id = 1").Update("revision", 2).Error; err != nil {
					t.Fatal(err)
				}
			case "evidence":
				if err := f.conn.Create(&models.EmbyItemEvidence{ItemID: "4"}).Error; err != nil {
					t.Fatal(err)
				}
			case "new attempt":
				ctx = f.attempt(t.Context())
			}
			err := verifyEmbyDeletion(ctx, f.input, f.target)
			if _, ok := errors.AsType[*embyProtectedOwnersError](err); !ok {
				t.Fatalf("fresh hidden shared part was not protected: %v", err)
			}
			if f.pages.Load() != 4 || f.parts.Load() != 2 {
				t.Fatalf("pages=%d parts=%d", f.pages.Load(), f.parts.Load())
			}
		})
	}
}

func TestEmbyDeletionVerificationDoesNotReuseIncompleteOrOverBudgetScan(t *testing.T) {
	for _, failure := range []string{"part", "page", "collection timeout", "revision during scan"} {
		t.Run(failure, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			ctx := f.attempt(t.Context())
			switch failure {
			case "part":
				f.badPart.Store(true)
			case "page":
				f.badPage.Store(true)
			case "collection timeout":
				f.scanDelay.Store(int64(embyDeletionCollectionBudget))
			case "revision during scan":
				f.changeRevision.Store(true)
			}
			if err := verifyEmbyDeletion(ctx, f.input, f.target); err == nil {
				t.Fatal("incomplete or expired scan authorized deletion")
			}
			f.badPart.Store(false)
			f.badPage.Store(false)
			f.scanDelay.Store(0)
			f.changeRevision.Store(false)
			if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
				t.Fatal(err)
			}
			if f.pages.Load() != 4 {
				t.Fatalf("failed scan reused: pages=%d", f.pages.Load())
			}
		})
	}
}

func TestEmbyDeletionVerificationLongCompleteScanCanBeReused(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	f.scanDelay.Store(int64(31 * time.Second))
	ctx := f.attempt(t.Context())
	for range 2 {
		if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
			t.Fatal(err)
		}
	}
	if f.pages.Load() != 2 || f.parts.Load() != 1 {
		t.Fatalf("long successful scan unnecessarily repeated: pages=%d parts=%d", f.pages.Load(), f.parts.Load())
	}
	f.clock.Add(int64(embyDeletionVerificationWindow))
	if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
		t.Fatal(err)
	}
	if f.pages.Load() != 4 || f.parts.Load() != 2 {
		t.Fatalf("completed context survived a long wait: pages=%d parts=%d", f.pages.Load(), f.parts.Load())
	}
}

func TestEmbyDeletionVerificationKeepsLocalFinalGuards(t *testing.T) {
	for _, change := range []string{"original ID", "STRM", "configuration", "disabled"} {
		t.Run(change, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			ctx := f.attempt(t.Context())
			if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "original ID":
				f.alive.Store(true)
			case "STRM":
				original := f.input.Owners[0].Item.Path
				if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(original, []byte("new STRM"), 0600); err != nil {
					t.Fatal(err)
				}
			case "configuration":
				if err := f.conn.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("emby_api_key", "changed").Error; err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if err := f.conn.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("enable_delete_netdisk", 0).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyEmbyDeletion(ctx, f.input, f.target); err == nil {
				t.Fatal("cached full inventory bypassed final protection")
			}
			if f.pages.Load() != 2 {
				t.Fatalf("local check repeated full inventory: %d", f.pages.Load())
			}
		})
	}
}

func TestEmbyDirectoryLiveRetentionChecksPhysicalAndSourceScope(t *testing.T) {
	for _, test := range []struct {
		name          string
		local, remote string
		physical      string
		account       uint
		wantAllowed   bool
	}{
		{name: "unindexed inside", local: "/media/Show/unknown.strm"},
		{name: "renamed outside shares inside", local: "/media/Other/renamed.strm", remote: "/cloud/Show", account: 1},
		{name: "outside mapped source", local: "/media/Other/file.strm", remote: "/cloud/Other", account: 1, wantAllowed: true},
		{name: "sibling prefix", local: "/media/Show Extras/file.strm", remote: "/cloud/Show Extras", account: 1, wantAllowed: true},
		{name: "unknown source location", local: "/media/Other/file.strm"},
		{name: "other account cannot establish location", local: "/media/Other/file.strm", remote: "/cloud/Other", account: 2},
		{name: "local physical source outside", local: "/media/Other/file.mkv", physical: "/media/Other/file.mkv", wantAllowed: true},
		{name: "local physical source inside", local: "/media/Other/file.mkv", physical: "/media/Show/file.mkv"},
		{name: "absolute physical source inside cloud root", local: "/media/Other/file.mkv", physical: "/cloud/Show/file.mkv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			if test.remote != "" {
				if err := f.conn.Create(&models.SyncFile{SourceType: models.SourceType115, AccountId: test.account, Path: test.remote, FileName: "live.mkv", PickCode: "live"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			scope := models.EmbyDirectoryScope{LocalPath: "/media/Show", Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Show"}}
			snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "9", Path: test.local}, Sources: []models.EmbySnapshotSource{{Path: "http://qms/stream?pickcode=live", PickCode: "live"}}}
			if test.physical != "" {
				snapshot.Sources[0] = models.EmbySnapshotSource{Path: test.physical}
			}
			err := verifyEmbyDirectoryLiveItems(models.WithEmbyDirectoryProvider(t.Context(), &webhookTestProvider{}), scope, []models.EmbyItemSnapshot{snapshot})
			if (err == nil) != test.wantAllowed {
				t.Fatalf("err=%v, allowed=%t", err, test.wantAllowed)
			}
			if f.pages.Load()+f.ids.Load()+f.servers.Load()+f.parts.Load() != 0 {
				t.Fatal("directory lookup queried unknown files through Emby")
			}
		})
	}
}

func TestEmbyDirectoryLiveRetentionDoesNotInventUnknownOwners(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	scope := models.EmbyDirectoryScope{LocalPath: "/media/Show", Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Show"}}
	if err := verifyEmbyDirectoryLiveItems(models.WithEmbyDirectoryProvider(t.Context(), &webhookTestProvider{}), scope, nil); err != nil {
		t.Fatal(err)
	}
	if f.pages.Load()+f.ids.Load()+f.servers.Load()+f.parts.Load() != 0 {
		t.Fatal("empty live inventory triggered an ownership query")
	}
}

func TestEmbyDeletionVerificationObservedRevivalInvalidatesFullScan(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	ctx := f.attempt(t.Context())
	if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
		t.Fatal(err)
	}
	f.alive.Store(true)
	if err := verifyEmbyDeletion(ctx, f.input, f.target); err == nil {
		t.Fatal("original item revival ignored")
	}
	f.alive.Store(false)
	f.sharedPart.Store(true)
	err := verifyEmbyDeletion(ctx, f.input, f.target)
	if _, ok := errors.AsType[*embyProtectedOwnersError](err); !ok {
		t.Fatalf("revival left old inventory reusable: %v", err)
	}
	if f.pages.Load() != 4 {
		t.Fatalf("pages=%d", f.pages.Load())
	}
}

func TestEmbyDirectoryLiveRetentionUsesCompletePartsAndReusesSourceMap(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	for _, code := range []string{"other", "other-part"} {
		if err := f.conn.Create(&models.SyncFile{SourceType: models.SourceType115, AccountId: 1, Path: "/cloud/Other", FileName: code + ".mkv", PickCode: code}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var ledgerQueries atomic.Int64
	if err := f.conn.Callback().Query().Before("gorm:query").Register("count_directory_source_reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			ledgerQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.target.Directory = &models.EmbyDirectoryScope{LocalPath: filepath.Dir(f.input.Owners[0].Item.Path), Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Movie"}}
	ctx := f.attempt(t.Context())
	for range 2 {
		if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
			t.Fatal(err)
		}
	}
	if f.pages.Load() != 2 || f.parts.Load() != 1 || f.ids.Load() != 2 {
		t.Fatalf("pages=%d parts=%d ids=%d", f.pages.Load(), f.parts.Load(), f.ids.Load())
	}
	if ledgerQueries.Load() != 1 {
		t.Fatalf("same-attempt source map was reread: %d", ledgerQueries.Load())
	}
	// 持锁期间不改账本；新一次执行必须重新读取，以保护移到目录内的分段来源。
	if err := f.conn.Model(&models.SyncFile{}).Where("pick_code = ?", "other-part").Update("path", "/cloud/Movie").Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyEmbyDeletion(f.attempt(t.Context()), f.input, f.target); err == nil {
		t.Fatal("live additional part inside directory was ignored")
	}
	if f.pages.Load() != 4 || f.parts.Load() != 2 {
		t.Fatalf("pages=%d parts=%d", f.pages.Load(), f.parts.Load())
	}
	if ledgerQueries.Load() != 2 {
		t.Fatalf("new attempt did not reread source map: %d", ledgerQueries.Load())
	}
}

func TestEmbyScopedMetadataProtectsUnindexedSeasonMembers(t *testing.T) {
	for _, test := range []struct {
		name, itemType, itemID, season string
		wantAllowed                    bool
	}{
		{name: "Season same season", itemType: "Season", itemID: "10", season: "10"},
		{name: "Series same season", itemType: "Series", itemID: "20", season: "10"},
		{name: "Series other season", itemType: "Series", itemID: "20", season: "11", wantAllowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			f.input.ItemID, f.input.ItemType = test.itemID, test.itemType
			f.target.Kind = "scoped_metadata"
			f.target.File = models.EmbyFrozenFile{FileID: "season-poster"}
			f.input.ScopedFiles = []models.EmbyScopedFile{{File: f.target.File, ScopeType: "Season", ScopeItemID: test.season}}
			err := verifyEmbyDeletion(f.attempt(t.Context()), f.input, f.target)
			if (err == nil) != test.wantAllowed {
				t.Fatalf("scoped season artwork protection: err=%v, allowed=%t", err, test.wantAllowed)
			}
			if f.pages.Load() != 2 || f.parts.Load() != 1 {
				t.Fatalf("incomplete season check: pages=%d parts=%d", f.pages.Load(), f.parts.Load())
			}
		})
	}
}

func TestEmbyScopedMetadataBatchChecksEverySeasonWithOneSnapshot(t *testing.T) {
	for _, firstKind := range []string{"video", "scoped_metadata"} {
		t.Run(firstKind, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			f.input.ItemID, f.input.ItemType = "20", "Series"
			allowed := models.EmbyFrozenFile{FileID: "season11-poster"}
			retained := models.EmbyFrozenFile{FileID: "season10-poster"}
			f.input.ScopedFiles = []models.EmbyScopedFile{
				{File: allowed, ScopeType: "Season", ScopeItemID: "11"},
				{File: retained, ScopeType: "Season", ScopeItemID: "10"},
			}
			f.target.Kind, f.target.File = firstKind, allowed
			f.target.ScopedMetadata = []models.EmbyFrozenFile{allowed, retained}
			ctx := f.attempt(t.Context())
			err := verifyEmbyDeletion(ctx, f.input, f.target)
			conflict, ok := errors.AsType[*models.EmbyDeletionTargetConflict](err)
			if !ok || len(conflict.Keys) != 1 || conflict.Keys[0] != models.EmbyDeletionFileKey(retained) {
				t.Fatalf("later season protection was lost by aggregate verification: %v", err)
			}
			f.target.ScopedMetadata = []models.EmbyFrozenFile{allowed}
			if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
				t.Fatalf("independent season metadata was blocked: %v", err)
			}
			if f.pages.Load() != 2 || f.parts.Load() != 1 {
				t.Fatalf("exceptional subset repeated full scan: pages=%d parts=%d", f.pages.Load(), f.parts.Load())
			}
		})
	}
}

func TestEmbyDirectoryLiveRetentionPreservesLedgerQueryFailure(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	if err := f.conn.Migrator().DropTable(&models.SyncFile{}); err != nil {
		t.Fatal(err)
	}
	scope := models.EmbyDirectoryScope{LocalPath: "/media/Show", Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Show"}}
	snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "9", Path: "/media/Other/live.strm"}, Sources: []models.EmbySnapshotSource{{Path: "http://qms/stream?pickcode=live", PickCode: "live"}}}
	if err := verifyEmbyDirectoryLiveItems(models.WithEmbyDirectoryProvider(t.Context(), &webhookTestProvider{}), scope, []models.EmbyItemSnapshot{snapshot}); err == nil {
		t.Fatal("ledger query failure treated as no retained users")
	}
}

func TestEmbyDirectoryLiveRetentionRejectsUnresolvedSourcesDespiteBindings(t *testing.T) {
	for _, test := range []struct {
		name          string
		account       uint
		local         string
		code          string
		mappedInside  bool
		contradiction bool
	}{
		{name: "unrelated HTTP under same account root", account: 1, local: "/second/cloud/Other/live.strm"},
		{name: "unindexed pickcode under disjoint root", account: 1, local: "/second/cloud/Other/live.strm", code: "live"},
		{name: "different account same remote path", account: 2, local: "/second/cloud/Show/live.strm", code: "live"},
		{name: "source contradicts disjoint STRM", account: 1, local: "/second/cloud/Other/live.strm", code: "live", contradiction: true},
		{name: "unmapped local path", account: 1, local: "/outside/live.strm", code: "live"},
		{name: "mapping reaches original root", account: 1, local: "/second/cloud/Show/live.strm", code: "live"},
		{name: "one overlapping binding among accounts", account: 2, local: "/second/cloud/Show/live.strm", code: "live", mappedInside: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			if err := f.conn.AutoMigrate(&models.Account{}, &models.SyncPath{}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []uint{1, 2} {
				if err := f.conn.Create(&models.Account{BaseModel: models.BaseModel{ID: id}, SourceType: models.SourceType115, UserId: fmt.Sprint(id)}).Error; err != nil {
					t.Fatal(err)
				}
			}
			root := models.SyncPath{AccountId: test.account, SourceType: models.SourceType115, BaseCid: "bound-root", LocalPath: "/second", RemotePath: "/cloud"}
			if err := f.conn.Create(&root).Error; err != nil {
				t.Fatal(err)
			}
			if test.mappedInside {
				root.ID, root.AccountId, root.BaseCid = 0, 1, "overlapping-root"
				if err := f.conn.Create(&root).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.contradiction {
				if err := f.conn.Create(&models.SyncFile{SourceType: models.SourceType115, AccountId: 1, Path: "/cloud/Show", FileName: "live.mkv", PickCode: test.code}).Error; err != nil {
					t.Fatal(err)
				}
			}
			scope := models.EmbyDirectoryScope{LocalPath: "/media/Show", Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Show"}}
			snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "9", Path: test.local}, Sources: []models.EmbySnapshotSource{{Path: "http://playback/live", PickCode: test.code}}}
			err := verifyEmbyDirectoryLiveItems(models.WithEmbyDirectoryProvider(t.Context(), &webhookTestProvider{}), scope, []models.EmbyItemSnapshot{snapshot})
			if err == nil {
				t.Fatal("STRM binding was used to exclude an unresolved playback source")
			}
			if f.pages.Load()+f.ids.Load()+f.servers.Load()+f.parts.Load() != 0 {
				t.Fatal("source exclusion introduced per-file remote queries")
			}
		})
	}
}

func TestEmbyDirectoryLiveRetentionReusesAndExpiresSourceLedger(t *testing.T) {
	f := newDeletionVerificationFixture(t)
	for _, code := range []string{"other", "other-part"} {
		if err := f.conn.Create(&models.SyncFile{SourceType: models.SourceType115, AccountId: 1, Path: "/cloud/Other", FileName: code + ".mkv", PickCode: code}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var queries atomic.Int64
	if err := f.conn.Callback().Query().Before("gorm:query").Register("count_directory_source_reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			queries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.target.Directory = &models.EmbyDirectoryScope{LocalPath: filepath.Dir(f.input.Owners[0].Item.Path), Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Movie"}}
	ctx := f.attempt(t.Context())
	for range 2 {
		if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil {
			t.Fatal(err)
		}
	}
	if queries.Load() != 1 || f.pages.Load() != 2 || f.parts.Load() != 1 {
		t.Fatalf("ledger=%d pages=%d parts=%d", queries.Load(), f.pages.Load(), f.parts.Load())
	}
	if err := f.conn.Where("pick_code = ?", "other-part").Delete(&models.SyncFile{}).Error; err != nil {
		t.Fatal(err)
	}
	f.clock.Add(int64(embyDeletionVerificationWindow))
	if err := verifyEmbyDeletion(ctx, f.input, f.target); err == nil {
		t.Fatal("expired context authorized a source that no longer has a ledger location")
	}
	if queries.Load() != 2 || f.pages.Load() != 4 {
		t.Fatalf("ledger=%d pages=%d", queries.Load(), f.pages.Load())
	}
}
