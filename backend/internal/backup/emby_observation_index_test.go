package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func setupEmbyObservationBackup(t *testing.T) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence) {
	t.Helper()
	return seedEmbyObservationBackup(t, setupBackupTest(t))
}

func seedEmbyObservationBackup(t *testing.T, conn *gorm.DB) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
		t.Fatal(err)
	}
	// 故意把派生表排在原表前，恢复只能在全部原表导入后重建一次。
	models.AllTables = []any{
		&models.EmbyItemMembership{}, &models.EmbyObservedEvidenceIndex{}, &models.EmbyIndexState{},
		&models.EmbyItemState{}, &models.EmbyWebhookRecord{}, &models.EmbyItemEvidence{}, &backupTestItem{},
	}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	state := models.EmbyIndexState{BaseModel: models.BaseModel{ID: 1}, ServerID: "server-a", ServerConfigKey: "connection", Revision: 5}
	if err := conn.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	var evidence []models.EmbyItemEvidence
	for i, season := range []string{"200", "201"} {
		item := models.EmbyMediaItem{ItemId: "101", ServerId: "server-a", Type: "Episode", Generation: 1, SeasonId: season, SeriesId: "300"}
		evidence = append(evidence, models.EmbyItemEvidence{
			BaseModel: models.BaseModel{ID: uint(11 + i)}, ServerID: "server-a", ServerConfigKey: "connection", ConfigKey: "different-sync-scope",
			ItemID: "101", Generation: 1, ItemJSON: cleanupBackupJSON(t, item), SourcesJSON: "[]", FilesJSON: "[]", SidecarsJSON: "[]",
		})
	}
	if err := conn.Create(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	// 当前媒体索引表完全不存在，成员关系只能来自状态引用的最新证据。
	itemState := models.EmbyItemState{ServerID: "server-a", ItemID: "101", SnapshotID: evidence[1].ID, Generation: 1, Revision: 8, Deleted: true}
	if err := conn.Create(&itemState).Error; err != nil {
		t.Fatal(err)
	}
	record := models.EmbyWebhookRecord{
		BaseModel: models.BaseModel{ID: 21}, ServerID: "server-a", ServerConfigKey: "connection",
		Event: "library.modified", ItemID: "101", ItemType: "Episode", ObservedAt: 1, Status: models.EmbyWebhookDone,
		ObservationJSON: `[{"evidence_id":11,"expected_state_revision":7},{"evidence_id":12,"expected_state_revision":8}]`,
	}
	if err := conn.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := models.RebuildEmbyObservedEvidenceIndex(conn); err != nil {
		t.Fatal(err)
	}
	if err := models.RebuildEmbyItemMembership(conn); err != nil {
		t.Fatal(err)
	}
	return conn, record, evidence
}

func TestRestoreEmbyObservationIndexRebuildsFromOriginals(t *testing.T) {
	testRestoreEmbyObservationIndexRebuildsFromOriginals(t, setupEmbyObservationBackup)
}

