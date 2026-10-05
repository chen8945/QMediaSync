//go:build integration

package models

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
)

func setupEmbyWebhookPostgres(t *testing.T) (*gorm.DB, string, *EmbyConfig, EmbyIndexToken, EmbyItemSnapshot) {
	t.Helper()
	conn, schema := setupEmbySnapshotPostgres(t)
	config, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
	if err := conn.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	return conn, schema, config, token, snapshot
}

func embyPostgresEnvelope(event, itemID string) EmbyWebhookEnvelope {
	return EmbyWebhookEnvelope{Event: event, ServerID: "server-a", ItemID: itemID, ItemType: "Movie", ItemPath: "/local/media/movie.strm", Source: "official"}
}

func claimEmbyWebhookPostgres(t *testing.T, now int64) EmbyWebhookRecord {
	t.Helper()
	record, err := ClaimEmbyWebhook(t.Context(), now)
	if err != nil || record == nil || record.ClaimToken == "" || record.Status != EmbyWebhookRunning {
		t.Fatalf("claim failed: %+v, %v", record, err)
	}
	return *record
}

func loadEmbyWebhookInputPostgres(t *testing.T, record EmbyWebhookRecord) EmbyDeletionInput {
	t.Helper()
	var input EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func embyWebhookPlanPostgres(t *testing.T, record EmbyWebhookRecord) EmbyDeletionPlan {
	t.Helper()
	input := loadEmbyWebhookInputPostgres(t, record)
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
		t.Fatalf("fixture must identify one physical file: %+v", input)
	}
	file := input.Owners[0].Files[0]
	provider := &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{file.FileID: embyRemoteFromFrozen(file)}}
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil || len(plan.Targets) != 1 {
		t.Fatalf("plan failed: %+v, %v", plan, err)
	}
	return plan
}

func TestEmbyWebhookPostgresReceiptRollsBackDeletionBarrier(t *testing.T) {
	conn, _, _, token, snapshot := setupEmbyWebhookPostgres(t)
	var before EmbyItemState
	if err := conn.Where("item_id = ?", "9101").First(&before).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected receipt failure")
	fail := true
	if err := conn.Callback().Create().Before("gorm:create").Register("test:emby_receipt_failure", func(tx *gorm.DB) {
		if fail && tx.Statement.Table == conn.NamingStrategy.TableName("EmbyWebhookRecord") {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Create().Remove("test:emby_receipt_failure") })
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101")); !errors.Is(err, injected) {
		t.Fatalf("receipt did not return the storage failure: %v", err)
	}
	var after EmbyItemState
	if err := conn.First(&after, before.ID).Error; err != nil || after != before {
		t.Fatalf("failed receipt left a deletion barrier: before=%+v after=%+v, %v", before, after, err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyWebhookRecord{}, 0)
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 1)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatalf("rolled-back receipt invalidated the index token: %v", err)
	}
	fail = false
	record, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101"))
	if err != nil || record.ID == 0 || record.DeletionRevision <= token.Revision || !record.Authorized {
		t.Fatalf("successful receipt did not atomically persist authorization and barrier: %+v, %v", record, err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("committed receipt accepted an old index token: %v", err)
	}
}

func TestEmbyWebhookPostgresConcurrentClaimsKeepDistinctEvents(t *testing.T) {
	_, _, _, _, _ = setupEmbyWebhookPostgres(t)
	first, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.new", "9101"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.new", "9101"))
	if err != nil || second.ID == first.ID {
		t.Fatalf("same-item receipts were collapsed: first=%d second=%d, %v", first.ID, second.ID, err)
	}
	type claimResult struct {
		record *EmbyWebhookRecord
		err    error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var workers sync.WaitGroup
	t.Cleanup(workers.Wait)
	for range 2 {
		workers.Go(func() {
			<-start
			record, err := ClaimEmbyWebhook(t.Context(), time.Now().Unix())
			results <- claimResult{record: record, err: err}
		})
	}
	close(start)
	claimed := map[uint]string{}
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.record != nil {
				if claimed[result.record.ID] != "" || result.record.ClaimToken == "" {
					t.Fatalf("same receipt was claimed twice: %+v", result.record)
				}
				claimed[result.record.ID] = result.record.ClaimToken
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent claims did not finish")
		}
	}
	for len(claimed) < 2 {
		record := claimEmbyWebhookPostgres(t, time.Now().Unix())
		if claimed[record.ID] != "" {
			t.Fatalf("running receipt was claimed again: %d", record.ID)
		}
		claimed[record.ID] = record.ClaimToken
	}
	third, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.new", "9101"))
	if err != nil {
		t.Fatal(err)
	}
	if got := claimEmbyWebhookPostgres(t, time.Now().Unix()); got.ID != third.ID {
		t.Fatalf("event received during processing was lost: got=%d want=%d", got.ID, third.ID)
	}
	if record, err := ClaimEmbyWebhook(t.Context(), time.Now().Unix()); err != nil || record != nil {
		t.Fatalf("running events must not be reclaimed without recovery: %+v, %v", record, err)
	}
}

