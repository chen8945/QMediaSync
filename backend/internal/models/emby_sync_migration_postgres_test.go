//go:build integration

package models

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestEmbySyncMigrationPostgres(t *testing.T) {
	for _, scenario := range []string{"upgrade", "table_failure", "column_failure", "index_failure", "version_failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			testEmbySyncMigration(t, conn, func() *gorm.DB {
				conn = reopenEmbyWebhookPostgres(t, conn, schema)
				return conn
			}, scenario)
			var definition string
			if err := conn.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ? AND indexname = ?", "sync_files", syncFileNameIndexName).Scan(&definition).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(definition, "CREATE INDEX ") || !strings.Contains(definition, " USING hash (file_name)") {
				t.Fatalf("完整名称索引必须为非唯一 HASH：%s", definition)
			}
		})
	}
}