func testRestoreEmbyObservationIndexRebuildsFromOriginals(t *testing.T, setup func(*testing.T) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence)) {
	for _, scenario := range []string{"roundtrip", "old archive without index", "old archive without observations"} {
		t.Run(scenario, func(t *testing.T) {
			conn, record, evidence := setup(t)
			var stateBefore models.EmbyItemState
			if err := conn.First(&stateBefore).Error; err != nil {
				t.Fatal(err)
			}
			var archivePath string
			if scenario == "roundtrip" {
				// 归档故意保存过期成员投影，恢复不能采用它。
				if err := conn.Model(&models.EmbyItemMembership{}).Where("item_id = ?", "101").Updates(map[string]any{"snapshot_id": evidence[0].ID, "season_id": "200"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := Backup(models.BackupTypeManual, "observation index"); err != nil {
					t.Fatal(err)
				}
				var archive models.BackupRecord
				if err := conn.Last(&archive).Error; err != nil {
					t.Fatal(err)
				}
				archivePath = archive.FilePath
			} else {
				files := map[string]string{"backupTestItem.json": "{\"ID\":1,\"Name\":\"restored\"}\n"}
				if scenario == "old archive without index" {
					files["EmbyWebhookRecord.json"] = cleanupBackupJSON(t, record) + "\n"
					files["EmbyItemEvidence.json"] = cleanupBackupJSON(t, evidence[0]) + "\n" + cleanupBackupJSON(t, evidence[1]) + "\n"
				}
				archivePath = writeBackupArchive(t, files, zip.Deflate)
			}
			stale := models.EmbyObservedEvidenceIndex{RecordID: 999, EvidenceID: 999, ServerID: "server-a", ServerConfigKey: "connection", ItemID: "999", SeasonID: "200"}
			if err := conn.Create(&stale).Error; err != nil {
				t.Fatal(err)
			}
			staleMember := models.EmbyItemMembership{ServerID: "server-a", ServerConfigKey: "connection", ItemID: "999", SnapshotID: 999, ItemType: "Episode", SeasonID: "200"}
			if err := conn.Create(&staleMember).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "roundtrip" {
				if err := conn.Model(&models.EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("observation_json", "damaged since backup").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := Restore(archivePath); err != nil {
				t.Fatal(err)
			}
			var rows []models.EmbyObservedEvidenceIndex
			if err := conn.Order("ref_index").Find(&rows).Error; err != nil || len(rows) != 2 {
				t.Fatalf("rebuilt index=%+v err=%v", rows, err)
			}
			for i, row := range rows {
				if row.RecordID != record.ID || row.RefIndex != i || row.EvidenceID != evidence[i].ID || row.ItemID != "101" || row.ServerConfigKey != "connection" || row.SeasonID != []string{"200", "201"}[i] {
					t.Fatalf("stale or incomplete index survived: %+v", row)
				}
			}
			var restoredRecord models.EmbyWebhookRecord
			var restoredEvidence []models.EmbyItemEvidence
			if err := conn.First(&restoredRecord, record.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.Order("id").Find(&restoredEvidence).Error; err != nil {
				t.Fatal(err)
			}
			if restoredRecord != record || !reflect.DeepEqual(restoredEvidence, evidence) {
				t.Fatal("derived rebuild changed original observations or evidence")
			}
			var members []models.EmbyItemMembership
			if err := conn.Find(&members).Error; err != nil || len(members) != 1 {
				t.Fatalf("rebuilt membership=%+v err=%v", members, err)
			}
			member := members[0]
			if member.ServerID != "server-a" || member.ServerConfigKey != "connection" || member.ItemID != "101" || member.ItemType != "Episode" || member.SnapshotID != evidence[1].ID || member.SeasonID != "201" || member.SeriesID != "300" {
				t.Fatalf("restored stale membership or lost state evidence: %+v", member)
			}
			var stateAfter models.EmbyItemState
			if err := conn.First(&stateAfter).Error; err != nil || stateAfter != stateBefore {
				t.Fatalf("membership rebuild changed original state: before=%+v after=%+v err=%v", stateBefore, stateAfter, err)
			}
			if conn.Migrator().HasTable(&models.EmbyMediaItem{}) {
				t.Fatal("membership restore fabricated the current media index")
			}
		})
	}
}

func TestRestoreEmbyObservationIndexFailureLeavesNoStaleIndex(t *testing.T) {
	testRestoreEmbyObservationIndexFailureLeavesNoStaleIndex(t, setupEmbyObservationBackup)
}

func testRestoreEmbyObservationIndexFailureLeavesNoStaleIndex(t *testing.T, setup func(*testing.T) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence)) {
	for _, scenario := range []string{"damaged original", "index write failure", "missing state evidence", "state identity mismatch", "membership write failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, record, _ := setup(t)
			files := map[string]string{}
			switch scenario {
			case "damaged original":
				record.ObservationJSON = `[{"evidence_id":999999}]`
			case "missing state evidence", "state identity mismatch":
				var state models.EmbyItemState
				if err := conn.First(&state).Error; err != nil {
					t.Fatal(err)
				}
				if scenario == "missing state evidence" {
					state.SnapshotID = 999999
				} else {
					state.IdentityKey = "different-identity"
				}
				files["EmbyItemState.json"] = cleanupBackupJSON(t, state) + "\n"
			default:
				failedTable := "emby_observed_evidence_indices"
				if scenario == "membership write failure" {
					failedTable = "emby_item_memberships"
				}
				if err := conn.Callback().Create().Before("gorm:create").Register("test:observation_index_restore", func(tx *gorm.DB) {
					if tx.Statement.Table == failedTable {
						tx.AddError(errors.New("injected index write failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Callback().Create().Remove("test:observation_index_restore") })
			}
			files["EmbyWebhookRecord.json"] = cleanupBackupJSON(t, record) + "\n"
			archive := writeBackupArchive(t, files, zip.Store)
			if err := Restore(archive); err == nil {
				t.Fatal("failed derived index rebuild reported success")
			}
			assertEmbyDerivedIndexesUnavailable(t, conn)
			if result := GetRunningResult(); result.Status != models.BackupStatusFailed {
				t.Fatalf("restore failure status=%+v", result)
			}
		})
	}
}

func TestRestoreEmbyObservationPartialImportKeepsIndexUnavailable(t *testing.T) {
	testRestoreEmbyObservationPartialImportKeepsIndexUnavailable(t, setupEmbyObservationBackup)
}

func testRestoreEmbyObservationPartialImportKeepsIndexUnavailable(t *testing.T, setup func(*testing.T) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence)) {
	for _, scenario := range []string{"invalid json", "insert failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, older, _ := setup(t)
			older.ObservationJSON = `[{"evidence_id":11}]`
			latest := older
			latest.ID++
			latest.ObservationJSON = `[{"evidence_id":12}]`
			latestLine := cleanupBackupJSON(t, latest)
			if scenario == "invalid json" {
				latestLine = `{`
			} else {
				if err := conn.Callback().Create().Before("gorm:create").Register("test:latest_observation_import", func(tx *gorm.DB) {
					if row, ok := tx.Statement.Dest.(*models.EmbyWebhookRecord); ok && row.ID == latest.ID {
						tx.AddError(errors.New("latest observation import failed"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Callback().Create().Remove("test:latest_observation_import") })
			}
			archive := writeBackupArchive(t, map[string]string{
				"EmbyWebhookRecord.json": cleanupBackupJSON(t, older) + "\n" + latestLine + "\n",
				"backupTestItem.json":    "{\"ID\":1,\"Name\":\"still imported\"}\n",
			}, zip.Deflate)
			if err := Restore(archive); err == nil {
				t.Fatal("partial observation import reported success")
			}
			var restored models.EmbyWebhookRecord
			if err := conn.First(&restored).Error; err != nil || restored.ObservationJSON != older.ObservationJSON {
				t.Fatalf("older observation fixture was not imported: %+v %v", restored, err)
			}
			assertEmbyDerivedIndexesUnavailable(t, conn)
			var other backupTestItem
			if err := conn.First(&other).Error; err != nil || other.Name != "still imported" {
				t.Fatalf("partial failure stopped remaining table import: %+v %v", other, err)
			}
		})
	}
}

func TestRestoreEmbyMembershipPartialEvidenceKeepsIndexUnavailable(t *testing.T) {
	testRestoreEmbyMembershipPartialEvidenceKeepsIndexUnavailable(t, setupEmbyObservationBackup)
}

func testRestoreEmbyMembershipPartialEvidenceKeepsIndexUnavailable(t *testing.T, setup func(*testing.T) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence)) {
	for _, scenario := range []string{"invalid json", "insert failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, _, evidence := setup(t)
			latestLine := cleanupBackupJSON(t, evidence[1])
			if scenario == "invalid json" {
				latestLine = `{`
			} else {
				if err := conn.Callback().Create().Before("gorm:create").Register("test:latest_evidence_import", func(tx *gorm.DB) {
					if row, ok := tx.Statement.Dest.(*models.EmbyItemEvidence); ok && row.ID == evidence[1].ID {
						tx.AddError(errors.New("latest evidence import failed"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Callback().Create().Remove("test:latest_evidence_import") })
			}
			archive := writeBackupArchive(t, map[string]string{
				"EmbyItemEvidence.json": cleanupBackupJSON(t, evidence[0]) + "\n" + latestLine + "\n",
				"backupTestItem.json":   "{\"ID\":1,\"Name\":\"still imported\"}\n",
			}, zip.Deflate)
			if err := Restore(archive); err == nil {
				t.Fatal("partial state evidence import reported success")
			}
			var restored []models.EmbyItemEvidence
			if err := conn.Find(&restored).Error; err != nil || len(restored) != 1 || restored[0].ID != evidence[0].ID {
				t.Fatalf("partial evidence fixture=%+v err=%v", restored, err)
			}
			assertEmbyDerivedIndexesUnavailable(t, conn)
			var other backupTestItem
			if err := conn.First(&other).Error; err != nil || other.Name != "still imported" {
				t.Fatalf("partial failure stopped remaining table import: %+v %v", other, err)
			}
		})
	}
}

func assertEmbyDerivedIndexesUnavailable(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, model := range []any{&models.EmbyObservedEvidenceIndex{}, &models.EmbyItemMembership{}} {
		if conn.Migrator().HasTable(model) {
			t.Fatalf("failed restore retained a usable stale or partial %T", model)
		}
	}
}
