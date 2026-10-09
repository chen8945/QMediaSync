package models

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

type embyAutoSTRMFixture struct {
	config   EmbyConfig
	account  Account
	root     SyncPath
	token    EmbyIndexToken
	snapshot EmbyItemSnapshot
	local    string
	reopen   func()
}

func setupEmbyAutoSTRMTest(t *testing.T) *embyAutoSTRMFixture {
	t.Helper()
	return setupEmbyAutoSTRMDatabase(t, setupCleanupSQLite)
}

func setupEmbyAutoSTRMDatabase(t *testing.T, setup cleanupPersistenceDatabase) *embyAutoSTRMFixture {
	t.Helper()
	conn, reopen := setup(t)
	if err := conn.AutoMigrate(&EmbyConfig{}, &Account{}, &SyncPath{}, &SyncFile{}, &SyncRootEvidence{}, &Settings{}, &EmbyLibrarySyncPath{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateEmbyDeletionSchema(conn); err != nil {
		t.Fatal(err)
	}
	f := &embyAutoSTRMFixture{
		config:  EmbyConfig{EmbyUrl: "https://emby.invalid", EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1},
		account: Account{SourceType: SourceType115, UserId: "auto-owner"},
		reopen:  func() { reopen() },
	}
	for _, value := range []any{&f.config, &f.account, &Settings{SettingStrm: SettingStrm{StrmBaseUrl: "https://qms.test"}}} {
		if err := conn.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.root = SyncPath{SourceType: SourceType115, AccountId: f.account.ID, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "movies"}
	if err := conn.Create(&f.root).Error; err != nil {
		t.Fatal(err)
	}
	source := EmbySnapshotSource{ID: "source-101", ItemID: "101", PickCode: "external", Path: "https://qms.test/115/newurl?userid=auto-owner&pickcode=external&token=private"}
	f.snapshot = EmbyItemSnapshot{
		Item: EmbyMediaItem{ItemId: "101", Type: "Episode", SeriesId: "201", SeasonId: "202", ParentIndexNumber: 1,
			Path: "/emby/Show/Season 1/episode.strm", PickCode: source.PickCode, MediaSourcePath: source.Path},
		Sources: []EmbySnapshotSource{source}, MembersComplete: true,
	}
	f.local = filepath.Join(f.root.GetFullLocalPath(), "Show", "Season 1", "episode.strm")
	writeEmbyAutoSTRMTestFile(t, f.local, source.Path+"\n")
	var err error
	f.token, err = BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func writeEmbyAutoSTRMTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEmbyAutoSTRMExternalReferencePersistsWithoutLedger(t *testing.T) {
	testEmbyAutoSTRMExternalReferencePersistsWithoutLedger(t, setupCleanupSQLite)
}

func testEmbyAutoSTRMExternalReferencePersistsWithoutLedger(t *testing.T, setup cleanupPersistenceDatabase) {
	t.Helper()
	for _, variant := range []string{"POSIX", "same_path", "Windows", "UNC"} {
		t.Run(variant, func(t *testing.T) {
			f := setupEmbyAutoSTRMDatabase(t, setup)
			switch variant {
			case "same_path":
				f.snapshot.Item.Path = f.local
			case "Windows":
				f.snapshot.Item.Path = `D:\Emby\Show\Season 1\episode.strm`
			case "UNC":
				f.snapshot.Item.Path = `\\NAS\Media\Show\Season 1\episode.strm`
			}
			reads := forbidEmbyObservationHTTP(t)
			collector := EmbyDirectoryEvidenceCollector{}
			season := 1
			roles := []EmbyMediaDirectory{{ItemID: "202", MediaType: "Season", LocalPath: "/emby/Show/Season 1", SeasonNumber: &season}}
			if err := collector.Enrich(t.Context(), &f.snapshot, roles); err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); err != nil {
				t.Fatal(err)
			}
			evidence, sources, files := readSnapshotSources(t, "101")
			if len(files) != 0 || len(sources) != 1 || sources[0].SyncFileID != 0 {
				t.Fatalf("external STRM invented a ledger binding: sources=%+v files=%+v", sources, files)
			}
			reference := sources[0].Reference
			if reference.LocalPath != f.local || reference.LocalRoot != f.root.GetFullLocalPath() ||
				reference.AutoSyncPathID != f.root.ID || reference.AutoRootIdentity == "" || reference.SyncPathID != 0 {
				t.Fatalf("external path missing or granted structural authority: %+v", reference)
			}
			metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil || len(metadata.DirectoryScopes) != 0 || *reads != 0 {
				t.Fatalf("automatic location created directory authority or HTTP: %+v, reads=%d, err=%v", metadata, *reads, err)
			}
			if strings.Contains(evidence.SourcesJSON, "private") || strings.Contains(evidence.SourcesJSON, "userid") {
				t.Fatal("raw STRM content leaked into the immutable snapshot")
			}
			f.reopen()
			reopened, sources, _ := readSnapshotSources(t, "101")
			if reopened != evidence || sources[0].Reference != reference {
				t.Fatal("reopen changed the frozen automatic path")
			}
			if err := os.Remove(f.local); err != nil {
				t.Fatal(err)
			}
			writeEmbyAutoSTRMTestFile(t, filepath.Join(f.root.GetFullLocalPath(), "episode.strm"), f.snapshot.Sources[0].Path)
			if err := ValidateEmbySTRMReference(t.Context(), &f.config, reference); err != nil {
				t.Fatal("validation must check the frozen location without searching a replacement", err)
			}
			var config EmbyConfig
			if err := db.Db.First(&config).Error; err != nil || len(config.PathMappings) != 0 {
				t.Fatal("automatic location changed manual mappings", err)
			}
		})
	}
}

func TestEmbyAutoSTRMExternalProviders(t *testing.T) {
	for _, provider := range []SourceType{SourceTypeBaiduPan, SourceTypeOpenList} {
		for _, scenario := range []string{"matched", "wrong_object", "wrong_account"} {
			t.Run(string(provider)+"/"+scenario, func(t *testing.T) {
				f := setupEmbyAutoSTRMTest(t)
				f.account.SourceType, f.root.SourceType = provider, provider
				source := &f.snapshot.Sources[0]
				source.Path = "https://qms.test/baidupan/url/episode.mkv?userid=auto-owner&pickcode=123&token=private"
				source.PickCode = "123"
				if provider == SourceTypeOpenList {
					f.account.BaseUrl, f.account.Username = "https://openlist.test/proxy", "auto-owner"
					source.Path = "https://openlist.test/proxy/d/movies/Show/episode.mkv?sign=private"
					source.PickCode = source.Path
				}
				for _, value := range []any{&f.account, &f.root} {
					if err := db.Db.Save(value).Error; err != nil {
						t.Fatal(err)
					}
				}
				f.snapshot.Item.PickCode, f.snapshot.Item.MediaSourcePath = source.PickCode, source.Path
				content := strings.ReplaceAll(source.Path, "private", "rotated")
				switch scenario {
				case "wrong_object":
					content = strings.ReplaceAll(strings.ReplaceAll(content, "pickcode=123", "pickcode=456"), "episode.mkv", "other.mkv")
				case "wrong_account":
					content = strings.ReplaceAll(strings.ReplaceAll(content, "auto-owner", "other-owner"), "openlist.test", "other.test")
				}
				writeEmbyAutoSTRMTestFile(t, f.local, "\ufeff"+content+"\n")
				other := Account{SourceType: provider, UserId: "other-owner", BaseUrl: "https://other.test/proxy", Username: "other-owner"}
				if err := db.Db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				otherRoot := SyncPath{SourceType: provider, AccountId: other.ID, BaseCid: "other-root", LocalPath: t.TempDir(), RemotePath: "movies"}
				if err := db.Db.Create(&otherRoot).Error; err != nil {
					t.Fatal(err)
				}
				writeEmbyAutoSTRMTestFile(t, filepath.Join(otherRoot.GetFullLocalPath(), "Show", "Season 1", "episode.strm"), source.Path)
				reads := forbidEmbyObservationHTTP(t)
				collector := EmbyDirectoryEvidenceCollector{}
				if err := collector.Enrich(t.Context(), &f.snapshot, nil); err != nil {
					t.Fatal(err)
				}
				if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); err != nil {
					t.Fatal(err)
				}
				evidence, sources, files := readSnapshotSources(t, "101")
				if len(sources) != 1 || len(files) != 0 || sources[0].AccountID != f.account.ID || sources[0].SourceType != provider || *reads != 0 {
					t.Fatalf("external source changed account, acquired a ledger or made HTTP requests: %+v files=%v reads=%d", sources, files, *reads)
				}
				reference := sources[0].Reference
				if scenario == "matched" {
					if reference.LocalPath != f.local || reference.AutoSyncPathID != f.root.ID || reference.SyncPathID != 0 {
						t.Fatalf("confirmed external source lost its own root: %+v", reference)
					}
				} else if reference.LocalPath != "" || reference.AutoSyncPathID != 0 {
					t.Fatalf("mismatched local content acquired a reference: %+v", reference)
				}
				if strings.Contains(evidence.SourcesJSON, "private") || strings.Contains(evidence.SourcesJSON, "rotated") {
					t.Fatal("STRM credentials leaked into the snapshot")
				}
			})
		}
	}
}

func TestEmbyAutoSTRMDeletionAfterReopen(t *testing.T) {
	testEmbyAutoSTRMDeletionAfterReopen(t, setupCleanupSQLite)
}

func testEmbyAutoSTRMDeletionAfterReopen(t *testing.T, setup cleanupPersistenceDatabase) {
	t.Helper()
	for _, scenario := range []string{"unchanged", "root_changed"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupEmbyAutoSTRMDatabase(t, setup)
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &f.snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); err != nil {
				t.Fatal(err)
			}
			_, sources, _ := readSnapshotSources(t, "101")
			if len(sources) != 1 || sources[0].Reference.AutoSyncPathID != f.root.ID {
				t.Fatal("automatic reference was not frozen")
			}
			if err := os.Remove(f.local); err != nil {
				t.Fatal(err)
			}
			if _, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: "101", ItemType: "Episode", Source: "official"}); err != nil {
				t.Fatal(err)
			}
			record := webhookModelClaim(t)
			input := webhookModelInput(t, record)
			if len(input.Owners) != 1 {
				t.Fatalf("wrong deletion owner count: %d", len(input.Owners))
			}
			source := sources[0]
			file := EmbyFrozenFile{ExternalSource: true, SourceType: source.SourceType, AccountID: source.AccountID, AccountIdentity: source.AccountIdentity,
				FileID: "1000", ParentID: "0", Path: "/movies/Show/Season 1", FileName: "episode.mkv", PickCode: source.ObjectID,
				SHA1: "original", MTime: 123, SourceID: source.ID, Reference: source.Reference}
			supplement := EmbyDeletionSupplement{Owner: embyOwnerRef(input.Owners[0]), Source: source, File: file, ReadAt: time.Now().UTC()}
			provider := &embyJointTestProvider{deletionMatrix: &deletionMatrix{
				remote: map[string]EmbyRemoteFile{deletionMatrixKey(file.AccountID, file.FileID): embyRemoteFromFrozen(file)}, fail: map[string]bool{},
			}}
			plan, err := BuildEmbyDeletionPlanWithSupplements(t.Context(), input, []EmbyDeletionSupplement{supplement},
				func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, allowEmbyDeleteTest)
			if err != nil || len(plan.Targets) != 1 || len(plan.Issues) != 0 || len(plan.Input.DirectoryScopes) != 0 {
				t.Fatalf("automatic reference did not create one file-only plan: %+v %v", plan, err)
			}
			if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
				t.Fatal(err)
			}
			f.reopen()
			if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			recovered := webhookModelClaim(t)
			var restored EmbyDeletionPlan
			if err := json.Unmarshal([]byte(recovered.PlanJSON), &restored); err != nil {
				t.Fatal(err)
			}
			if recovered.InputJSON != record.InputJSON || recovered.PlanJSON != embyJSON(plan) {
				t.Fatal("reopen changed frozen input or automatic reference")
			}
			if scenario == "root_changed" {
				if err := db.Db.Model(&f.root).Update("local_path", t.TempDir()).Error; err != nil {
					t.Fatal(err)
				}
			}
			results := ExecuteEmbyDeletionBatch(t.Context(), restored, restored.Targets, provider, allowEmbyDeleteTest,
				func([]EmbyDeletionTarget) error { return nil })
			if len(results) != 1 {
				t.Fatalf("unexpected deletion results: %+v", results)
			}
			if scenario == "root_changed" {
				if results[0].Outcome != EmbyDeletionUnresolved || len(provider.batches) != 0 {
					t.Fatalf("reopened plan followed a changed root: %+v batches=%v", results, provider.batches)
				}
				return
			}
			if results[0].Outcome != EmbyDeletionDeleted || len(provider.batches) != 1 || len(provider.batches[0]) != 1 || provider.batches[0][0] != "1000" {
				t.Fatalf("reopened automatic reference deleted the wrong target: %+v batches=%v", results, provider.batches)
			}
			if err := FinalizeEmbyDeletionPlan(t.Context(), restored, results); err != nil {
				t.Fatal(err)
			}
			assertWebhookCount(t, &EmbyMediaItem{}, 0)
			assertWebhookCount(t, &SyncFile{}, 0)
		})
	}
}

