//go:build integration

package syncstrm

import (
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSTRMLocalEvidenceSurvivesReopenAndEmbySyncPostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for an isolated PostgreSQL test database")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil || parsed.Host == "" {
		t.Fatal("QMS_TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgres.New(postgres.Config{DriverName: "pgx", DSN: dsn}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adminSQL.Close(); err != nil {
			t.Error(err)
		}
	})
	schema := fmt.Sprintf("qms_strm_emby_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	// 首次连接和重开连接都使用同一隔离 schema，不预种根资料或条目证据。
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	query.Set("statement_timeout", "15000")
	parsed.RawQuery = query.Encode()
	t.Logf("isolated_schema=%s postgres_connections=4", schema)
	testSTRMLocalEvidenceSurvivesReopenAndEmbySync(t, postgres.New(postgres.Config{DriverName: "pgx", DSN: parsed.String()}))
}
