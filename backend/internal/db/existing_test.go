package db

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestOpenExistingSQLiteURI(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/tmp/QMS config/data ?#&.db", want: "/tmp/QMS config/data ?#&.db"},
		{path: "C:/QMediaSync/config/data.db", want: "/C:/QMediaSync/config/data.db"},
	} {
		dsn := existingSQLiteDSN(tc.path)
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("解析 SQLite URI %q：%v", dsn, err)
		}
		if parsed.Scheme != "file" || parsed.Host != "" || parsed.Path != tc.want ||
			parsed.Query().Get("mode") != "rw" || parsed.Query().Get("_pragma") != "busy_timeout(10000)" {
			t.Errorf("SQLite URI 未保留绝对文件路径和只打开已有文件的选项：%q", dsn)
		}
	}
}

func TestOpenExisting(t *testing.T) {
	for _, tc := range []struct {
		name      string
		file      string
		prepare   bool
		canceled  bool
		wantError bool
	}{
		{name: "existing file", file: "data.db", prepare: true},
		{name: "URI characters in filename", file: "data ?#&.db", prepare: true},
		{name: "missing file", file: "missing.db", wantError: true},
		{name: "empty filename", wantError: true},
		{name: "directory", file: ".", wantError: true},
		{name: "canceled", file: "data.db", prepare: true, canceled: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if tc.prepare {
				// 先创建普通数据库再改名，避免测试准备把文件名解释成 URI 参数。
				tmp := filepath.Join(dir, "initial.db")
				conn, err := gorm.Open(sqlite.Open(tmp), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				if err := conn.Exec("CREATE TABLE existing_marker (value TEXT)").Error; err != nil {
					t.Fatal(err)
				}
				sqlDB, err := conn.DB()
				if err != nil {
					t.Fatal(err)
				}
				if err := sqlDB.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			conn, err := OpenExisting(ctx, dir, helpers.ConfigDb{Engine: helpers.DbEngineSqlite, SqliteFile: tc.file})
			if tc.wantError {
				if err == nil {
					sqlDB, _ := conn.DB()
					sqlDB.Close()
					t.Fatal("应拒绝无效数据库或取消的连接")
				}
				if tc.name == "missing file" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("恢复入口创建了不存在的数据库")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			if !conn.Migrator().HasTable("existing_marker") || conn.Migrator().HasTable("users") {
				t.Fatal("恢复连接未打开原数据库，或意外执行了迁移")
			}
		})
	}
	for _, config := range []helpers.ConfigDb{
		{},
		{Engine: helpers.DbEnginePostgres, PostgresType: helpers.PostgresTypeEmbedded},
		{Engine: helpers.DbEnginePostgres, PostgresType: helpers.PostgresTypeExternal},
	} {
		if _, err := OpenExisting(t.Context(), t.TempDir(), config); err == nil {
			t.Fatal("无效配置或内嵌数据库不能用于恢复")
		}
	}
}
