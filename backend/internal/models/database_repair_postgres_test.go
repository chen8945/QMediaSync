//go:build integration

package models

import (
	"math"
	"strings"
	"testing"

	"gorm.io/gorm"
)

type sequenceRepairProbe struct {
	ID   int64 `gorm:"primaryKey"`
	Name string
}

func TestRepairDatabasePostgresRepairsAllRegisteredSequences(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Table("sync_files").Create(map[string]any{"id": 100, "file_id": "restored-file"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().DropIndex(&SyncFile{}, syncFileSiblingPathIndexName); err != nil {
		t.Fatal(err)
	}
	if err := RepairDatabase(conn); err != nil {
		t.Fatal(err)
	}
	if !conn.Migrator().HasIndex(&SyncFile{}, syncFileSiblingPathIndexName) {
		t.Fatal("repair omitted the missing lookup index")
	}
	next := SyncFile{FileId: "after-repair"}
	if err := conn.Create(&next).Error; err != nil || next.ID != 101 {
		t.Fatalf("repair did not repair the restored ID sequence: id=%d err=%v", next.ID, err)
	}
}

func TestResetSequencePostgresWaitsForOutstandingInsert(t *testing.T) {
	conn, schema := setupEmbySnapshotPostgres(t)
	if err := conn.AutoMigrate(&sequenceRepairProbe{}); err != nil {
		t.Fatal(err)
	}
	writer := conn.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	var writerPID int
	if err := writer.Raw("SELECT pg_backend_pid()").Scan(&writerPID).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Create(&sequenceRepairProbe{ID: 100, Name: "in-flight insert"}).Error; err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- ResetSequence("sequence_repair_probes", "id") }()
	waitEmbyPostgresLock(t, conn, schema, writerPID, result)
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := receiveEmbyPostgresResult(t, result); err != nil {
		t.Fatal(err)
	}
	next := sequenceRepairProbe{Name: "after repair"}
	if err := conn.Create(&next).Error; err != nil || next.ID != 101 {
		t.Fatalf("repair omitted the in-flight row: id=%d err=%v", next.ID, err)
	}
}

func TestResetSequencePostgresBlocksInsertUntilCommit(t *testing.T) {
	conn, schema := setupEmbySnapshotPostgres(t)
	if err := conn.AutoMigrate(&sequenceRepairProbe{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&sequenceRepairProbe{ID: 100}).Error; err != nil {
		t.Fatal(err)
	}
	repair := conn.Begin()
	if repair.Error != nil {
		t.Fatal(repair.Error)
	}
	defer repair.Rollback()
	var repairPID int
	if err := repair.Raw("SELECT pg_backend_pid()").Scan(&repairPID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ResetSequenceTx(repair, "sequence_repair_probes", "id"); err != nil {
		t.Fatal(err)
	}
	next := sequenceRepairProbe{Name: "wait for repair"}
	result := make(chan error, 1)
	go func() { result <- conn.Create(&next).Error }()
	waitEmbyPostgresLock(t, conn, schema, repairPID, result)
	if err := repair.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := receiveEmbyPostgresResult(t, result); err != nil || next.ID != 101 {
		t.Fatalf("insert after sequence repair: id=%d err=%v", next.ID, err)
	}
}

func TestResetSequencePostgresRollsBackWithRestore(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := conn.AutoMigrate(&sequenceRepairProbe{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&sequenceRepairProbe{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&sequenceRepairProbe{ID: 100}).Error; err != nil {
		t.Fatal(err)
	}
	repair := conn.Begin()
	if repair.Error != nil {
		t.Fatal(repair.Error)
	}
	defer repair.Rollback()
	if err := RepairSequencesTx(repair, []any{sequenceRepairProbe{}}); err != nil {
		t.Fatal(err)
	}
	restored := sequenceRepairProbe{Name: "uncommitted"}
	if err := repair.Create(&restored).Error; err != nil || restored.ID != 101 {
		t.Fatalf("transaction did not see repaired sequence: id=%d err=%v", restored.ID, err)
	}
	if err := repair.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	next := sequenceRepairProbe{Name: "original sequence"}
	if err := conn.Create(&next).Error; err != nil || next.ID != 2 {
		t.Fatalf("rolled-back restore changed sequence: id=%d err=%v", next.ID, err)
	}
}

func TestResetSequencePostgresPreservesAllocatedValues(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := conn.AutoMigrate(&sequenceRepairProbe{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&sequenceRepairProbe{}).Error; err != nil {
		t.Fatal(err)
	}
	var reserved int64
	if err := conn.Raw("SELECT nextval(pg_get_serial_sequence('sequence_repair_probes', 'id'))").Scan(&reserved).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Transaction(func(tx *gorm.DB) error {
		return ResetSequenceTx(tx, "sequence_repair_probes", "id")
	}); err != nil {
		t.Fatal(err)
	}
	next := sequenceRepairProbe{}
	if err := conn.Create(&next).Error; err != nil || next.ID <= reserved {
		t.Fatalf("repair reused reserved sequence value: id=%d reserved=%d err=%v", next.ID, reserved, err)
	}
}

func TestResetSequencePostgresPreservesMaximumPrimaryKey(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := conn.AutoMigrate(&sequenceRepairProbe{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&sequenceRepairProbe{ID: math.MaxInt64, Name: "maximum ID"}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := conn.Transaction(func(tx *gorm.DB) error {
			return ResetSequenceTx(tx, "sequence_repair_probes", "id")
		}); err != nil {
			t.Fatal(err)
		}
		var id int64
		err := conn.Raw("SELECT nextval(pg_get_serial_sequence('sequence_repair_probes', 'id'))").Scan(&id).Error
		if err == nil || !strings.Contains(err.Error(), "reached maximum value") {
			t.Fatalf("maximum ID should leave the sequence exhausted, id=%d err=%v", id, err)
		}
	}
	var saved sequenceRepairProbe
	if err := conn.First(&saved, int64(math.MaxInt64)).Error; err != nil || saved.ID != math.MaxInt64 || saved.Name != "maximum ID" {
		t.Fatalf("repair changed the maximum primary key: %+v err=%v", saved, err)
	}
}