func TestEmbyAutoSTRMRejectsUnknownOrConflictingCandidates(t *testing.T) {
	for _, scenario := range []string{"wrong_account", "wrong_object", "wrong_origin", "unknown_account", "duplicate", "outside_root", "different_tree"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupEmbyAutoSTRMTest(t)
			content := f.snapshot.Sources[0].Path
			switch scenario {
			case "wrong_account":
				writeEmbyAutoSTRMTestFile(t, f.local, strings.Replace(content, "auto-owner", "other-owner", 1))
			case "wrong_object":
				writeEmbyAutoSTRMTestFile(t, f.local, strings.Replace(content, "pickcode=external", "pickcode=other", 1))
			case "wrong_origin":
				writeEmbyAutoSTRMTestFile(t, f.local, strings.Replace(content, "qms.test", "foreign.test", 1))
			case "unknown_account":
				f.snapshot.Sources[0].Path = strings.Replace(content, "auto-owner", "unknown", 1)
				writeEmbyAutoSTRMTestFile(t, f.local, f.snapshot.Sources[0].Path)
			case "duplicate":
				writeEmbyAutoSTRMTestFile(t, filepath.Join(f.root.GetFullLocalPath(), "episode.strm"), content)
			case "outside_root", "different_tree":
				if err := os.Remove(f.local); err != nil {
					t.Fatal(err)
				}
				destination := filepath.Join(f.root.GetFullLocalPath(), "Unrelated", "episode.strm")
				if scenario == "outside_root" {
					destination = filepath.Join(f.root.GetFullLocalPath()+"-copy", "Show", "Season 1", "episode.strm")
				}
				writeEmbyAutoSTRMTestFile(t, destination, content)
			}
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &f.snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); err != nil {
				t.Fatal(err)
			}
			_, sources, files := readSnapshotSources(t, "101")
			if len(files) != 0 || len(sources) != 1 || sources[0].Reference.LocalPath != "" || sources[0].Reference.AutoSyncPathID != 0 {
				t.Fatalf("unknown or conflicting STRM acquired a path: %+v", sources)
			}
		})
	}
}

