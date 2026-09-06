package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestParseAdminRecoveryOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		requested bool
		wantError bool
	}{
		{name: "normal startup"},
		{name: "normal deployment flags", args: []string{"--guid", "1000", "--fnos"}},
		{name: "reset", args: []string{"--reset-admin-password"}, requested: true},
		{name: "delete", args: []string{"--delete-admin", "--yes"}, requested: true},
		{name: "explicit path", args: []string{"--config-dir", "dir with spaces", "--reset-admin-password"}, requested: true},
		{name: "deployment flags", args: []string{"--fnos", "--guid", "1000", "--reset-admin-password"}, requested: true},
		{name: "unconfirmed delete", args: []string{"--delete-admin"}, requested: true, wantError: true},
		{name: "conflicting actions", args: []string{"--delete-admin", "--reset-admin-password", "--yes"}, requested: true, wantError: true},
		{name: "disabled action", args: []string{"--reset-admin-password=false"}, requested: true, wantError: true},
		{name: "missing action", args: []string{"--config-dir", "config"}, requested: true, wantError: true},
		{name: "positional argument", args: []string{"--reset-admin-password", "extra"}, requested: true, wantError: true},
		{name: "update forbidden", args: []string{"--reset-admin-password", "--update", "dir"}, requested: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, err := parseAdminRecoveryOptions(tc.args)
			if (options != nil) != tc.requested || (err != nil) != tc.wantError {
				t.Fatalf("requested=%v error=%v", options != nil, err)
			}
		})
	}
}

