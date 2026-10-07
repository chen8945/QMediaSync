package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackupReaderIsReadOnlyIndependentAndClosed(t *testing.T) {
	database, businessPool := maintenanceTestDB(t)
	reader, closeReader, err := OpenBackupReader(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeReader() })
	readerPool, err := reader.DB()
	if err != nil {
		t.Fatal(err)
	}
	if readerPool == businessPool || businessPool.Stats().MaxOpenConnections != 1 {
		t.Fatal("backup must not share or resize the business pool")
	}
	if err := reader.Exec("INSERT INTO maintenance_items (value) VALUES ('forbidden')").Error; err == nil {
		t.Fatal("backup reader must reject writes")
	}
	if err := closeReader(); err != nil {
		t.Fatal(err)
	}
	if err := readerPool.Ping(); err == nil {
		t.Fatal("dedicated backup connection was not closed")
	}
	if err := businessPool.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupReaderSharesRestoreMaintenanceGate(t *testing.T) {
	database, _ := maintenanceTestDB(t)
	reader, closeReader, err := OpenBackupReader(database)
	if err != nil {
		t.Fatal(err)
	}
	defer closeReader()
	tx := reader.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := BeginMaintenance(ctx, database); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("maintenance did not drain the backup transaction: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := reader.Exec("SELECT 1").Error; !errors.Is(err, ErrMaintenance) {
		t.Fatalf("reader bypassed maintenance: %v", err)
	}
}
