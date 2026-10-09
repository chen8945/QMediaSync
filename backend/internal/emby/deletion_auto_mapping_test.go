package emby

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

func setupAutomaticExternalDeletionTest(t *testing.T) (*externalDeletionFixture, models.SyncPath, string) {
	t.Helper()
	f := setupExternalDeletionTest(t)
	f.config.PathMappings, f.config.PathMappingsJSON = nil, "[]"
	if err := db.Db.Save(&f.config).Error; err != nil {
		t.Fatal(err)
	}
	var err error
	f.token, err = models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	root := models.SyncPath{SourceType: models.SourceType115, AccountId: f.account.ID,
		LocalPath: f.localRoot, RemotePath: "external", BaseCid: "1"}
	if err := db.Db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := f.snapshot("101", "10")
	local := filepath.Join(root.GetFullLocalPath(), "101.strm")
	if err := os.MkdirAll(root.GetFullLocalPath(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(snapshot.Sources[0].Path), 0600); err != nil {
		t.Fatal(err)
	}
	collector := models.EmbyDirectoryEvidenceCollector{}
	if err := collector.Enrich(t.Context(), &snapshot, nil); err != nil {
		t.Fatal(err)
	}
	f.apply(t, snapshot)
	f.remote.addFile("10")
	return f, root, local
}

func TestWebhookCleanupAutomaticSTRMReferenceUsesFrozenPath(t *testing.T) {
	for _, scenario := range []string{"absent", "original_present", "root_symlink", "root_changed", "live_user"} {
		t.Run(scenario, func(t *testing.T) {
			f, root, local := setupAutomaticExternalDeletionTest(t)
			if scenario != "original_present" {
				if err := os.Remove(local); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "root_symlink":
				if err := os.Rename(root.GetFullLocalPath(), root.GetFullLocalPath()+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), root.GetFullLocalPath()); err != nil {
					t.Skip(err)
				}
			case "root_changed":
				if err := db.Db.Model(&root).Update("local_path", t.TempDir()).Error; err != nil {
					t.Fatal(err)
				}
			case "live_user":
				f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "202", Type: "Movie", Path: "/different/202.strm",
					MediaSources: []embyclientrestgo.MediaSource{{ID: "other-source", ItemID: "202", Path: f.snapshot("101", "10").Sources[0].Path}}}}
			}
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			fresh := readWebhookTest(t, record.ID)
			if scenario != "absent" {
				if len(f.remote.writes)+len(f.remote.lookups)+len(f.remote.details)+len(f.remote.lists) != 0 {
					t.Fatalf("automatic reference skipped a deletion guard: scenario=%s writes=%v", scenario, f.remote.writes)
				}
				if _, exists := f.remote.files["10"]; !exists || fresh.Status == models.EmbyWebhookDone {
					t.Fatal("retained automatic reference was reported deleted")
				}
				return
			}
			assertCleanupWorkerDone(t, record.ID)
			plan := externalDeletionPlan(t, fresh)
			if len(plan.Supplements) != 1 || len(plan.Targets) != 1 || len(plan.Input.DirectoryScopes) != 0 ||
				plan.Supplements[0].File.Reference.LocalPath != local || plan.Supplements[0].File.Reference.SyncPathID != 0 ||
				plan.Supplements[0].File.Reference.AutoSyncPathID != root.ID || plan.Targets[0].Kind != "video" {
				t.Fatalf("automatic reference changed position or granted directory deletion: %+v", plan)
			}
			if fresh.InputJSON != record.InputJSON || len(f.remote.writes) != 1 || len(f.remote.writes[0]) != 1 || f.remote.writes[0][0] != "10" {
				t.Fatal("automatic reference changed immutable input or deleted the wrong target")
			}
		})
	}
}

func TestPrepareEmbyAutomaticReferencePropagatesDatabaseFailure(t *testing.T) {
	f, _, _ := setupAutomaticExternalDeletionTest(t)
	record := f.receive(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected automatic root query failure")
	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:auto_root_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_paths" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("test:auto_root_failure") })
	_, issues, err := prepareEmbyDeletionSupplements(t.Context(), input, models.NewEmbyDeleteProvider,
		func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
			t.Fatal("required root query failure must stop before source verification")
			return nil
		}, false)
	if !errors.Is(err, failure) || len(issues) != 0 {
		t.Fatalf("required database failure became a local mapping issue: issues=%v err=%v", issues, err)
	}
}

func TestEmbyLegacyReferenceRejectsNewExplicitMapping(t *testing.T) {
	local := filepath.Join(t.TempDir(), "old.strm")
	owner := models.EmbyDeletionOwner{Item: models.EmbyMediaItem{Path: local}}
	file := models.EmbyFrozenFile{LocalFilePath: local}
	config := &models.EmbyConfig{}
	if err := verifyEmbyFileReference(t.Context(), config, owner, file); err != nil {
		t.Fatal(err)
	}
	config.PathMappings = []models.EmbyPathMapping{{EmbyRoot: filepath.Dir(local), LocalRoot: t.TempDir()}}
	if err := verifyEmbyFileReference(t.Context(), config, owner, file); !errors.Is(err, models.ErrEmbySnapshotStale) {
		t.Fatal("legacy zero reference ignored a newly configured mapping", err)
	}
}

func TestEmbyAutomaticReferenceRejectsSymlinkRootAtDeletion(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Skip(err)
	}
	original := filepath.Join(root, "missing.strm")
	if err := embyOriginalPathAbsent(root, original, true); err == nil {
		t.Fatal("automatic root symlink became proof that the original file disappeared")
	}
	if err := embyOriginalPathAbsent(root, original, false); err != nil {
		t.Fatal("explicit mapped root lost its existing symlink compatibility", err)
	}
}