func TestEmbyAutoSTRMPreparationCannotBindChangedObjects(t *testing.T) {
	for _, scenario := range []string{"local_root", "remote_root", "root_generation", "account", "content", "file_moved", "new_ledger", "explicit_mapping"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupEmbyAutoSTRMTest(t)
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &f.snapshot, nil); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "local_root":
				f.root.LocalPath = t.TempDir()
			case "remote_root":
				f.root.BaseCid = "replaced-root"
			case "root_generation":
				f.root.CreatedAt++
			case "account":
				if err := db.Db.Model(&f.account).Update("user_id", "replacement").Error; err != nil {
					t.Fatal(err)
				}
			case "content":
				writeEmbyAutoSTRMTestFile(t, f.local, strings.Replace(f.snapshot.Sources[0].Path, "pickcode=external", "pickcode=new", 1))
			case "file_moved":
				if err := os.Rename(f.local, filepath.Join(f.root.GetFullLocalPath(), "episode.strm")); err != nil {
					t.Fatal(err)
				}
			case "new_ledger":
				file := SyncFile{SyncPathId: f.root.ID, AccountId: f.account.ID, SourceType: SourceType115,
					FileId: "new", PickCode: "external", Path: "movies/Show/Season 1", FileName: "episode.mkv",
					LocalFilePath: f.local, Sha1: "known", MTime: 1, IsVideo: true}
				if err := db.Db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			case "explicit_mapping":
				if err := db.Db.Model(&f.config).Update("path_mappings_json", embyJSON([]EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: t.TempDir()}})).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Db.Save(&f.root).Error; err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
				t.Fatalf("prepared reference accepted %s: %v", scenario, err)
			}
			var count int64
			if err := db.Db.Model(&EmbyItemEvidence{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("stale group left evidence: count=%d err=%v", count, err)
			}
		})
	}
}

