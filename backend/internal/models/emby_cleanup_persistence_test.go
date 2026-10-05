package models

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

type cleanupPersistenceDatabase func(*testing.T) (*gorm.DB, func() *gorm.DB)

func setupCleanupSQLite(t *testing.T) (*gorm.DB, func() *gorm.DB) {
	t.Helper()
	previousDB, previousLogger := db.Db, helpers.AppLogger
	databasePath := filepath.Join(t.TempDir(), "cleanup.db")
	var current *gorm.DB
	open := func() *gorm.DB {
		t.Helper()
		if current != nil {
			connection, err := current.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		current, err = gorm.Open(sqlite.Open(databasePath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		connection, err := current.DB()
		if err != nil {
			t.Fatal(err)
		}
		connection.SetMaxOpenConns(1)
		db.Db = current
		return current
	}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		if current != nil {
			connection, err := current.DB()
			if err == nil {
				err = connection.Close()
			}
			if err != nil {
				t.Error(err)
			}
		}
		db.Db, helpers.AppLogger = previousDB, previousLogger
	})
	return open(), open
}

func seedCleanupPersistence(t *testing.T, conn *gorm.DB) (EmbyIndexToken, EmbyItemSnapshot) {
	t.Helper()
	if err := conn.AutoMigrate(&EmbyConfig{}, &Account{}, &SyncPath{}, &SyncFile{}, &EmbyLibrarySyncPath{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateEmbyDeletionSchema(conn); err != nil {
		t.Fatal(err)
	}
	config := EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "cleanup-test-key", SyncEnabled: 1, EnableDeleteNetdisk: 1}
	account := Account{SourceType: SourceType115, UserId: "cleanup-test-user"}
	for _, value := range []any{&config, &account} {
		if err := conn.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	root := SyncPath{SourceType: SourceType115, AccountId: account.ID, BaseCid: "root", LocalPath: "/strm", RemotePath: "/movies"}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	file := SyncFile{SourceType: SourceType115, AccountId: account.ID, SyncPathId: root.ID, FileId: "video", ParentId: "movie-dir", FileName: "movie.mkv", Path: "/movies/Feature", LocalFilePath: root.GetFullLocalPath() + "/Feature/movie.strm", PickCode: "cleanup-pick", Sha1: "video-generation", FileSize: 42, MTime: 123, IsVideo: true}
	if err := conn.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	sidecar := SyncFile{SourceType: file.SourceType, AccountId: file.AccountId, SyncPathId: file.SyncPathId, FileId: "sidecar", ParentId: file.ParentId, FileName: "movie.nfo", Path: file.Path, LocalFilePath: root.GetFullLocalPath() + "/Feature/movie.nfo", Sha1: "sidecar-generation", FileSize: 15, MTime: 124, IsMeta: true}
	if err := conn.Create(&sidecar).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", &config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.cleanupFiles, err = resolveEmbySnapshotFiles(conn, snapshot)
	if err != nil || len(snapshot.cleanupFiles) != 1 || snapshot.cleanupFiles[0].Reason != "" {
		t.Fatalf("invalid physical fixture: %+v, %v", snapshot.cleanupFiles, err)
	}
	directory := EmbyFrozenFile{SourceType: file.SourceType, AccountID: account.ID, AccountIdentity: EmbyAccountIdentity(account), SyncPathID: root.ID, LocalRoot: root.GetFullLocalPath(), RemoteRoot: root.RemotePath, RootFileID: root.BaseCid, FileID: "movie-dir", ParentID: root.BaseCid, Path: root.RemotePath, FileName: "Feature", LocalFilePath: root.GetFullLocalPath() + "/Feature"}
	snapshot.DirectoryScopes = []EmbyDirectoryScope{{Root: directory, MediaType: "Movie", ItemID: snapshot.Item.ItemId, LocalPath: directory.LocalFilePath, EvidenceKind: "emby_physical_ancestor", Ancestors: []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: root.RemotePath}}}}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	return token, snapshot
}

func cleanupPersistenceReceipt(t *testing.T, snapshot EmbyItemSnapshot) EmbyWebhookRecord {
	t.Helper()
	_, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: snapshot.Item.ItemId, ItemType: snapshot.Item.Type, ItemPath: snapshot.Item.Path, Source: "official"})
	if err != nil {
		t.Fatal(err)
	}
	return webhookModelClaim(t)
}

