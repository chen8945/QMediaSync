//go:build integration

package db

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"

	"qmediasync/internal/helpers"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOpenExistingPostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config := helpers.ConfigDb{
		Engine: helpers.DbEnginePostgres,
		PostgresConfig: helpers.PostgresConfig{
			Host: parsed.Host, Port: int(parsed.Port), User: parsed.User,
			Password: parsed.Password, Database: parsed.Database, SSL: parsed.TLSConfig != nil,
		},
	}
	conn, err := OpenExisting(t.Context(), t.TempDir(), config)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if got := pool.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("recovery connection pool must have one connection, got %d", got)
	}
	var name string
	if err := pool.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&name); err != nil || name != parsed.Database {
		t.Fatalf("recovery did not open the configured database: name=%q err=%v", name, err)
	}

	t.Run("missing_database", func(t *testing.T) {
		missing := config
		missing.PostgresConfig.Database = "qms_missing_" + strings.ToLower(rand.Text())
		opened, err := OpenExisting(t.Context(), t.TempDir(), missing)
		if opened != nil {
			unexpected, _ := opened.DB()
			_ = unexpected.Close()
		}
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.SQLState() != "3D000" {
			t.Fatalf("missing database must retain PostgreSQL SQLSTATE 3D000: %v", err)
		}
		var exists bool
		if err := pool.QueryRowContext(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", missing.PostgresConfig.Database).Scan(&exists); err != nil || exists {
			t.Fatalf("recovery created a missing database: exists=%t err=%v", exists, err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		opened, err := OpenExisting(ctx, t.TempDir(), config)
		if opened != nil {
			unexpected, _ := opened.DB()
			_ = unexpected.Close()
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("recovery did not preserve connection cancellation: %v", err)
		}
	})
}