func TestEmbyAutoSTRMReferenceChecksOnlyRelevantCurrentIdentity(t *testing.T) {
	for _, scenario := range []string{"cron", "same_principal_token", "root_generation", "account_generation", "explicit_mapping", "unrelated_mapping", "query_error", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupEmbyAutoSTRMTest(t)
			reference := embyAutoSTRMReference(f.snapshot.Item.Path, f.local, f.root, EmbyAccountIdentity(f.account))
			ctx := t.Context()
			queryError := errors.New("injected root read failure")
			switch scenario {
			case "cron":
				f.root.EnableCron = !f.root.EnableCron
			case "same_principal_token":
				f.account.Token = "renewed"
			case "root_generation":
				f.root.CreatedAt++
			case "account_generation":
				f.account.CreatedAt++
			case "explicit_mapping":
				f.config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: f.root.GetFullLocalPath()}}
			case "unrelated_mapping":
				f.config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/unrelated", LocalRoot: t.TempDir()}}
			case "query_error":
				if err := db.Db.Callback().Query().Before("gorm:query").Register("test:auto_reference_query", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_paths" {
						tx.AddError(queryError)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove("test:auto_reference_query") })
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			for _, value := range []any{&f.root, &f.account} {
				if err := db.Db.Save(value).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := ValidateEmbySTRMReference(ctx, &f.config, reference)
			switch scenario {
			case "cron", "same_principal_token", "unrelated_mapping":
				if err != nil {
					t.Fatal("unrelated settings invalidated the original path", err)
				}
			case "query_error":
				if !errors.Is(err, queryError) {
					t.Fatal("database failure was hidden as a mapping conflict", err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("reference query ignored cancellation", err)
				}
			default:
				if !errors.Is(err, ErrEmbySnapshotStale) {
					t.Fatal("changed root or mapping reused old reference", err)
				}
			}
		})
	}
}

