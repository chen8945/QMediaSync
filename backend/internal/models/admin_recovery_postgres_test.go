//go:build integration

package models

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoverAdminPostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("设置 QMS_TEST_POSTGRES_DSN 为可创建 schema 的 PostgreSQL 测试库 URL")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil {
		t.Fatal("QMS_TEST_POSTGRES_DSN 必须是包含用户和数据库名的 URL")
	}
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil {
			t.Fatal(err)
		}
	}
	password, _ := parsed.User.Password()
	config := helpers.ConfigDb{
		Engine: helpers.DbEnginePostgres, PostgresType: helpers.PostgresTypeExternal,
		PostgresConfig: helpers.PostgresConfig{
			Host: parsed.Hostname(), Port: port, User: parsed.User.Username(), Password: password,
			Database: strings.TrimPrefix(parsed.Path, "/"), SSL: parsed.Query().Get("sslmode") != "disable",
		},
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := baseSQL.Close(); err != nil {
			t.Error(err)
		}
	})
	testRecoverAdmin(t, func(t *testing.T) *gorm.DB {
		schema := fmt.Sprintf("qms_admin_recovery_%d_%d", os.Getpid(), time.Now().UnixNano())
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := base.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
				t.Error(err)
			}
		})
		conn, err := db.OpenExisting(t.Context(), t.TempDir(), config)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if err := sqlDB.Close(); err != nil {
				t.Error(err)
			}
		})
		if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
			t.Fatal(err)
		}
		return conn
	})
}
