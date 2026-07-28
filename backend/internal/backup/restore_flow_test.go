package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestorePreflightResultUsesSnakeCaseAPIFields(t *testing.T) {
	encoded, err := json.Marshal(RestorePreflightResult{
		PreflightID: "preflight-id",
		ExpiresAt:   123,
		TargetLabel: "sqlite:restored.db",
	})
	if err != nil {
		t.Fatalf("json.Marshal(RestorePreflightResult) error = %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("json.Unmarshal(RestorePreflightResult) error = %v", err)
	}
	if result["preflight_id"] != "preflight-id" || result["target_label"] != "sqlite:restored.db" {
		t.Fatalf("恢复预检响应字段 = %v，必须使用前端契约的 snake_case", result)
	}
	if _, exists := result["PreflightID"]; exists {
		t.Fatalf("恢复预检响应不应包含 Go 字段名：%v", result)
	}
	if _, exists := result["encrypted"]; exists {
		t.Fatalf("恢复预检响应不应包含未使用字段：%v", result)
	}
}

// setupRestoreFlowEnvironment 导出一个可恢复的真实工件，并让当前实例指向另一个数据库目标。
// 它同时覆盖「恢复目标由备份携带的配置决定」这一语义：当前连接的数据库不得被写入。
func setupRestoreFlowEnvironment(t *testing.T, password []byte) ExportedArtifact {
	t.Helper()
	setupExportTestEnvironment(t)

	// 本机密钥可能已被同一测试二进制的其他用例初始化，这里把有效密钥写入当前配置目录，
	// 保证工件指纹与当前实例一致。
	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		t.Fatalf("LocalEncryptionKeyText() error = %v", err)
	}
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "encryption.key"), []byte(keyText+"\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yaml"), testRestoreConfigYAML(t, testSqliteConfig("restored.db")))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "server.crt"), []byte("artifact-certificate"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "logs", "app.log"), []byte("artifact log\n"))

	seeded := models.SyncPath{BaseCid: "恢复用例", LocalPath: "/local", RemotePath: "/remote"}
	if err := db.Db.Create(&seeded).Error; err != nil {
		t.Fatalf("写入测试数据失败：%v", err)
	}
	session := models.UserSession{SessionID: "session-1", TokenID: "token-1"}
	if err := db.Db.Create(&session).Error; err != nil {
		t.Fatalf("写入测试会话失败：%v", err)
	}

	exported, err := exportArtifact(models.BackupTypeManual, password, nil)
	if err != nil {
		t.Fatalf("exportArtifact() error = %v", err)
	}

	// 当前实例连接的是另一个 SQLite 文件，恢复不得写入它。
	originalConfig := helpers.GlobalConfig
	helpers.GlobalConfig.Db.Engine = helpers.DbEngineSqlite
	helpers.GlobalConfig.Db.SqliteFile = "current.db"
	t.Cleanup(func() { helpers.GlobalConfig = originalConfig })
	return exported
}

// TestPreflightRestoreIssuesOneTimeCredentialWithoutTouchingData 覆盖 D4 的第一阶段契约：
// 预检完整验证工件并返回不含密码的目标标识与固定 30 分钟有效期的一次性 preflight_id，
// 且不进入维护、不创建快照、不写入任何数据库。
func TestPreflightRestoreIssuesOneTimeCredentialWithoutTouchingData(t *testing.T) {
	exported := setupRestoreFlowEnvironment(t, nil)

	issuedAt := time.Now().Unix()
	result, err := PreflightRestore(RestorePreflightRequest{
		Kind:         PreflightSourceRecord,
		RecordID:     1,
		ArtifactPath: exported.Path,
	})
	if err != nil {
		t.Fatalf("PreflightRestore() error = %v", err)
	}
	if result.TargetLabel != "sqlite:restored.db" {
		t.Fatalf("TargetLabel = %q, want sqlite:restored.db", result.TargetLabel)
	}
	if delta := result.ExpiresAt - issuedAt; delta < int64(PreflightValidity.Seconds())-5 || delta > int64(PreflightValidity.Seconds())+5 {
		t.Fatalf("预检有效期 = %d 秒，want %v", delta, PreflightValidity)
	}
	if _, err := os.Stat(filepath.Join(helpers.ConfigDir, "restored.db")); !os.IsNotExist(err) {
		t.Fatalf("预检不得创建恢复目标数据库：%v", err)
	}
	if InMaintenance() {
		t.Fatal("预检不得进入维护屏障")
	}

	artifactSHA256, err := fileSHA256(exported.Path)
	if err != nil {
		t.Fatalf("fileSHA256() error = %v", err)
	}
	source := PreflightSource{
		Kind:           PreflightSourceRecord,
		RecordID:       1,
		ArtifactPath:   exported.Path,
		ArtifactSHA256: artifactSHA256,
	}
	if _, err := ConsumePreflight(result.PreflightID, source); err != nil {
		t.Fatalf("ConsumePreflight() error = %v", err)
	}
	if _, err := ConsumePreflight(result.PreflightID, source); !errors.Is(err, ErrPreflightInvalid) {
		t.Fatalf("重放 ConsumePreflight() error = %v, want ErrPreflightInvalid", err)
	}
}