func TestEmbyAutoSTRMReadRejectsSymlinksAndNonRegularFiles(t *testing.T) {
	for _, scenario := range []string{"file_symlink", "directory_symlink", "root_symlink", "directory", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			local := filepath.Join(root, "inside", "file.strm")
			if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			writeEmbyAutoSTRMTestFile(t, filepath.Join(outside, "file.strm"), "source")
			switch scenario {
			case "file_symlink":
				if err := os.Symlink(filepath.Join(outside, "file.strm"), local); err != nil {
					t.Skip(err)
				}
			case "directory_symlink":
				if err := os.Remove(filepath.Dir(local)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Dir(local)); err != nil {
					t.Skip(err)
				}
			case "root_symlink":
				root = filepath.Join(root, "link")
				if err := os.Symlink(outside, root); err != nil {
					t.Skip(err)
				}
				local = filepath.Join(root, "file.strm")
			case "directory":
				if err := os.Mkdir(local, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				writeEmbyAutoSTRMTestFile(t, local, strings.Repeat("x", embyAutoSTRMMaxBytes+1))
			}
			relative, err := filepath.Rel(root, local)
			if err != nil {
				t.Fatal(err)
			}
			reference := EmbySTRMReference{EmbyPath: "/emby/" + filepath.ToSlash(relative), LocalRoot: root, LocalPath: local}
			if _, _, err := readEmbyAutoSTRM(reference); err == nil {
				t.Fatal("unreadable or unsafe candidate accepted")
			}
		})
	}
}

