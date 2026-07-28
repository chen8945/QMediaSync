package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
)

const testSnapshotOperationID = "aabbccddeeff00112233445566778899"

// setupSnapshotTestConfig 准备一个完整的白名单文件树与 SQLite 目标文件。
func setupSnapshotTestConfig(t *testing.T, createTarget bool) (RestoreTarget, []byte) {
	t.Helper()
	helpers.ConfigDir = t.TempDir()

	writeTestFile(t, filepath.Join(helpers.ConfigDir, "encryption.key"), []byte("instance-key\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yaml"), []byte("db:\n  engine: sqlite\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "server.crt"), []byte("original-certificate"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "logs", "app.log"), []byte("original log\n"))

	targetConfigYAML := testRestoreConfigYAML(t, testSqliteConfig("qmediasync.db"))
	target, err := resolveRestoreTarget(targetConfigYAML)
	if err != nil {
		t.Fatalf("resolveRestoreTarget() error = %v", err)
	}
	if createTarget {
		writeTestFile(t, target.SQLitePath, []byte("original-database"))
		writeTestFile(t, target.SQLitePath+"-wal", []byte("original-wal"))
	}
	return target, targetConfigYAML
}

// TestRestoreSnapshotRollbackRestoresDatabaseAndWhitelistTree 覆盖 D3 的自动回滚语义：
// 回滚必须还原目标数据库、被改写的配置和被删除的文件，并删除恢复过程新增的同类文件。
func TestRestoreSnapshotRollbackRestoresDatabaseAndWhitelistTree(t *testing.T) {
	target, targetConfigYAML := setupSnapshotTestConfig(t, true)

	snapshot, err := CreateRestoreSnapshot(testSnapshotOperationID, target, targetConfigYAML)
	if err != nil {
		t.Fatalf("CreateRestoreSnapshot() error = %v", err)
	}

	// 模拟恢复已经覆盖数据库、配置和日志，并新增了工件中不存在的文件。
	writeTestFile(t, target.SQLitePath, []byte("restored-database"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yaml"), []byte("db:\n  engine: postgres\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yml"), []byte("legacy: true\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "server.key"), []byte("new private key"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "logs", "extra.log"), []byte("new log\n"))
	if err := os.Remove(filepath.Join(helpers.ConfigDir, "server.crt")); err != nil {
		t.Fatalf("Remove(server.crt) error = %v", err)
	}

	if err := snapshot.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	for path, want := range map[string]string{
		target.SQLitePath: "original-database",
		filepath.Join(helpers.ConfigDir, "config.yaml"):     "db:\n  engine: sqlite\n",
		filepath.Join(helpers.ConfigDir, "server.crt"):      "original-certificate",
		filepath.Join(helpers.ConfigDir, "logs", "app.log"): "original log\n",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("回滚后缺少文件 %s：%v", path, err)
		}
		if string(content) != want {
			t.Fatalf("回滚后 %s = %q，want %q", path, content, want)
		}
	}
	for _, path := range []string{
		filepath.Join(helpers.ConfigDir, "config.yml"),
		filepath.Join(helpers.ConfigDir, "server.key"),
		filepath.Join(helpers.ConfigDir, "logs", "extra.log"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("回滚必须删除快照外的白名单文件 %s：%v", path, err)
		}
	}

	// 幂等：同一份快照可以被下一次进程启动重复应用。
	if err := snapshot.Rollback(); err != nil {
		t.Fatalf("重复 Rollback() error = %v", err)
	}
}

// TestRestoreSnapshotRollbackRemovesTargetCreatedDuringRestore 覆盖目标原本不存在的场景：
// 回滚意味着让目标回到「不存在」，否则会留下一个半成品数据库。
func TestRestoreSnapshotRollbackRemovesTargetCreatedDuringRestore(t *testing.T) {
	target, targetConfigYAML := setupSnapshotTestConfig(t, false)

	snapshot, err := CreateRestoreSnapshot(testSnapshotOperationID, target, targetConfigYAML)
	if err != nil {
		t.Fatalf("CreateRestoreSnapshot() error = %v", err)
	}
	writeTestFile(t, target.SQLitePath, []byte("restored-database"))
	writeTestFile(t, target.SQLitePath+"-wal", []byte("restored-wal"))

	if err := snapshot.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	for _, path := range append([]string{target.SQLitePath}, sqliteSidecarPaths(target.SQLitePath)...) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("回滚后仍存在 %s：%v", path, err)
		}
	}
}

// TestSqliteRollbackKeepsTargetWhenSnapshotSourceCannotBeRead 覆盖回滚的原子替换边界：
// 快照文件不可读时，当前数据库不能先被删除。
func TestRestoreSnapshotRollbackConfigTreeWrapsWhitelistCleanupError(t *testing.T) {
	helpers.ConfigDir = filepath.Join(t.TempDir(), "missing-config")
	snapshot := &RestoreSnapshot{}

	err := snapshot.rollbackConfigTree()
	if !errors.Is(err, ErrRestoreSnapshotFailed) {
		t.Fatalf("rollbackConfigTree() error = %v, want ErrRestoreSnapshotFailed", err)
	}
}