func TestEmbyWebhookPostgresReopenRetainsPlanResultsAndClaimGuards(t *testing.T) {
	conn, schema, _, _, _ := setupEmbyWebhookPostgres(t)
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101")); err != nil {
		t.Fatal(err)
	}
	oldClaim := claimEmbyWebhookPostgres(t, time.Now().Unix())
	plan := embyWebhookPlanPostgres(t, oldClaim)
	if err := SaveEmbyWebhookPlan(t.Context(), oldClaim, plan); err != nil {
		t.Fatal(err)
	}
	failed := EmbyDeletionResult{Key: plan.Targets[0].Key, Outcome: EmbyDeletionFailed, Reason: "unknown provider result"}
	if err := SaveEmbyWebhookResult(t.Context(), oldClaim, failed); err != nil {
		t.Fatal(err)
	}
	var saved EmbyWebhookRecord
	if err := conn.First(&saved, oldClaim.ID).Error; err != nil {
		t.Fatal(err)
	}
	conn = reopenEmbyWebhookPostgres(t, conn, schema)
	if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := claimEmbyWebhookPostgres(t, time.Now().Unix())
	if current.ID != saved.ID || current.ClaimToken == oldClaim.ClaimToken || current.InputJSON != saved.InputJSON || current.PlanJSON != saved.PlanJSON || current.Authorized != saved.Authorized {
		t.Fatalf("reopen changed frozen evidence or retained an old claim: %+v", current)
	}
	targets, err := LoadEmbyWebhookTargets(t.Context(), current.ID)
	if err != nil || len(targets) != 1 || targets[0].Outcome != EmbyDeletionFailed || targets[0].Reason != failed.Reason || targets[0].Attempts != 1 || targets[0].TargetJSON != embyJSON(plan.Targets[0]) {
		t.Fatalf("recovery lost unfinished target identity/result: %+v, %v", targets, err)
	}
	if err := SaveEmbyWebhookResult(t.Context(), oldClaim, EmbyDeletionResult{Key: failed.Key, Outcome: EmbyDeletionDeleted}); err == nil {
		t.Fatal("pre-restart worker overwrote the reclaimed target")
	}
	if err := FinishEmbyWebhook(t.Context(), oldClaim, EmbyWebhookDone, "", 0, false); err == nil {
		t.Fatal("pre-restart worker finished the new claim")
	}
	mutated := plan
	mutated.Input.Authorized = false
	if err := SaveEmbyWebhookPlan(t.Context(), current, mutated); err == nil {
		t.Fatal("retry replaced the frozen plan with different authorization")
	}
	success := EmbyDeletionResult{Key: failed.Key, Outcome: EmbyDeletionDeleted}
	if err := SaveEmbyWebhookResult(t.Context(), current, success); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), current, EmbyWebhookDone, "", 0, false); err != nil {
		t.Fatal(err)
	}
	duplicate, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101"))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := FindEmbyWebhookSuccess(t.Context(), duplicate, plan.Targets[0])
	if err != nil || prior == nil || prior.Outcome != EmbyDeletionDeleted {
		t.Fatalf("same physical generation did not retain success: %+v, %v", prior, err)
	}
	replacement := plan.Targets[0]
	replacement.File.SHA1 = "new-file-at-same-path"
	replacement.Key = EmbyDeletionFileKey(replacement.File)
	if prior, err := FindEmbyWebhookSuccess(t.Context(), duplicate, replacement); err != nil || prior != nil {
		t.Fatalf("old success leaked into a replacement generation: %+v, %v", prior, err)
	}
	for _, scenario := range []struct {
		name   string
		record EmbyWebhookRecord
	}{
		{name: "different_server", record: EmbyWebhookRecord{ServerID: "another-server", ServerConfigKey: duplicate.ServerConfigKey, Authorized: true}},
		{name: "different_connection", record: EmbyWebhookRecord{ServerID: duplicate.ServerID, ServerConfigKey: "another-connection", Authorized: true}},
		{name: "unauthorized_receipt", record: EmbyWebhookRecord{ServerID: duplicate.ServerID, ServerConfigKey: duplicate.ServerConfigKey, Authorized: false}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if prior, err := FindEmbyWebhookSuccess(t.Context(), scenario.record, plan.Targets[0]); err != nil || prior != nil {
				t.Fatalf("success crossed server/connection/receipt authorization: %+v, %v", prior, err)
			}
		})
	}
	assertEmbyPostgresCount(t, conn, &EmbyWebhookRecord{}, 2)
}