func TestEmbyAutoSTRMOldReferenceDoesNotAcquireNewAuthority(t *testing.T) {
	f := setupEmbyAutoSTRMTest(t)
	var reference EmbySTRMReference
	if err := json.Unmarshal([]byte(`{"emby_path":"/emby/Show/Season 1/episode.strm"}`), &reference); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEmbySTRMReference(t.Context(), &f.config, reference); !errors.Is(err, ErrEmbyDeleteUnverified) {
		t.Fatal("old unresolved evidence silently acquired an automatic mapping", err)
	}
}

func TestEmbyAutoSTRMMissingRootCannotBeIgnored(t *testing.T) {
	f := setupEmbyAutoSTRMTest(t)
	missing := SyncPath{SourceType: f.root.SourceType, AccountId: f.root.AccountId,
		BaseCid: "other-root", LocalPath: filepath.Join(t.TempDir(), "unmounted"), RemotePath: "movies"}
	if err := db.Db.Create(&missing).Error; err != nil {
		t.Fatal(err)
	}
	collector := EmbyDirectoryEvidenceCollector{}
	if err := collector.Enrich(t.Context(), &f.snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); err != nil {
		t.Fatal(err)
	}
	_, sources, _ := readSnapshotSources(t, "101")
	if len(sources) != 1 || sources[0].Reference.LocalRoot != "" || sources[0].Reference.Reason != "strm_candidate_unreadable" {
		t.Fatalf("missing mount was treated as absent candidates: %+v", sources)
	}
}

func TestEmbyAutoSTRMExplicitRulesNeverFallBackToAutomatic(t *testing.T) {
	for _, scenario := range []string{"configured", "invalid", "overlapping"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupEmbyAutoSTRMTest(t)
			f.config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: t.TempDir()}}
			switch scenario {
			case "invalid":
				f.config.PathMappings[0].LocalRoot = "relative"
			case "overlapping":
				f.config.PathMappings = append(f.config.PathMappings, EmbyPathMapping{EmbyRoot: "/emby/Show", LocalRoot: t.TempDir()})
			}
			f.config.PathMappingsJSON = embyJSON(f.config.PathMappings)
			if err := db.Db.Save(&f.config).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			f.token, err = BeginEmbyIndexRead("server-a", &f.config)
			if err != nil {
				t.Fatal(err)
			}
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &f.snapshot, nil); err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(f.token, []EmbyItemSnapshot{f.snapshot}); err != nil {
				t.Fatal(err)
			}
			_, sources, _ := readSnapshotSources(t, "101")
			if len(sources) != 1 || sources[0].Reference.AutoSyncPathID != 0 || len(collector.strmRoots) != 0 {
				t.Fatalf("explicit rule triggered automatic discovery: %+v", sources)
			}
			if scenario == "configured" && sources[0].Reference.LocalRoot != f.config.PathMappings[0].LocalRoot ||
				scenario != "configured" && sources[0].Reference.Reason == "" {
				t.Fatalf("explicit mapping or conflict was ignored: %+v", sources[0].Reference)
			}
		})
	}
}

func TestEmbyAutoSTRMManagedSamePathKeepsLegacyGeneration(t *testing.T) {
	previous := db.Db
	t.Cleanup(func() { db.Db = previous })
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	before, _, files := readSnapshotSources(t, "101")
	if len(files) != 1 || files[0].Reference != (EmbySTRMReference{}) {
		t.Fatal("unchanged managed path no longer has its legacy representation")
	}
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", file.SyncPathId).Update("enable_cron", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := readSnapshotSources(t, "101")
	if before != after {
		t.Fatal("ordinary unchanged sync manufactured a new evidence or generation")
	}
}

func TestEmbyAutoSTRMManagedPreparationRejectsRootChanges(t *testing.T) {
	previous := db.Db
	t.Cleanup(func() { db.Db = previous })
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.Type, snapshot.Item.Path = "Video", "/emby/movie.strm"
	collector := EmbyDirectoryEvidenceCollector{}
	if err := collector.Enrich(t.Context(), &snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.cleanupFiles) != 1 || snapshot.cleanupFiles[0].Reference.AutoSyncPathID == 0 {
		t.Fatal("managed reference was not prepared")
	}
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", file.SyncPathId).Update("base_cid", "replacement").Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("prepared path was rebound to a changed root", err)
	}
}