// TestPreflightRestoreRejectsWrongPasswordAndDropsUploadSource 覆盖 R3：
// 密码错误统一以「密码错误或工件损坏」结束，且失败的上传源文件立即删除。
func TestPreflightRestoreRejectsWrongPasswordAndDropsUploadSource(t *testing.T) {
	exported := setupRestoreFlowEnvironment(t, []byte("CorrectBackupPass1"))

	staged := filepath.Join(UploadStagingDir(), "uploaded.zip")
	if err := os.MkdirAll(UploadStagingDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(upload staging) error = %v", err)
	}
	if err := copyFile(exported.Path, staged); err != nil {
		t.Fatalf("copyFile() error = %v", err)
	}

	_, err := PreflightRestore(RestorePreflightRequest{
		Kind:         PreflightSourceUpload,
		ArtifactPath: staged,
		Password:     []byte("WrongBackupPass1"),
	})
	if !errors.Is(err, ErrArtifactPasswordOrCorrupt) {
		t.Fatalf("PreflightRestore() error = %v, want ErrArtifactPasswordOrCorrupt", err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("预检失败必须删除上传源文件：%v", err)
	}
}

// TestApplyRestoreArtifactSwitchesTargetAndMirrorsWhitelist 覆盖 D3 的提交语义：
// 数据写入备份配置指定的目标而非当前数据库，浏览器会话被失效，
// 白名单文件树按清单精确镜像（含删除工件中不存在的同类文件），并留下阶段日志。
func TestApplyRestoreArtifactSwitchesTargetAndMirrorsWhitelist(t *testing.T) {
	exported := setupRestoreFlowEnvironment(t, nil)

	// 模拟恢复前的现场：证书被删除、日志多出一个文件、配置被改写。
	if err := os.Remove(filepath.Join(helpers.ConfigDir, "server.crt")); err != nil {
		t.Fatalf("Remove(server.crt) error = %v", err)
	}
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "logs", "stale.log"), []byte("stale\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yaml"), []byte("db:\n  engine: postgres\n"))

	verified, _, err := verifyRestoreArtifact(exported.Path, nil)
	if err != nil {
		t.Fatalf("verifyRestoreArtifact() error = %v", err)
	}
	defer verified.Cleanup()
	target, _, err := resolveConfirmedRestoreTarget(verified)
	if err != nil {
		t.Fatalf("resolveConfirmedRestoreTarget() error = %v", err)
	}

	coordinator, err := NewOperationCoordinator(t.TempDir())
	if err != nil {
		t.Fatalf("NewOperationCoordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(OperationKindRestore, true)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := applyRestoreArtifact(coordinator, grant.OperationID, target, verified, nil); err != nil {
		t.Fatalf("applyRestoreArtifact() error = %v", err)
	}

	restored, err := openSqliteDatabase(target.SQLitePath)
	if err != nil {
		t.Fatalf("openSqliteDatabase(target) error = %v", err)
	}
	defer closeSqliteDatabase(restored)
	var syncPaths []models.SyncPath
	if err := restored.Find(&syncPaths).Error; err != nil {
		t.Fatalf("读取恢复后的数据失败：%v", err)
	}
	if len(syncPaths) != 1 || syncPaths[0].BaseCid != "恢复用例" {
		t.Fatalf("恢复后的数据 = %+v，want 1 条种子记录", syncPaths)
	}
	var sessionCount int64
	if err := restored.Model(&models.UserSession{}).Count(&sessionCount).Error; err != nil {
		t.Fatalf("统计会话失败：%v", err)
	}
	if sessionCount != 0 {
		t.Fatalf("恢复后仍有 %d 个浏览器会话，必须全部失效", sessionCount)
	}

	if _, err := os.Stat(filepath.Join(helpers.ConfigDir, "current.db")); !os.IsNotExist(err) {
		t.Fatalf("当前连接的数据库不得被写入：%v", err)
	}
	certificate, err := os.ReadFile(filepath.Join(helpers.ConfigDir, "server.crt"))
	if err != nil || string(certificate) != "artifact-certificate" {
		t.Fatalf("证书未按清单恢复：%q, %v", certificate, err)
	}
	if _, err := os.Stat(filepath.Join(helpers.ConfigDir, "logs", "stale.log")); !os.IsNotExist(err) {
		t.Fatalf("工件中不存在的日志必须被删除：%v", err)
	}
	configYAML, err := os.ReadFile(filepath.Join(helpers.ConfigDir, "config.yaml"))
	if err != nil || string(configYAML) == "db:\n  engine: postgres\n" {
		t.Fatalf("配置未按清单恢复：%q, %v", configYAML, err)
	}

	phases, err := coordinator.Phases(grant.OperationID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	for _, phase := range []OperationPhase{
		OperationPhaseDatabaseSwitched,
		OperationPhaseConfigSwitched,
		OperationPhaseLogsSwitched,
	} {
		if !hasPhase(phases, phase) {
			t.Fatalf("缺少阶段日志 %s", phase)
		}
	}
}

func TestMirrorWhitelistFilesKeepsDistinctLogPathsDistinct(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "encryption.key"), []byte("current-key"))

	key := []byte("current-key")
	files := newTestInnerFiles(t, key)
	files["config/logs/a_b.log"] = []byte("flat log\n")
	files["config/logs/a/b.log"] = []byte("nested log\n")
	innerArchivePath := filepath.Join(t.TempDir(), "inner.zip")
	writeTestInnerArchive(t, innerArchivePath, newTestArtifactManifest(), files)

	artifactPath := buildTestArtifact(t, innerArchivePath, key)
	verified, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         artifactPath,
		StagingDir:           t.TempDir(),
		CurrentEncryptionKey: key,
	})
	if err != nil {
		t.Fatalf("VerifyArtifact() error = %v", err)
	}
	defer verified.Cleanup()

	reader, err := openInnerArchive(verified.InnerArchivePath, verified.Manifest)
	if err != nil {
		t.Fatalf("openInnerArchive() error = %v", err)
	}
	defer reader.Close()
	if err := mirrorWhitelistFiles(reader, true); err != nil {
		t.Fatalf("mirrorWhitelistFiles() error = %v", err)
	}

	for path, want := range map[string]string{
		filepath.Join(helpers.ConfigDir, "logs", "a_b.log"):    "flat log\n",
		filepath.Join(helpers.ConfigDir, "logs", "a", "b.log"): "nested log\n",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if string(content) != want {
			t.Fatalf("日志 %s = %q, want %q", path, content, want)
		}
	}
}

