//go:build integration

package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type maintenanceSchemaProbe struct {
	ID    int64 `gorm:"primaryKey"`
	Value string
}

func TestMaintenancePostgresDrainsTransactionsAndRows(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" {
		t.Fatal("QMS_TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("qms_maintenance_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("statement_timeout", "5000")
	parsed.RawQuery = query.Encode()
	pool, err := OpenMaintenanceSQL("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxOpenConns(4)
	database, err := gorm.Open(postgres.New(postgres.Config{DriverName: "postgres", Conn: pool}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// 已有表的 AutoMigrate/ColumnTypes 必须使用 lib/pq 参数语义。
	for range 2 {
		if err := database.AutoMigrate(&maintenanceSchemaProbe{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Migrator().ColumnTypes(&maintenanceSchemaProbe{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("CREATE TABLE maintenance_items (id bigint PRIMARY KEY, value text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("INSERT INTO maintenance_items VALUES (1,'initial')"); err != nil {
		t.Fatal(err)
	}
	prepared, err := pool.Prepare("UPDATE maintenance_items SET value=$1")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	tx, err := pool.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := pool.Query("SELECT value FROM maintenance_items")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type result struct {
		maintenance *Maintenance
		err         error
	}
	finished := make(chan result, 1)
	go func() {
		maintenance, err := BeginMaintenance(t.Context(), database)
		finished <- result{maintenance, err}
	}()
	waitMaintenanceBlocked(t, database)
	if _, err := prepared.Exec("stale"); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("prepared SQL passed gate: %v", err)
	}
	if _, err := tx.Exec("UPDATE maintenance_items SET value='finished'"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
		t.Fatal("maintenance did not wait for PostgreSQL result set")
	default:
	}
	if err := rows.Close(); err != nil {
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
		t.Fatal("PostgreSQL drain timed out")
	}
	if err := maintenance.Database().Transaction(func(tx *gorm.DB) error { return tx.Exec("UPDATE maintenance_items SET value='restored'").Error }); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := maintenance.Database().Raw("SELECT value FROM maintenance_items").Scan(&value).Error; err != nil || value != "restored" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if err := database.Exec("UPDATE maintenance_items SET value='late'").Error; !errors.Is(err, ErrMaintenance) {
		t.Fatalf("late SQL passed gate: %v", err)
	}
}
