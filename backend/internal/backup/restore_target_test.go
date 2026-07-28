package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"gopkg.in/yaml.v2"
)

// testRestoreConfigYAML 生成工件携带的配置副本；恢复目标只由它决定。
func testRestoreConfigYAML(t *testing.T, config helpers.Config) []byte {
	t.Helper()
	data, err := yaml.Marshal(&config)
	if err != nil {
		t.Fatalf("yaml.Marshal(config) error = %v", err)
	}
	return data
}

func testSqliteConfig(fileName string) helpers.Config {
	config := helpers.Config{}
	config.Db.Engine = helpers.DbEngineSqlite
	config.Db.SqliteFile = fileName
	return config
}

func testPostgresConfig(password string) helpers.Config {
	config := helpers.Config{}
	config.Db.Engine = helpers.DbEnginePostgres
	config.Db.PostgresType = helpers.PostgresTypeExternal
	config.Db.PostgresConfig = helpers.PostgresConfig{
		Host:     "db.example.internal",
		Port:     5433,
		User:     "qms",
		Password: password,
		Database: "qms_prod",
	}
	return config
}

// TestResolveRestoreTargetKeepsCredentialsOutOfLabel 覆盖 D3 的目标标识约定：
// 恢复确认与阶段日志展示的标识必须能识别目标，且绝不包含数据库密码。
func TestResolveRestoreTargetKeepsCredentialsOutOfLabel(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	const password = "super-secret-password"

	target, err := resolveRestoreTarget(testRestoreConfigYAML(t, testPostgresConfig(password)))
	if err != nil {
		t.Fatalf("resolveRestoreTarget(postgres) error = %v", err)
	}
	if strings.Contains(target.Label, password) {
		t.Fatalf("目标标识泄露了数据库密码：%q", target.Label)
	}
	for _, expected := range []string{"postgres", "external", "db.example.internal", "5433", "qms_prod"} {
		if !strings.Contains(target.Label, expected) {
			t.Fatalf("目标标识 %q 缺少可识别信息 %q", target.Label, expected)
		}
	}
	if target.Postgres.Password != password {
		t.Fatal("恢复目标必须在进程内保留连接密码")
	}

	sqliteTarget, err := resolveRestoreTarget(testRestoreConfigYAML(t, testSqliteConfig("qmediasync.db")))
	if err != nil {
		t.Fatalf("resolveRestoreTarget(sqlite) error = %v", err)
	}
	if sqliteTarget.Label != "sqlite:qmediasync.db" {
		t.Fatalf("SQLite 目标标识 = %q，应为相对文件路径", sqliteTarget.Label)
	}
	if sqliteTarget.SQLitePath != filepath.Join(helpers.ConfigDir, "qmediasync.db") {
		t.Fatalf("SQLite 目标路径 = %q，应位于配置目录内", sqliteTarget.SQLitePath)
	}
}

// TestResolveRestoreTargetRejectsUnsupportedOrEscapingTargets 覆盖预检的兼容性边界：
// 未指定引擎、路径逃逸和缺参数的配置都必须在任何写入前被拒绝。
func TestResolveRestoreTargetRejectsUnsupportedOrEscapingTargets(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	incompletePostgres := testPostgresConfig("secret")
	incompletePostgres.Db.PostgresConfig.Database = ""

	for _, test := range []struct {
		name   string
		config helpers.Config
	}{
		{name: "未指定引擎", config: helpers.Config{}},
		{name: "SQLite 绝对路径", config: testSqliteConfig("/etc/passwd")},
		{name: "SQLite 向上逃逸", config: testSqliteConfig("../outside.db")},
		{name: "SQLite 缺少文件", config: testSqliteConfig("")},
		{name: "PostgreSQL 缺少库名", config: incompletePostgres},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveRestoreTarget(testRestoreConfigYAML(t, test.config)); !errors.Is(err, ErrRestoreTargetIncompatible) {
				t.Fatalf("resolveRestoreTarget() error = %v, want ErrRestoreTargetIncompatible", err)
			}
		})
	}
}