// TestConvergeInterruptedRestoreRollsBackAndKeepsServiceUsable 覆盖启动期收敛：
// 快照就绪后中断的恢复必须被幂等回滚，终态明确区分「恢复失败」与「已自动回滚」，
// 回滚成功后允许继续启动业务。
func TestConvergeInterruptedRestoreRollsBackAndKeepsServiceUsable(t *testing.T) {
	target, targetConfigYAML := setupSnapshotTestConfig(t, true)
	coordinator, grant := startInterruptedRestore(t, target, targetConfigYAML, true)

	writeTestFile(t, target.SQLitePath, []byte("half-restored-database"))
	convergence := convergeInterruptedRestore(coordinator, *coordinator.Active())

	if !convergence.RolledBack || convergence.DiagnosticsOnly || !convergence.ConfigRestored {
		t.Fatalf("convergence = %+v，want 已回滚且允许启动业务", convergence)
	}
	content, err := os.ReadFile(target.SQLitePath)
	if err != nil || string(content) != "original-database" {
		t.Fatalf("回滚后的数据库 = %q, %v", content, err)
	}
	terminal := coordinator.LatestTerminal()
	if terminal == nil || terminal.State != OperationStateFailed || terminal.RollbackState != RollbackStateSucceeded {
		t.Fatalf("terminal = %+v，want 恢复失败且已自动回滚", terminal)
	}
	if _, err := os.Stat(snapshotDir(grant.OperationID)); !os.IsNotExist(err) {
		t.Fatalf("终态确认后必须删除预恢复快照：%v", err)
	}
}