func reopenEmbyWebhookPostgres(t *testing.T, old *gorm.DB, schema string) *gorm.DB {
	t.Helper()
	sqlDB, err := old.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(os.Getenv("QMS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	query.Set("statement_timeout", "15000")
	parsed.RawQuery = query.Encode()
	conn, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	db.Db = conn.WithContext(t.Context())
	return db.Db
}

func TestEmbyWebhookPostgresReceiptAuthorizationNeverIncreases(t *testing.T) {
	conn, _, config, _, _ := setupEmbyWebhookPostgres(t)
	if err := conn.Model(config).Update("enable_delete_netdisk", 0).Error; err != nil {
		t.Fatal(err)
	}
	record, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101"))
	if err != nil || record.Authorized || loadEmbyWebhookInputPostgres(t, record).Authorized {
		t.Fatalf("disabled receipt gained authorization: %+v, %v", record, err)
	}
	if err := conn.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	claimed := claimEmbyWebhookPostgres(t, time.Now().Unix())
	if claimed.Authorized || loadEmbyWebhookInputPostgres(t, claimed).Authorized {
		t.Fatal("enabling later retroactively authorized the queued event")
	}
	future := time.Now().Unix() + 100
	if err := FinishEmbyWebhook(t.Context(), claimed, EmbyWebhookRetry, "busy", future, false); err != nil {
		t.Fatal(err)
	}
	if premature, err := ClaimEmbyWebhook(t.Context(), future-1); err != nil || premature != nil {
		t.Fatalf("retry was claimed before its saved time: %+v, %v", premature, err)
	}
	again := claimEmbyWebhookPostgres(t, future)
	if again.Authorized || again.Attempts != 0 {
		t.Fatalf("busy retry changed authorization or consumed failure budget: %+v", again)
	}
}

func prepareEmbyPostgresObservation(t *testing.T) (*gorm.DB, string, EmbyWebhookRecord, EmbyIndexToken, EmbyItemSnapshot) {
	t.Helper()
	conn, schema, _, token, snapshot := setupEmbyWebhookPostgres(t)
	snapshot.Item.ItemId = "9102"
	snapshot.Sources[0].ItemID = "9102"
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.new", "9102")); err != nil {
		t.Fatal(err)
	}
	record := claimEmbyWebhookPostgres(t, time.Now().Unix())
	return conn, schema, record, token, snapshot
}

func TestEmbyWebhookPostgresObservationAdoptedWithoutIndexMutation(t *testing.T) {
	conn, _, record, token, snapshot := prepareEmbyPostgresObservation(t)
	if err := SaveEmbyObservedEvidence(t.Context(), record, token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyMediaItem{}, 1)
	assertEmbyPostgresCount(t, conn, &EmbyMediaSyncFile{}, 1)
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 2)
	injected := errors.New("injected receipt failure after observation adoption")
	if err := conn.Callback().Create().Before("gorm:create").Register("test:emby_observed_receipt_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == conn.NamingStrategy.TableName("EmbyWebhookRecord") {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Create().Remove("test:emby_observed_receipt_failure") })
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9102")); !errors.Is(err, injected) {
		t.Fatalf("observation adoption did not return receipt failure: %v", err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyItemState{}, 1)
	assertEmbyPostgresCount(t, conn, &EmbyWebhookRecord{}, 1)
	var index EmbyIndexState
	if err := conn.First(&index).Error; err != nil || index.Revision != token.Revision {
		t.Fatalf("failed adoption retained a deletion barrier: %+v, %v", index, err)
	}
	if err := conn.Callback().Create().Remove("test:emby_observed_receipt_failure"); err != nil {
		t.Fatal(err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9102"))
	if err != nil {
		t.Fatal(err)
	}
	input := loadEmbyWebhookInputPostgres(t, deleted)
	if len(input.Owners) != 1 || input.Owners[0].Item.ItemId != "9102" || len(input.Owners[0].Files) != 1 || input.Owners[0].Files[0].FileID != "video-1" {
		t.Fatalf("receipt lost independently observed identity: %+v", input)
	}
	var state EmbyItemState
	if err := conn.Where("server_id = ? AND item_id = ?", token.ServerID, "9102").First(&state).Error; err != nil || !state.Deleted || state.SnapshotID != input.Owners[0].Evidence.ID {
		t.Fatalf("observation adoption and barrier were not atomic: %+v, %v", state, err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyMediaItem{}, 1)
	assertEmbyPostgresCount(t, conn, &EmbyMediaSyncFile{}, 1)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("observation adoption revived the old token: %v", err)
	}
}

func TestEmbyWebhookPostgresNewSnapshotPreventsOldObservationAdoption(t *testing.T) {
	conn, _, record, token, snapshot := prepareEmbyPostgresObservation(t)
	if err := SaveEmbyObservedEvidence(t.Context(), record, token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&SyncFile{}).Where("file_id = ?", "video-1").Updates(map[string]any{"file_id": "new-physical-file", "sha1": "new-content"}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot.Item.Name = "current object"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var current EmbyItemState
	if err := conn.Where("item_id = ?", "9102").First(&current).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9102"))
	if err != nil {
		t.Fatal(err)
	}
	input := loadEmbyWebhookInputPostgres(t, deleted)
	if len(input.Owners) != 1 || input.Owners[0].Evidence.ID != current.SnapshotID || input.Owners[0].Files[0].FileID != "new-physical-file" {
		t.Fatalf("old observation replaced a newer current identity: %+v", input)
	}
}

func TestEmbyWebhookPostgresReceiptBlocksLateObservedEvidence(t *testing.T) {
	conn, schema, observed, token, snapshot := prepareEmbyPostgresObservation(t)
	held := make(chan int, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	if err := conn.Callback().Create().Before("gorm:create").Register("test:hold_emby_receipt", func(tx *gorm.DB) {
		if tx.Statement.Table != conn.NamingStrategy.TableName("EmbyWebhookRecord") {
			return
		}
		var pid int
		if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			tx.AddError(err)
			return
		}
		held <- pid
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Create().Remove("test:hold_emby_receipt") })
	receiptResult := make(chan error, 1)
	receiptFinished := make(chan struct{})
	t.Cleanup(func() { waitEmbyPostgresFinished(t, receiptFinished) })
	go func() {
		defer close(receiptFinished)
		_, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9102"))
		receiptResult <- err
	}()
	var blockerPID int
	select {
	case blockerPID = <-held:
	case err := <-receiptResult:
		t.Fatalf("receipt did not reach the held transaction: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("receipt did not reach persistence")
	}
	observationResult := make(chan error, 1)
	observationFinished := make(chan struct{})
	t.Cleanup(func() { waitEmbyPostgresFinished(t, observationFinished) })
	go func() {
		defer close(observationFinished)
		observationResult <- SaveEmbyObservedEvidence(t.Context(), observed, token, []EmbyItemSnapshot{snapshot})
	}()
	waitEmbyPostgresLock(t, conn, schema, blockerPID, observationResult)
	unblock()
	if err := receiveEmbyPostgresResult(t, receiptResult); err != nil {
		t.Fatal(err)
	}
	if err := receiveEmbyPostgresResult(t, observationResult); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("late observation crossed a committed receipt barrier: %v", err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 1)
	var persisted EmbyWebhookRecord
	if err := conn.First(&persisted, observed.ID).Error; err != nil || persisted.ObservationJSON != "" {
		t.Fatalf("rejected observation left evidence references: %+v, %v", persisted, err)
	}
}

func TestEmbyWebhookPostgresIndependentObservationsSurviveOtherDeletion(t *testing.T) {
	_, _, _, token, snapshot := setupEmbyWebhookPostgres(t)
	for _, id := range []string{"9201", "9202"} {
		if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.new", id)); err != nil {
			t.Fatal(err)
		}
		observed := snapshot
		observed.Item.ItemId = id
		observed.Sources = slices.Clone(snapshot.Sources)
		for i := range observed.Sources {
			observed.Sources[i].ItemID = id
		}
		if err := SaveEmbyObservedEvidence(t.Context(), claimEmbyWebhookPostgres(t, time.Now().Unix()), token, []EmbyItemSnapshot{observed}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"9201", "9202"} {
		record, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", id))
		if err != nil {
			t.Fatal(err)
		}
		input := loadEmbyWebhookInputPostgres(t, record)
		if len(input.Owners) != 1 || input.Owners[0].Item.ItemId != id {
			t.Fatalf("unrelated deletion lost %s observation: %+v", id, input)
		}
	}
}

func TestEmbyWebhookPostgresObservationRejectsSnapshotCommittedDuringRead(t *testing.T) {
	conn, _, record, token, snapshot := prepareEmbyPostgresObservation(t)
	if err := conn.Model(&SyncFile{}).Where("file_id = ?", "video-1").Updates(map[string]any{"file_id": "new-physical-file", "sha1": "new-content"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyObservedEvidence(t.Context(), record, token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("old read bound to state committed after its watermark: %v", err)
	}
	var persisted EmbyWebhookRecord
	if err := conn.First(&persisted, record.ID).Error; err != nil || persisted.ObservationJSON != "" {
		t.Fatalf("stale read persisted references: %+v, %v", persisted, err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9102"))
	if err != nil {
		t.Fatal(err)
	}
	input := loadEmbyWebhookInputPostgres(t, deleted)
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 || input.Owners[0].Files[0].FileID != "new-physical-file" {
		t.Fatalf("new physical generation replaced: %+v", input)
	}
}

func TestEmbyWebhookPostgresObservedSurvivorReleasesOnlyFreshBarrier(t *testing.T) {
	conn, _, config, _, snapshot := setupEmbyWebhookPostgres(t)
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101")); err != nil {
		t.Fatal(err)
	}
	deleted := claimEmbyWebhookPostgres(t, time.Now().Unix())
	if err := FinishEmbyWebhook(t.Context(), deleted, EmbyWebhookUnresolved, "API unavailable", 0, false); err != nil {
		t.Fatal(err)
	}
	if err := conn.First(&deleted, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.modified", "9101")); err != nil {
		t.Fatal(err)
	}
	observed := claimEmbyWebhookPostgres(t, time.Now().Unix())
	beforeGET, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101")); err != nil {
		t.Fatal(err)
	}
	if admitted, err := AdmitEmbyObservedSurvivors(t.Context(), observed, beforeGET, []string{"9101"}); !errors.Is(err, ErrEmbySnapshotStale) || admitted {
		t.Fatalf("stale GET released newer barrier: %v, %v", admitted, err)
	}
	freshGET, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if admitted, err := AdmitEmbyObservedSurvivors(t.Context(), observed, freshGET, []string{"9101"}); err != nil || !admitted {
		t.Fatalf("fresh confirmed survivor remains blocked: %v, %v", admitted, err)
	}
	if err := ApplyEmbySnapshots(freshGET, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("old response applied after admission", err)
	}
	nextGET, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(nextGET, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var unchanged EmbyWebhookRecord
	if err := conn.First(&unchanged, deleted.ID).Error; err != nil || unchanged != deleted {
		t.Fatalf("old delete changed: %+v, %v", unchanged, err)
	}
}

func TestEmbyWebhookPostgresVerifiedGroupAdmissionAtomic(t *testing.T) {
	conn, _, config, _, _ := setupEmbyWebhookPostgres(t)
	if err := conn.Transaction(func(tx *gorm.DB) error {
		_, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"9101", "9102"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	if err := conn.Callback().Update().Before("gorm:update").Register("test:pg_group_admission_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_item_states" {
			updates++
			if updates == 2 {
				tx.AddError(errors.New("second member update failed"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if admitted, err := AdmitEmbyVerifiedSurvivors(t.Context(), token, []string{"9101", "9102"}); err == nil || admitted {
		t.Fatalf("failed group accepted: %v, %v", admitted, err)
	}
	conn.Callback().Update().Remove("test:pg_group_admission_failure")
	remaining, err := LoadEmbyDeletionBarriers(t.Context(), token, 0, 10)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("partial admission survived rollback: %+v, %v", remaining, err)
	}
	if admitted, err := AdmitEmbyVerifiedSurvivors(t.Context(), token, []string{"9101", "9102"}); err != nil || !admitted {
		t.Fatalf("group retry failed: %v, %v", admitted, err)
	}
	fresh, err := BeginEmbyIndexRead("server-a", config)
	if err != nil || fresh.Revision != token.Revision+1 {
		t.Fatalf("group revision=%+v, %v", fresh, err)
	}
	remaining, err = LoadEmbyDeletionBarriers(t.Context(), fresh, 0, 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("verified members remain blocked: %+v, %v", remaining, err)
	}
}