func TestResolveRestoreTargetRejectsSQLitePathThroughSymlink(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(helpers.ConfigDir, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("创建符号链接失败：%v", err)
	}

	_, err := resolveRestoreTarget(testRestoreConfigYAML(t, testSqliteConfig("linked/restored.db")))
	if !errors.Is(err, ErrRestoreTargetIncompatible) {
		t.Fatalf("resolveRestoreTarget() error = %v, want ErrRestoreTargetIncompatible", err)
	}
}

// TestVerifyRestoreCompatibilityRejectsEngineAndSchemaMismatch 覆盖 R1：
// 错误引擎与更高 schema 版本必须在任何数据库写入前失败。
func TestVerifyRestoreCompatibilityRejectsEngineAndSchemaMismatch(t *testing.T) {
	target := RestoreTarget{Engine: string(helpers.DbEngineSqlite)}
	header := ArtifactHeader{SourceEngine: string(helpers.DbEngineSqlite), SchemaVersion: models.SchemaVersion}

	if err := verifyRestoreCompatibility(header, target, models.SchemaVersion); err != nil {
		t.Fatalf("同引擎同版本应通过，error = %v", err)
	}

	higher := header
	higher.SchemaVersion = models.SchemaVersion + 1
	if err := verifyRestoreCompatibility(higher, target, models.SchemaVersion); !errors.Is(err, ErrRestoreTargetIncompatible) {
		t.Fatalf("更高版本 error = %v, want ErrRestoreTargetIncompatible", err)
	}

	otherEngine := header
	otherEngine.SourceEngine = string(helpers.DbEnginePostgres)
	if err := verifyRestoreCompatibility(otherEngine, target, models.SchemaVersion); !errors.Is(err, ErrRestoreTargetIncompatible) {
		t.Fatalf("跨引擎 error = %v, want ErrRestoreTargetIncompatible", err)
	}

	// 较低 schema 版本允许恢复，其升级由下一次启动的既有迁移链完成。
	lower := header
	lower.SchemaVersion = models.SchemaVersion - 1
	if lower.SchemaVersion >= 0 {
		if err := verifyRestoreCompatibility(lower, target, models.SchemaVersion); err != nil {
			t.Fatalf("较低版本应允许恢复，error = %v", err)
		}
	}
}

// TestCheckSqliteTargetReadyValidatesWritabilityAndSpace 覆盖 D3 的预检能力检查：
// 目录不可用要报目标不可用，空间不足要报空间不足，二者都在写入前发生。
func TestCheckSqliteTargetReadyValidatesWritabilityAndSpace(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	target := RestoreTarget{
		Engine:     string(helpers.DbEngineSqlite),
		SQLitePath: filepath.Join(helpers.ConfigDir, "qmediasync.db"),
	}
	if err := checkRestoreTargetReady(target, 1024); err != nil {
		t.Fatalf("可写目录应通过预检，error = %v", err)
	}

	missing := RestoreTarget{
		Engine:     string(helpers.DbEngineSqlite),
		SQLitePath: filepath.Join(helpers.ConfigDir, "missing", "qmediasync.db"),
	}
	if err := checkRestoreTargetReady(missing, 1024); !errors.Is(err, ErrRestoreTargetUnavailable) {
		t.Fatalf("缺失目录 error = %v, want ErrRestoreTargetUnavailable", err)
	}

	// 需求空间远超任何真实磁盘，必须在预检阶段以稳定错误码拒绝。
	if err := checkRestoreTargetReady(target, int64(1)<<62); !errors.Is(err, ErrRestoreInsufficientSpace) {
		t.Fatalf("空间不足 error = %v, want ErrRestoreInsufficientSpace", err)
	}
}

// TestCheckSqliteTargetReadyRejectsIrregularTarget 覆盖目标必须是常规文件：
// 目录或特殊文件占位时不能被当作可恢复的数据库。
func TestCheckSqliteTargetReadyRejectsIrregularTarget(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	targetPath := filepath.Join(helpers.ConfigDir, "qmediasync.db")
	if err := os.Mkdir(targetPath, 0o700); err != nil {
		t.Fatalf("Mkdir(target) error = %v", err)
	}
	target := RestoreTarget{Engine: string(helpers.DbEngineSqlite), SQLitePath: targetPath}
	if err := checkRestoreTargetReady(target, 1024); !errors.Is(err, ErrRestoreTargetUnavailable) {
		t.Fatalf("目录占位 error = %v, want ErrRestoreTargetUnavailable", err)
	}
}