// TestConvergeInterruptedRestoreWithoutSnapshotEntersDiagnosticsOnly 覆盖回滚失败：
// 没有可用快照时不得启动业务运行时，只保留状态诊断能力。
func TestConvergeInterruptedRestoreWithoutSnapshotEntersDiagnosticsOnly(t *testing.T) {
	target, targetConfigYAML := setupSnapshotTestConfig(t, true)
	coordinator, _ := startInterruptedRestore(t, target, targetConfigYAML, false)
	t.Cleanup(func() { diagnosticsOnly.Store(false) })

	convergence := convergeInterruptedRestore(coordinator, *coordinator.Active())

	if !convergence.DiagnosticsOnly || convergence.RolledBack {
		t.Fatalf("convergence = %+v，want 仅诊断模式", convergence)
	}
	if !DiagnosticsOnly() || !InMaintenance() {
		t.Fatal("仅诊断模式必须维持维护屏障，业务 API 不可用")
	}
	terminal := coordinator.LatestTerminal()
	if terminal == nil || terminal.RollbackState != RollbackStateFailed {
		t.Fatalf("terminal = %+v，want 回滚失败", terminal)
	}
}

// TestRecoverRestoreOperationPanicRollsBackReadySnapshot 覆盖提交 goroutine 异常：
// 快照已就绪时不能仅标记失败，必须先还原恢复前的数据。
func TestRecoverRestoreOperationPanicRollsBackReadySnapshot(t *testing.T) {
	target, targetConfigYAML := setupSnapshotTestConfig(t, true)
	coordinator, grant := startInterruptedRestore(t, target, targetConfigYAML, true)
	writeTestFile(t, target.SQLitePath, []byte("partially-restored-database"))

	SetOrderlyExitHook(func() {})
	t.Cleanup(func() { SetOrderlyExitHook(nil) })
	recoverRestoreOperationPanic(coordinator, grant.OperationID, mustLoadRestoreSnapshot(t, grant.OperationID))

	content, err := os.ReadFile(target.SQLitePath)
	if err != nil || string(content) != "original-database" {
		t.Fatalf("panic recovery did not restore database: %q, %v", content, err)
	}
	terminal := coordinator.LatestTerminal()
	if terminal == nil || terminal.State != OperationStateFailed || terminal.RollbackState != RollbackStateSucceeded {
		t.Fatalf("terminal = %+v, want failed restore with successful rollback", terminal)
	}
}

func mustLoadRestoreSnapshot(t *testing.T, operationID string) *RestoreSnapshot {
	t.Helper()
	snapshot, err := LoadRestoreSnapshot(operationID)
	if err != nil || snapshot == nil {
		t.Fatalf("LoadRestoreSnapshot() = %v, %v", snapshot, err)
	}
	return snapshot
}

// startInterruptedRestore 构造一个「快照就绪后中断」的恢复现场。
func startInterruptedRestore(
	t *testing.T,
	target RestoreTarget,
	targetConfigYAML []byte,
	withSnapshot bool,
) (*OperationCoordinator, OperationGrant) {
	t.Helper()
	coordinator, err := NewOperationCoordinator(t.TempDir())
	if err != nil {
		t.Fatalf("NewOperationCoordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(OperationKindRestore, true)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	for _, state := range []OperationState{
		OperationStateWaitingForTasks,
		OperationStateValidating,
		OperationStateRunning,
	} {
		if err := coordinator.Transition(grant.OperationID, OperationTransition{State: state}); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	for _, phase := range []OperationPhase{OperationPhaseValidated, OperationPhaseSnapshotReady} {
		if err := coordinator.RecordPhase(grant.OperationID, phase); err != nil {
			t.Fatalf("RecordPhase(%s) error = %v", phase, err)
		}
	}
	if withSnapshot {
		if _, err := CreateRestoreSnapshot(grant.OperationID, target, targetConfigYAML); err != nil {
			t.Fatalf("CreateRestoreSnapshot() error = %v", err)
		}
	}
	return coordinator, grant
}
