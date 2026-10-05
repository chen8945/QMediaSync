package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// 批量历史只提供原始收件和证据，由生产回填入口建立查询索引。
func seedEmbyObservedHistory(t *testing.T, token EmbyIndexToken, count int, seasonID string) {
	t.Helper()
	var evidence []EmbyItemEvidence
	for i := range count {
		id := fmt.Sprint(1000 + i)
		item := EmbyMediaItem{ItemId: id, ServerId: token.ServerID, Type: "Episode", SeasonId: seasonID, SeriesId: "300", Generation: 1}
		evidence = append(evidence, EmbyItemEvidence{
			ServerID: token.ServerID, ServerConfigKey: token.ServerConfigKey, ConfigKey: token.ConfigKey,
			ItemID: id, Generation: 1, IdentityKey: "identity-" + id,
			ItemJSON: embyJSON(item), SourcesJSON: "[]", FilesJSON: "[]", SidecarsJSON: `{"version":2,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`,
		})
	}
	if count == 0 {
		return
	}
	if err := db.Db.CreateInBatches(&evidence, 50).Error; err != nil {
		t.Fatal(err)
	}
	var records []EmbyWebhookRecord
	for _, entry := range evidence {
		records = append(records, EmbyWebhookRecord{
			Event: "library.modified", ServerID: token.ServerID, ServerConfigKey: token.ServerConfigKey,
			ItemID: entry.ItemID, ItemType: "Episode", ObservedAt: 1, Status: EmbyWebhookDone,
			ObservationJSON: embyJSON([]embyObservedRef{{EvidenceID: entry.ID, Revision: token.Revision}}),
		})
	}
	if err := db.Db.CreateInBatches(&records, 20).Error; err != nil {
		t.Fatal(err)
	}
	if err := RebuildEmbyObservedEvidenceIndex(db.Db); err != nil {
		t.Fatal(err)
	}
}

func TestEmbyObservedIndexHistoryDoesNotScaleReceiptReads(t *testing.T) {
	testEmbyObservedIndexHistoryReads(t, func(t *testing.T) (EmbyIndexToken, SyncFile) {
		_, token, file := setupEmbyWebhookModelTest(t)
		return token, file
	})
}

func testEmbyObservedIndexHistoryReads(t *testing.T, setup func(*testing.T) (EmbyIndexToken, SyncFile)) {
	var baselineQueries int
	for _, history := range []int{0, 500} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			token, file := setup(t)
			saveEmbyMembershipObservation(t, token, snapshotForFile(file))
			seedEmbyObservedHistory(t, token, history, "999")
			queries, observedRows, evidenceRows := 0, int64(0), int64(0)
			if err := db.Db.Callback().Query().After("gorm:query").Register("test:observation_query_count", func(tx *gorm.DB) {
				queries++
				switch tx.Statement.Table {
				case "emby_webhook_records":
					if strings.Contains(tx.Statement.SQL.String(), "observation_json") {
						observedRows += tx.RowsAffected
					}
				case "emby_item_evidences":
					evidenceRows += tx.RowsAffected
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Db.Callback().Query().Remove("test:observation_query_count") })
			record, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
			if err != nil {
				t.Fatal(err)
			}
			if input := webhookModelInput(t, record); len(input.Owners) != 1 || input.Owners[0].Item.ItemId != "101" {
				t.Fatalf("unrelated history changed receipt: %+v", input.Owners)
			}
			if history == 0 {
				baselineQueries = queries
			}
			if queries != baselineQueries || observedRows != 1 || evidenceRows != 1 {
				t.Fatalf("history=%d queries=%d baseline=%d observed rows=%d evidence rows=%d", history, queries, baselineQueries, observedRows, evidenceRows)
			}
			t.Logf("history=%d receipt SELECTs=%d observation rows=%d evidence rows=%d", history, queries, observedRows, evidenceRows)
		})
	}
}