func TestSqliteRollbackKeepsTargetWhenSnapshotSourceCannotBeRead(t *testing.T) {
	target, targetConfigYAML := setupSnapshotTestConfig(t, true)
	snapshot, err := CreateRestoreSnapshot(testSnapshotOperationID, target, targetConfigYAML)
	if err != nil {
		t.Fatalf("CreateRestoreSnapshot() error = %v", err)
	}
	if err := os.Remove(filepath.Join(snapshot.databaseDir(), snapshotSqliteFileName)); err != nil {
		t.Fatalf("Remove(snapshot database): %v", err)
	}

	if err := snapshot.Rollback(); err == nil {
		t.Fatal("Rollback() should fail when snapshot database is missing")
	}
	content, err := os.ReadFile(target.SQLitePath)
	if err != nil || string(content) != "original-database" {
		t.Fatalf("failed rollback changed current database: %q, %v", content, err)
	}
}

// TestReplaceSqliteDatabaseKeepsTargetWhenRenameFails 覆盖恢复提交的原子切换边界：
// 临时数据库丢失时，已有目标数据库不能被提前删除。
func TestReplaceSqliteDatabaseKeepsTargetWhenRenameFails(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.sqlite")
	writeTestFile(t, target, []byte("original-database"))

	if err := replaceSqliteDatabase(filepath.Join(directory, "missing.sqlite"), target); err == nil {
		t.Fatal("replaceSqliteDatabase() should fail for a missing temporary database")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "original-database" {
		t.Fatalf("failed switch changed current database: %q, %v", content, err)
	}
}

// TestLoadRestoreSnapshotKeepsCredentialsOutOfDescription 覆盖快照描述的脱敏要求：
// 描述文件只保存非敏感标识，连接凭据只存在于目标配置副本中。
func TestLoadRestoreSnapshotKeepsCredentialsOutOfDescription(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "encryption.key"), []byte("instance-key\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yaml"), []byte("db:\n  engine: sqlite\n"))

	const password = "snapshot-secret-password"
	targetConfigYAML := testRestoreConfigYAML(t, testPostgresConfig(password))
	target, err := resolveRestoreTarget(targetConfigYAML)
	if err != nil {
		t.Fatalf("resolveRestoreTarget() error = %v", err)
	}
	// PostgreSQL 目标不可连接时快照必须失败，但描述文件在此之前不得写出连接凭据。
	if _, err := CreateRestoreSnapshot(testSnapshotOperationID, target, targetConfigYAML); err == nil {
		t.Fatal("不可连接的 PostgreSQL 目标必须让快照失败")
	}
	meta, err := os.ReadFile(filepath.Join(snapshotDir(testSnapshotOperationID), snapshotMetaFileName))
	if err == nil && strings.Contains(string(meta), password) {
		t.Fatal("快照描述泄露了数据库密码")
	}

	// SQLite 目标的快照可以完整往返，并且能在新进程中重新解析出同一个目标。
	sqliteTarget, sqliteConfigYAML := setupSnapshotTestConfig(t, true)
	if _, err := CreateRestoreSnapshot(testSnapshotOperationID, sqliteTarget, sqliteConfigYAML); err != nil {
		t.Fatalf("CreateRestoreSnapshot() error = %v", err)
	}
	loaded, err := LoadRestoreSnapshot(testSnapshotOperationID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRestoreSnapshot() = %v, %v", loaded, err)
	}
	if loaded.target.SQLitePath != sqliteTarget.SQLitePath || loaded.meta.TargetLabel != sqliteTarget.Label {
		t.Fatalf("载入的快照目标 = %+v，want %+v", loaded.target, sqliteTarget)
	}
	if err := loaded.Remove(); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(snapshotDir(testSnapshotOperationID)); !os.IsNotExist(err) {
		t.Fatalf("Remove() 之后快照目录仍存在：%v", err)
	}
}

// TestRemoveOtherSnapshotsKeepsOnlyCurrentOperation 覆盖「只保留最近一次操作」：
// 历史快照必须被清理，当前操作的快照必须保留。
func TestRemoveOtherSnapshotsKeepsOnlyCurrentOperation(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	const staleOperationID = "ffeeddccbbaa99887766554433221100"
	for _, operationID := range []string{testSnapshotOperationID, staleOperationID} {
		writeTestFile(t, filepath.Join(snapshotDir(operationID), snapshotMetaFileName), []byte("{}"))
	}

	RemoveOtherSnapshots(testSnapshotOperationID)

	if _, err := os.Stat(snapshotDir(testSnapshotOperationID)); err != nil {
		t.Fatalf("当前操作的快照被误删：%v", err)
	}
	if _, err := os.Stat(snapshotDir(staleOperationID)); !os.IsNotExist(err) {
		t.Fatalf("历史快照未被清理：%v", err)
	}
}