// 持久化测试在收件授权上构造固定计划，不调用远端删除，也不重复测试目录准入算法。
func cleanupPersistencePlan(t *testing.T, record EmbyWebhookRecord, directory bool) EmbyDeletionPlan {
	t.Helper()
	input := webhookModelInput(t, record)
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 || len(input.Sidecars) != 1 {
		t.Fatalf("receipt lost video or historical sidecar: %+v", input)
	}
	owner := input.Owners[0]
	refs := []EmbyDeletionOwnerRef{{ItemID: owner.Item.ItemId, SnapshotID: owner.Evidence.ID, Generation: owner.Evidence.Generation}}
	plan := EmbyDeletionPlan{Input: input}
	for i, file := range []EmbyFrozenFile{owner.Files[0], input.Sidecars[0]} {
		kind := "video"
		if i == 1 {
			kind = "sidecar"
		}
		plan.Targets = append(plan.Targets, EmbyDeletionTarget{Key: EmbyDeletionFileKey(file), Kind: kind, File: file, Owners: refs})
	}
	if directory {
		if len(input.DirectoryScopes) != 1 {
			t.Fatalf("receipt lost historical directory: %+v", input.DirectoryScopes)
		}
		scope := input.DirectoryScopes[0]
		target := EmbyDeletionTarget{Key: "directory:" + EmbyDirectoryScopeKey(scope), Kind: "directory", Directory: &scope, File: scope.Root, Owners: refs}
		for i := range plan.Targets {
			target.CoveredKeys = append(target.CoveredKeys, plan.Targets[i].Key)
			plan.Targets[i].CoveredBy = target.Key
		}
		plan.Targets = append([]EmbyDeletionTarget{target}, plan.Targets...)
	}
	return plan
}

func TestEmbyCleanupSQLitePersistence(t *testing.T) {
	testCleanupPersistence(t, setupCleanupSQLite)
}