func TestEmbyObservedIndexBatchesAndRebuild(t *testing.T) {
	for _, count := range []int{500, 501} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			_, token, _ := setupEmbyWebhookModelTest(t)
			assertEmbyObservedIndexBatches(t, token, count)
		})
	}
}

func assertEmbyObservedIndexBatches(t *testing.T, token EmbyIndexToken, count int) {
	t.Helper()
	seedEmbyObservedHistory(t, token, count, "200")
	// 源收件保持一份额外的、同 EvidenceID 但不同预期状态的引用，回填不能按证据合并。
	var record EmbyWebhookRecord
	if err := db.Db.Order("id").First(&record).Error; err != nil {
		t.Fatal(err)
	}
	var refs []embyObservedRef
	if err := json.Unmarshal([]byte(record.ObservationJSON), &refs); err != nil {
		t.Fatal(err)
	}
	invalid := refs[0]
	invalid.StateID = 99999
	refs = append(refs, invalid)
	if err := db.Db.Model(&record).Update("observation_json", embyJSON(refs)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Migrator().DropTable(&EmbyObservedEvidenceIndex{}); err != nil {
		t.Fatal(err)
	}
	if err := RebuildEmbyObservedEvidenceIndex(db.Db); err != nil {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyObservedEvidenceIndex{}, int64(count+1))
	batchQueries := map[string]int{}
	if err := db.Db.Callback().Query().After("gorm:query").Register("test:observation_batches", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_webhook_records" || tx.Statement.Table == "emby_item_evidences" || tx.Statement.Table == "emby_item_states" {
			batchQueries[tx.Statement.Table]++
			if len(tx.Statement.Vars) > embyObservationBatchSize+1 {
				t.Errorf("unbounded IN query: table=%s vars=%d", tx.Statement.Table, len(tx.Statement.Vars))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("test:observation_batches") })
	batch, err := loadEmbyObservedBatchTx(db.Db, EmbyWebhookRecord{ServerID: token.ServerID, ServerConfigKey: token.ServerConfigKey, ItemID: "200", ItemType: "Season"})
	if err != nil || len(batch.refs) != count+1 || len(batch.evidence) != count {
		t.Fatalf("count=%d refs=%d evidence=%d err=%v", count, len(batch.refs), len(batch.evidence), err)
	}
	if !slices.Contains(batch.refs, refs[0]) || !slices.Contains(batch.refs, invalid) {
		t.Fatal("rebuild collapsed distinct expected state tuples")
	}
	wantQueries := (count + embyObservationBatchSize - 1) / embyObservationBatchSize
	for table, queries := range batchQueries {
		if queries != wantQueries {
			t.Errorf("table=%s queries=%d want=%d", table, queries, wantQueries)
		}
	}
}

func TestEmbyObservedIndexLatestInvalidDoesNotHideValid(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	valid := snapshotForFile(file)
	valid.Item.Type, valid.Item.SeasonId = "Episode", "200"
	older := saveEmbyMembershipObservation(t, token, valid)
	newer := valid
	newer.Item.SeasonId = "201"
	record := saveEmbyMembershipObservation(t, token, newer)
	var refs []embyObservedRef
	if err := db.Db.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(record.ObservationJSON), &refs); err != nil {
		t.Fatal(err)
	}
	refs[0].StateRevision++
	if err := db.Db.Model(&record).Update("observation_json", embyJSON(refs)).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.deleted", ServerID: token.ServerID, ItemID: "200", ItemType: "Season", Source: "official"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&older, older.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(older.ObservationJSON), &refs); err != nil {
		t.Fatal(err)
	}
	input := webhookModelInput(t, deleted)
	if len(input.Owners) != 1 || input.Owners[0].Evidence.ID != refs[0].EvidenceID {
		t.Fatalf("latest invalid observation hid older valid observation: %+v", input.Owners)
	}
}