func prepareAdminRecoveryTest(t *testing.T) string {
	t.Helper()
	oldConfig, oldConfigDir, oldRootDir, oldLevel := helpers.GlobalConfig, helpers.ConfigDir, helpers.RootDir, helpers.ConfiguredLogLevel()
	t.Cleanup(func() {
		helpers.GlobalConfig, helpers.ConfigDir, helpers.RootDir = oldConfig, oldConfigDir, oldRootDir
		helpers.SetGlobalLogLevel(oldLevel)
	})
	dir := t.TempDir()
	helpers.RootDir = filepath.Dir(dir)
	config := []byte("db:\n  engine: sqlite\n  sqliteFile: existing.db\njwtSecret: QMediaSync-JWT-TOKEN-250706\nlog:\n  level: error\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := gorm.Open(sqlite.Open(filepath.Join(dir, "existing.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&models.User{}, &models.UserSession{}, &models.ApiKey{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.User{BaseModel: models.BaseModel{ID: 42}, Username: "admin", Password: "damaged", TwoFactorEnabled: true, TwoFactorSecret: "damaged"}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPerformAdminRecovery(t *testing.T) {
	dir := prepareAdminRecoveryTest(t)
	configPath := filepath.Join(dir, "config.yaml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := performAdminRecovery(t.Context(), dir, false)
	if err != nil || result == nil {
		t.Fatalf("恢复失败：%v", err)
	}
	message := adminRecoveryMessage(result)
	if !strings.Contains(message, result.Username) || !strings.Contains(message, result.Password) || !strings.Contains(message, "API Key 已保留") {
		t.Fatal("终端结果缺少必要的恢复信息")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("恢复命令写回了配置文件或生成了新的 JWT 密钥")
	}
	if _, err := os.Stat(filepath.Join(dir, "encryption.key")); !os.IsNotExist(err) {
		t.Fatal("恢复命令生成了本机加密密钥")
	}
	logs, err := os.ReadFile(filepath.Join(dir, "logs", "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logs, []byte("管理员恢复完成")) || bytes.Contains(logs, []byte(result.Password)) {
		t.Fatal("应用日志缺少恢复审计或泄露了密码")
	}
	deleted, err := performAdminRecovery(t.Context(), dir, true)
	if err != nil || deleted == nil || !deleted.Deleted || deleted.Password != "" {
		t.Fatalf("删除管理员失败：%v", err)
	}
	if !strings.Contains(adminRecoveryMessage(deleted), "启动日志获取初始化码") {
		t.Fatal("删除结果缺少正常启动后的初始化说明")
	}
}

func TestPerformAdminRecoveryRedactsConnectionError(t *testing.T) {
	dir := prepareAdminRecoveryTest(t)
	const password = "recovery-test-only-db-password"
	config := "db:\n  engine: postgres\n  postgresType: external\n  postgresConfig:\n    host: 'bad host'\n    port: 5432\n    user: recovery\n    password: " + password + "\n    database: recovery\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := performAdminRecovery(t.Context(), dir, false)
	if err == nil || result != nil {
		t.Fatal("无效数据库地址不应执行恢复")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatal("数据库连接错误包含配置密码")
	}
	logs, err := os.ReadFile(filepath.Join(dir, "logs", "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logs, []byte("管理员恢复失败")) || bytes.Contains(logs, []byte(password)) {
		t.Fatal("应用日志缺少失败审计或泄露了数据库密码")
	}
}

func TestPerformAdminRecoveryRejectsUnsafeState(t *testing.T) {
	for _, name := range []string{"missing config", "missing database", "running instance", "pending migration"} {
		t.Run(name, func(t *testing.T) {
			dir := prepareAdminRecoveryTest(t)
			databasePath := filepath.Join(dir, "existing.db")
			before, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing config":
				if err := os.Remove(filepath.Join(dir, "config.yaml")); err != nil {
					t.Fatal(err)
				}
			case "missing database":
				if err := os.Remove(databasePath); err != nil {
					t.Fatal(err)
				}
			case "running instance":
				lock, err := helpers.AcquireInstanceLock(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "pending migration":
				if err := os.Mkdir(filepath.Join(dir, "backups"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "backups", "migrate.zip"), []byte("pending"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := performAdminRecovery(t.Context(), dir, false)
			if err == nil || result != nil {
				t.Fatal("不安全状态不应执行管理员恢复")
			}
			after, err := os.ReadFile(databasePath)
			if name == "missing database" {
				if !os.IsNotExist(err) {
					t.Fatal("恢复命令误建了新数据库")
				}
			} else if err != nil || !bytes.Equal(before, after) {
				t.Fatal("拒绝恢复时修改了数据库")
			}
		})
	}
}

func TestAdminRecoveryConfigDir(t *testing.T) {
	oldRoot := helpers.RootDir
	t.Cleanup(func() { helpers.RootDir = oldRoot })
	helpers.RootDir = t.TempDir()
	t.Setenv("TRIM_PKGETC", "")
	t.Setenv("TRIM_DATA_SHARE_PATHS", "")
	got, err := resolveConfigDir("")
	if err != nil || got != filepath.Join(helpers.RootDir, "config") {
		t.Fatal("默认恢复路径没有沿用部署目录")
	}
	if runtime.GOOS != "windows" {
		t.Setenv("TRIM_PKGETC", filepath.Join(helpers.RootDir, "etc"))
		t.Setenv("TRIM_DATA_SHARE_PATHS", filepath.Join(helpers.RootDir, "share"))
		got, err = resolveConfigDir("")
		if err != nil || got != filepath.Join(helpers.RootDir, "share", "config") {
			t.Fatal("飞牛恢复路径不正确")
		}
	}
	explicit := filepath.Join(helpers.RootDir, "explicit")
	got, err = resolveConfigDir(explicit)
	if err != nil || got != explicit {
		t.Fatal("显式配置目录未优先生效")
	}
}

func TestStartupConfigMigrationLock(t *testing.T) {
	for _, held := range []string{"target", "source", "none"} {
		t.Run(held, func(t *testing.T) {
			oldRoot, oldConfig, oldData, oldLock := helpers.RootDir, helpers.ConfigDir, helpers.DataDir, instanceLock
			helpers.RootDir = t.TempDir()
			t.Cleanup(func() {
				if instanceLock != nil && instanceLock != oldLock {
					instanceLock.Close()
				}
				helpers.RootDir, helpers.ConfigDir, helpers.DataDir, instanceLock = oldRoot, oldConfig, oldData, oldLock
			})
			source := filepath.Join(helpers.RootDir, "legacy")
			target := filepath.Join(helpers.RootDir, "share", "config")
			if runtime.GOOS == "windows" {
				t.Setenv("LOCALAPPDATA", source)
				source = filepath.Join(source, AppName, "config")
				target = filepath.Join(helpers.RootDir, "config")
			} else {
				t.Setenv("TRIM_PKGETC", source)
				t.Setenv("TRIM_DATA_SHARE_PATHS", filepath.Dir(target))
			}
			for _, entry := range []struct{ dir, value string }{{source, "legacy"}, {target, "current"}} {
				if err := os.MkdirAll(entry.dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(entry.dir, "config.yaml"), []byte(entry.value), 0o600); err != nil {
					t.Fatal(err)
				}
				lock, err := helpers.AcquireInstanceLock(entry.dir)
				if err != nil {
					t.Fatal(err)
				}
				if held == "source" && entry.dir == source || held == "target" && entry.dir == target {
					defer lock.Close()
				} else if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
			}
			lockBefore, err := os.Stat(filepath.Join(target, ".qmediasync.lock"))
			if err != nil {
				t.Fatal(err)
			}
			err = getDataAndConfigDir()
			if (err != nil) != (held != "none") {
				t.Fatalf("迁移未遵守源目录和目标目录的实例锁：%v", err)
			}
			want := "current"
			if held == "none" {
				want = "legacy"
			}
			content, err := os.ReadFile(filepath.Join(target, "config.yaml"))
			if err != nil || string(content) != want {
				t.Fatal("迁移覆盖了正在使用的配置，或未完成允许的迁移")
			}
			lockAfter, err := os.Stat(filepath.Join(target, ".qmediasync.lock"))
			if err != nil || !os.SameFile(lockBefore, lockAfter) {
				t.Fatal("迁移替换了目标实例锁文件")
			}
			if _, err := os.Stat(filepath.Join(source, ".qmediasync.lock")); err != nil {
				t.Fatal("迁移删除了源目录实例锁文件")
			}
		})
	}
}