func testCleanupPersistence(t *testing.T, setup cleanupPersistenceDatabase) {
	t.Run("directory_completion_survives_reopen", func(t *testing.T) {
		conn, reopen := setup(t)
		_, snapshot := seedCleanupPersistence(t, conn)
		record := cleanupPersistenceReceipt(t, snapshot)
		plan := cleanupPersistencePlan(t, record, true)
		if !plan.Input.CleanupPolicy.AllowsDirectoryContent() || !plan.Input.CleanupPolicy.AllowsJointBatch() {
			t.Fatal("new receipt did not freeze current policy")
		}
		if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
			t.Fatal(err)
		}
		attempt, err := BeginEmbyWebhookAttempt(t.Context(), record, plan.Targets[:1])
		if err != nil || attempt == "" {
			t.Fatalf("attempt: %q %v", attempt, err)
		}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionDeleted, Reason: "confirmed_directory_operation"}}); err != nil {
			t.Fatal(err)
		}
		var before EmbyWebhookRecord
		if err := conn.First(&before, record.ID).Error; err != nil {
			t.Fatal(err)
		}
		targetsBefore, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
		if err != nil {
			t.Fatal(err)
		}
		conn = reopen()
		if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
			t.Fatal(err)
		}
		after := webhookModelClaim(t)
		if after.InputJSON != before.InputJSON || after.PlanJSON != before.PlanJSON || after.ClaimToken == record.ClaimToken {
			t.Fatal("reopen changed frozen authority or reused a stale claim")
		}
		targetsAfter, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
		if err != nil || !reflect.DeepEqual(targetsBefore, targetsAfter) || len(targetsAfter) != 3 {
			t.Fatalf("directory operation/results did not survive reopen: %+v %v", targetsAfter, err)
		}
		for _, target := range targetsAfter {
			if target.Outcome != EmbyDeletionDeleted {
				t.Fatalf("known member did not complete: %+v", target)
			}
			if target.TargetKey != plan.Targets[0].Key && target.Reason != "covered_by_directory:"+plan.Targets[0].Key {
				t.Fatalf("covered member lost its operation reference: %+v", target)
			}
		}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionFailed}}); !errors.Is(err, ErrEmbyWebhookClaimLost) {
			t.Fatalf("stale worker altered persisted completion: %v", err)
		}
	})

	t.Run("absent_directory_does_not_complete_members", func(t *testing.T) {
		conn, reopen := setup(t)
		_, snapshot := seedCleanupPersistence(t, conn)
		record := cleanupPersistenceReceipt(t, snapshot)
		plan := cleanupPersistencePlan(t, record, true)
		if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
			t.Fatal(err)
		}
		attempt, err := BeginEmbyWebhookAttempt(t.Context(), record, plan.Targets[:1])
		if err != nil {
			t.Fatal(err)
		}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionAlreadyAbsent}}); err != nil {
			t.Fatal(err)
		}
		reopen()
		targets, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
		if err != nil || len(targets) != 3 {
			t.Fatalf("targets: %+v %v", targets, err)
		}
		for _, target := range targets {
			if target.TargetKey == plan.Targets[0].Key {
				if target.Outcome != EmbyDeletionAlreadyAbsent {
					t.Fatal("original root absence was not saved")
				}
			} else if target.Outcome != "" || target.Reason != "" {
				t.Fatalf("root absence fabricated member completion: %+v", target)
			}
		}
	})

	t.Run("joint_partial_result_survives_reopen", func(t *testing.T) {
		conn, reopen := setup(t)
		_, snapshot := seedCleanupPersistence(t, conn)
		record := cleanupPersistenceReceipt(t, snapshot)
		plan := cleanupPersistencePlan(t, record, false)
		if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
			t.Fatal(err)
		}
		attempt, err := BeginEmbyWebhookAttempt(t.Context(), record, plan.Targets)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := BeginEmbyWebhookAttempt(t.Context(), record, plan.Targets)
		if err != nil || repeated != attempt {
			t.Fatalf("repeated send guard registered another attempt: %q %v", repeated, err)
		}
		results := []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionFailed, Reason: "provider result uncertain"}, {Key: plan.Targets[1].Key, Outcome: EmbyDeletionDeleted}}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, "unrelated-attempt", results); err == nil {
			t.Fatal("unknown attempt ID wrote batch results")
		}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, results); err != nil {
			t.Fatal(err)
		}
		reopen()
		if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
			t.Fatal(err)
		}
		recovered := webhookModelClaim(t)
		targets, err := LoadEmbyWebhookTargets(t.Context(), recovered.ID)
		if err != nil || len(targets) != 2 {
			t.Fatalf("targets: %+v %v", targets, err)
		}
		for i, target := range targets {
			var execution EmbyWebhookTargetExecution
			if err := json.Unmarshal([]byte(target.TargetJSON), &execution); err != nil {
				t.Fatal(err)
			}
			if target.Outcome != results[i].Outcome || target.Attempts != 1 || len(execution.ExecutionAttempts) != 1 {
				t.Fatalf("lost individual outcome or counted attempt twice: %+v", target)
			}
			saved := execution.ExecutionAttempts[0]
			if saved.ID != attempt || saved.ClaimToken != record.ClaimToken || saved.StartedAt == 0 || saved.FinishedAt < saved.StartedAt {
				t.Fatalf("incomplete persisted batch attempt: %+v", saved)
			}
			if i == 0 && (len(saved.TargetKeys) != 2 || !slices.Contains(saved.TargetKeys, plan.Targets[0].Key) || !slices.Contains(saved.TargetKeys, plan.Targets[1].Key)) {
				t.Fatalf("batch leader lost the original submitted members: %+v", saved)
			}
		}
		if _, err := BeginEmbyWebhookAttempt(t.Context(), recovered, plan.Targets); err == nil {
			t.Fatal("completed sidecar accepted for whole-batch replay")
		}
		nextAttempt, err := BeginEmbyWebhookAttempt(t.Context(), recovered, plan.Targets[:1])
		if err != nil || nextAttempt == attempt {
			t.Fatalf("unfinished member did not get distinct recovery attempt: %q %v", nextAttempt, err)
		}
		targets, err = LoadEmbyWebhookTargets(t.Context(), recovered.ID)
		if err != nil || targets[0].Attempts != 2 || targets[1].Attempts != 1 {
			t.Fatalf("retry changed completed member attempts: %+v %v", targets, err)
		}
	})

	t.Run("directory_result_transaction_rollback", func(t *testing.T) {
		conn, _ := setup(t)
		_, snapshot := seedCleanupPersistence(t, conn)
		record := cleanupPersistenceReceipt(t, snapshot)
		plan := cleanupPersistencePlan(t, record, true)
		if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
			t.Fatal(err)
		}
		attempt, err := BeginEmbyWebhookAttempt(t.Context(), record, plan.Targets[:1])
		if err != nil {
			t.Fatal(err)
		}
		before, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("covered result write failed")
		writes := 0
		const callback = "test:cleanup_atomic_results"
		if err := conn.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "emby_webhook_targets" {
				writes++
				if writes == 2 {
					tx.AddError(injected)
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Callback().Update().Remove(callback) })
		result := []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionDeleted, Reason: "confirmed_directory_operation"}}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, result); !errors.Is(err, injected) {
			t.Fatalf("result failure was not propagated: %v", err)
		}
		after, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("partial directory success survived rollback: %+v %v", after, err)
		}
		if err := conn.Callback().Update().Remove(callback); err != nil {
			t.Fatal(err)
		}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, result); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unsupported_metadata_is_rejected_without_rewriting", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{name: "empty_array", raw: `[]`},
			{name: "file_array", raw: `[{"file_id":"sidecar-1","account_id":1}]`},
			{name: "old_envelope", raw: `{"version":1,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				conn, reopen := setup(t)
				_, snapshot := seedCleanupPersistence(t, conn)
				var evidence EmbyItemEvidence
				if err := conn.First(&evidence).Error; err != nil {
					t.Fatal(err)
				}
				if err := conn.Model(&evidence).Update("sidecars_json", tc.raw).Error; err != nil {
					t.Fatal(err)
				}
				var before EmbyItemState
				if err := conn.First(&before).Error; err != nil {
					t.Fatal(err)
				}
				_, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: snapshot.Item.ItemId, ItemType: snapshot.Item.Type, ItemPath: snapshot.Item.Path, Source: "official"})
				if err == nil {
					t.Fatal("unsupported metadata admitted deletion work")
				}
				conn = reopen()
				var restored EmbyItemEvidence
				if err := conn.First(&restored, evidence.ID).Error; err != nil || restored != evidence {
					t.Fatalf("rejected metadata changed after reopen: %v", err)
				}
				var after EmbyItemState
				if err := conn.First(&after, before.ID).Error; err != nil || after != before {
					t.Fatalf("rejected receipt changed frozen generation: %v", err)
				}
				assertWebhookCount(t, &EmbyWebhookRecord{}, 0)
				assertWebhookCount(t, &EmbyWebhookTarget{}, 0)
			})
		}
	})

	t.Run("oversized_plan_is_atomic", func(t *testing.T) {
		conn, _ := setup(t)
		_, snapshot := seedCleanupPersistence(t, conn)
		record := cleanupPersistenceReceipt(t, snapshot)
		plan := cleanupPersistencePlan(t, record, true)
		plan.Targets[0].Reason = strings.Repeat("x", EmbyWebhookMaxStoredBytes)
		if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err == nil {
			t.Fatal("8 MiB directory plan cap was bypassed")
		}
		var persisted EmbyWebhookRecord
		if err := conn.First(&persisted, record.ID).Error; err != nil || persisted.PlanJSON != "" {
			t.Fatalf("oversized plan was partially written: %v", err)
		}
		targets, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
		if err != nil || len(targets) != 0 {
			t.Fatalf("oversized plan left executable targets: %d %v", len(targets), err)
		}
	})

	t.Run("oversized_metadata_is_atomic", func(t *testing.T) {
		conn, _ := setup(t)
		token, snapshot := seedCleanupPersistence(t, conn)
		var before EmbyItemState
		if err := conn.First(&before).Error; err != nil {
			t.Fatal(err)
		}
		scope := snapshot.DirectoryScopes[0]
		count := EmbyWebhookMaxStoredBytes/len(embyJSON(scope)) + 1
		snapshot.DirectoryScopes = make([]EmbyDirectoryScope, count)
		for i := range snapshot.DirectoryScopes {
			snapshot.DirectoryScopes[i] = scope
		}
		snapshot.Item.Name = "oversized metadata must not replace history"
		if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err == nil {
			t.Fatal("8 MiB metadata envelope cap was bypassed")
		}
		var after EmbyItemState
		if err := conn.First(&after).Error; err != nil || after != before {
			t.Fatalf("oversized metadata changed item state: %+v %v", after, err)
		}
		assertWebhookCount(t, &EmbyItemEvidence{}, 1)
	})
}