func TestEmbyObservedIndexSaveRollsBack(t *testing.T) {
	for _, stage := range []string{"index", "record"} {
		t.Run(stage, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			snapshot := snapshotForFile(file)
			record := saveEmbyMembershipObservation(t, token, snapshot)
			var beforeRecord EmbyWebhookRecord
			var beforeIndex []EmbyObservedEvidenceIndex
			if err := db.Db.First(&beforeRecord, record.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Find(&beforeIndex).Error; err != nil {
				t.Fatal(err)
			}
			injected := errors.New("observation save failure")
			callback := func(tx *gorm.DB) {
				if stage == "index" && tx.Statement.Table == "emby_observed_evidence_indices" || stage == "record" && tx.Statement.Table == "emby_webhook_records" {
					tx.AddError(injected)
				}
			}
			if err := db.Db.Callback().Create().Before("gorm:create").Register("test:index_save", callback); err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Callback().Update().Before("gorm:update").Register("test:index_save", callback); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Db.Callback().Create().Remove("test:index_save")
				db.Db.Callback().Update().Remove("test:index_save")
			})
			if err := SaveEmbyObservedEvidence(t.Context(), record, token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, injected) {
				t.Fatalf("save err=%v", err)
			}
			var afterRecord EmbyWebhookRecord
			var afterIndex []EmbyObservedEvidenceIndex
			if err := db.Db.First(&afterRecord, record.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Find(&afterIndex).Error; err != nil {
				t.Fatal(err)
			}
			if beforeRecord != afterRecord || !reflect.DeepEqual(beforeIndex, afterIndex) {
				t.Fatal("failed save changed original refs or derived index")
			}
			assertWebhookCount(t, &EmbyItemEvidence{}, 1)
		})
	}
}

func TestEmbyObservedIndexRejectsDamagedAuthority(t *testing.T) {
	for _, scenario := range []string{"missing evidence", "wrong server", "wrong connection", "item mismatch", "invalid item", "ref mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			record := saveEmbyMembershipObservation(t, token, snapshotForFile(file))
			var evidence EmbyItemEvidence
			if err := db.Db.First(&evidence).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			switch scenario {
			case "missing evidence":
				err = db.Db.Delete(&evidence).Error
			case "wrong server":
				err = db.Db.Model(&evidence).Update("server_id", "other-server").Error
			case "wrong connection":
				err = db.Db.Model(&evidence).Update("server_config_key", "other-connection").Error
			case "item mismatch":
				err = db.Db.Model(&evidence).Update("item_json", `{"item_id":"102","server_id":"server-a","generation":1}`).Error
			case "invalid item":
				err = db.Db.Model(&evidence).Update("item_json", `{`).Error
			case "ref mismatch":
				err = db.Db.Model(&record).Update("observation_json", `[{"evidence_id":999999}]`).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err == nil {
				t.Fatal("damaged observation authorized receipt")
			}
			assertWebhookCount(t, &EmbyItemState{}, 0)
		})
	}
}

func TestEmbyObservedIndexRepairPublishesCompleteIndex(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			saveEmbyMembershipObservation(t, token, snapshotForFile(file))
			if err := db.Db.Migrator().DropTable(&EmbyObservedEvidenceIndex{}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("repair index failure")
			if fail {
				if err := db.Db.Callback().Create().Before("gorm:create").Register("test:observation_repair", func(tx *gorm.DB) {
					if tx.Statement.Table == "emby_observed_evidence_indices" {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Create().Remove("test:observation_repair") })
			}
			err := BatchCreateTable()
			if (err != nil) != fail || fail && !errors.Is(err, injected) {
				t.Fatalf("repair fail=%v err=%v", fail, err)
			}
			if fail {
				if db.Db.Migrator().HasTable(&EmbyObservedEvidenceIndex{}) {
					t.Fatal("repair exposed an empty or partial index after failed backfill")
				}
				return
			}
			assertWebhookCount(t, &EmbyObservedEvidenceIndex{}, 1)
			if err := BatchCreateTable(); err != nil {
				t.Fatal(err)
			}
			assertWebhookCount(t, &EmbyObservedEvidenceIndex{}, 1)
		})
	}
}
