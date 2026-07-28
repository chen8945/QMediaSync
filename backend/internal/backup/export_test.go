package backup

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// setupExportTestEnvironment 准备一次真实导出所需的最小环境：
// 独立配置目录、本机密钥和已建表的 SQLite 实例。
func setupExportTestEnvironment(t *testing.T) {
	t.Helper()

	originalConfigDir := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = originalConfigDir })
	if err := helpers.InitEncryptionKey(); err != nil {
		t.Fatalf("InitEncryptionKey() error = %v", err)
	}
	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		t.Fatalf("LocalEncryptionKeyText() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(helpers.ConfigDir, "encryption.key"), []byte(keyText), 0o600); err != nil {
		t.Fatalf("写入测试本机密钥失败：%v", err)
	}

	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败：%v", err)
	}
	original := db.Db
	db.Db = testDB
	t.Cleanup(func() { db.Db = original })

	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		if err := db.Db.AutoMigrate(entry.Model); err != nil {
			t.Fatalf("创建表 %s 失败：%v", entry.PhysicalName, err)
		}
	}
}

func TestExportArtifactPreservesAPIKeyHash(t *testing.T) {
	setupExportTestEnvironment(t)

	user := models.User{Username: "backup-user", Password: "password-hash"}
	if err := db.Db.Create(&user).Error; err != nil {
		t.Fatalf("创建测试用户失败：%v", err)
	}
	if err := db.Db.Create(&models.ApiKey{
		UserID:    user.ID,
		Name:      "backup-key",
		KeyHash:   "api-key-hash",
		KeyPrefix: "qms_test",
		IsActive:  true,
	}).Error; err != nil {
		t.Fatalf("创建测试 API Key 失败：%v", err)
	}

	exported, err := exportArtifact(models.BackupTypeManual, nil, nil)
	if err != nil {
		t.Fatalf("exportArtifact() error = %v", err)
	}
	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		t.Fatalf("LocalEncryptionKeyText() error = %v", err)
	}
	verified, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         exported.Path,
		StagingDir:           t.TempDir(),
		CurrentEncryptionKey: []byte(keyText),
	})
	if err != nil {
		t.Fatalf("VerifyArtifact() error = %v", err)
	}
	defer verified.Cleanup()

	line := readFirstArchivedLine(t, verified.InnerArchivePath, "data/api_keys.jsonl")
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("解析 API Key 备份记录失败：%v", err)
	}
	if record["key_hash"] != "api-key-hash" {
		t.Fatalf("备份 API Key 哈希 = %v，必须保留用于恢复认证", record["key_hash"])
	}
}

// TestExportArtifactPublishesVerifiableArtifact 覆盖备份主链路：
// 导出的工件必须能被同一实例验证通过，包含主表目录的全部数据文件和必需的配置白名单，
// 并且排除 .env。
func TestExportArtifactPublishesVerifiableArtifact(t *testing.T) {
	setupExportTestEnvironment(t)
	if err := os.WriteFile(filepath.Join(helpers.ConfigDir, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env): %v", err)
	}
	seeded := models.SyncPath{BaseCid: "导出用例", LocalPath: "/local", RemotePath: "/remote"}
	if err := db.Db.Create(&seeded).Error; err != nil {
		t.Fatalf("写入测试数据失败：%v", err)
	}

	catalog := models.RegularBackupRestoreTableCatalog()
	completed := 0
	total := 0
	exported, err := exportArtifact(models.BackupTypeManual, []byte("BackupPass123"), func(done int, all int, _ string) {
		completed = done
		total = all
	})
	if err != nil {
		t.Fatalf("exportArtifact() error = %v", err)
	}
	if completed != len(catalog) || total != len(catalog) {
		t.Fatalf("进度 = %d/%d, want %d/%d", completed, total, len(catalog), len(catalog))
	}
	if exported.TableCount != len(catalog) {
		t.Fatalf("TableCount = %d, want %d", exported.TableCount, len(catalog))
	}
	if exported.RecordCount < 1 {
		t.Fatalf("RecordCount = %d, want at least the seeded record", exported.RecordCount)
	}
	if filepath.Dir(exported.Path) != ArtifactDir() {
		t.Fatalf("工件发布目录 = %q, want %q", filepath.Dir(exported.Path), ArtifactDir())
	}
	if !strings.HasPrefix(filepath.Base(exported.Path), "backup_"+models.BackupTypeManual+"_") {
		t.Fatalf("工件文件名 = %q, want backup_%s_ 前缀", filepath.Base(exported.Path), models.BackupTypeManual)
	}

	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		t.Fatalf("LocalEncryptionKeyText() error = %v", err)
	}
	verified, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         exported.Path,
		StagingDir:           t.TempDir(),
		Password:             []byte("BackupPass123"),
		CurrentEncryptionKey: []byte(keyText),
	})
	if err != nil {
		t.Fatalf("VerifyArtifact() error = %v", err)
	}
	t.Cleanup(func() {
		if err := verified.Cleanup(); err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
	})

	archived := make(map[string]bool, len(verified.Manifest.Files))
	for _, file := range verified.Manifest.Files {
		archived[file.Path] = true
		if strings.Contains(file.Path, ".env") {
			t.Fatalf("工件包含被排除的 .env：%s", file.Path)
		}
	}
	for _, entry := range catalog {
		if !archived["data/"+entry.ID+".jsonl"] {
			t.Fatalf("缺少表 %s 的数据文件", entry.ID)
		}
	}
	if !archived["config/encryption.key"] {
		t.Fatal("工件必须包含 config/encryption.key，否则恢复端无法做三方指纹校验")
	}
	if verified.Manifest.SchemaVersion != models.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", verified.Manifest.SchemaVersion, models.SchemaVersion)
	}

	line := readFirstArchivedLine(t, verified.InnerArchivePath, "data/"+models.GetTableName(&models.SyncPath{})+".jsonl")
	if !strings.Contains(line, "导出用例") {
		t.Fatalf("数据文件未包含导出的记录：%s", line)
	}
}

// TestExportArtifactUsesSingleConsistentReadView 保护一致读视图：
// 事务开启失败时必须整体失败，不能退化成逐表独立读取。
func TestExportArtifactUsesSingleConsistentReadView(t *testing.T) {
	setupExportTestEnvironment(t)

	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败：%v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭底层连接失败：%v", err)
	}

	if _, err := exportArtifact(models.BackupTypeManual, nil, nil); err == nil {
		t.Fatal("数据库不可用时导出必须失败")
	}
	entries, err := os.ReadDir(ArtifactDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("读取工件目录失败：%v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".zip") {
			t.Fatalf("失败的导出不得在备份目录留下工件：%s", entry.Name())
		}
	}
}

func readFirstArchivedLine(t *testing.T, archivePath string, name string) string {
	t.Helper()

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("打开内层归档失败：%v", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			t.Fatalf("打开 %s 失败：%v", name, err)
		}
		defer opened.Close()

		scanner := bufio.NewScanner(opened)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		if scanner.Scan() {
			return scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		t.Fatalf("%s 为空", name)
	}
	t.Fatalf("内层归档缺少 %s", name)
	return ""
}
