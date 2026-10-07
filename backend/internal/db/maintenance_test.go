package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func maintenanceTestDB(t *testing.T) (*gorm.DB, *sql.DB) {
	t.Helper()
	database := InitSqlite3(filepath.Join(t.TempDir(), "maintenance.db"))
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := database.Exec("CREATE TABLE maintenance_items (id INTEGER PRIMARY KEY, value TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	return database, pool
}

func waitMaintenanceBlocked(t *testing.T, database *gorm.DB) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !IsMaintenance(database) {
		select {
		case <-deadline:
			t.Fatal("maintenance did not close admission")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestMaintenanceWaitsForTransactionsAndRejectsStaleWriters(t *testing.T) {
	database, pool := maintenanceTestDB(t)
	stmt, err := pool.Prepare("INSERT INTO maintenance_items (value) VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	tx, err := pool.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO maintenance_items (value) VALUES ('old transaction')"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		maintenance *Maintenance
		err         error
	}
	finished := make(chan result, 1)
	go func() { m, err := BeginMaintenance(t.Context(), database); finished <- result{m, err} }()
	waitMaintenanceBlocked(t, database)
	select {
	case <-finished:
		t.Fatal("maintenance did not wait for open transaction")
	default:
	}
	// 维护等候期间，已经获得许可的事务必须能继续完成。
	if _, err := tx.Exec("UPDATE maintenance_items SET value='finished'"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var maintenance *Maintenance
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatal(result.err)
		}
		maintenance = result.maintenance
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance deadlocked on SQLite single connection")
	}
	for name, write := range map[string]func() error{
		"gorm": func() error { return database.Exec("INSERT INTO maintenance_items (value) VALUES ('stale')").Error },
		"raw sql": func() error {
			_, err := pool.Exec("INSERT INTO maintenance_items (value) VALUES ('stale')")
			return err
		},
		"prepared statement": func() error { _, err := stmt.Exec("stale"); return err },
		"query row": func() error {
			var value string
			return pool.QueryRow("SELECT value FROM maintenance_items").Scan(&value)
		},
		"begin": func() error { _, err := pool.Begin(); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(); !errors.Is(err, ErrMaintenance) {
				t.Fatalf("stale operation error=%v", err)
			}
		})
	}
	if err := maintenance.Database().Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM maintenance_items").Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO maintenance_items (value) VALUES ('restored')").Error
	}); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := maintenance.Database().Raw("SELECT value FROM maintenance_items").Scan(&value).Error; err != nil || value != "restored" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if !IsMaintenance(database) {
		t.Fatal("restore transaction reopened ordinary SQL")
	}
}

func TestMaintenanceWaitsForRowsUntilClose(t *testing.T) {
	database, pool := maintenanceTestDB(t)
	if _, err := pool.Exec("INSERT INTO maintenance_items (value) VALUES ('one'), ('two')"); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query("SELECT * FROM maintenance_items")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	finished := make(chan error, 1)
	go func() { _, err := BeginMaintenance(t.Context(), database); finished <- err }()
	waitMaintenanceBlocked(t, database)
	select {
	case <-finished:
		t.Fatal("maintenance entered while rows were open")
	default:
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rows close did not release maintenance wait")
	}
}

func TestMaintenanceTimeoutKeepsGateClosed(t *testing.T) {
	database, pool := maintenanceTestDB(t)
	tx, err := pool.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = BeginMaintenance(ctx, database)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("INSERT INTO maintenance_items (value) VALUES ('late')").Error; !errors.Is(err, ErrMaintenance) {
		t.Fatalf("timed out maintenance released gate: %v", err)
	}
}
