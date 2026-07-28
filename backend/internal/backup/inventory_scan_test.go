package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// setupInventoryScanEnvironment 准备目录清点所需的配置目录、本机密钥与工件索引表。
func setupInventoryScanEnvironment(t *testing.T) {
	t.Helper()
	setupExportTestEnvironment(t)
	if err := db.Db.AutoMigrate(&models.BackupRecord{}); err != nil {
		t.Fatalf("创建备份记录表失败：%v", err)
	}
	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		t.Fatalf("LocalEncryptionKeyText() error = %v", err)
	}
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "encryption.key"), []byte(keyText+"\n"))
	writeTestFile(t, filepath.Join(helpers.ConfigDir, "config.yaml"), testRestoreConfigYAML(t, testSqliteConfig("restored.db")))
	globalInventoryReads.resume()
}

// writeTestLegacyArchive 写出一个旧格式 ZIP：可打开但没有 v1 的 header.json。
func writeTestLegacyArchive(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", path, err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", path, err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	entry, err := writer.Create("SyncPath.json")
	if err != nil {
		t.Fatalf("Create(zip entry) error = %v", err)
	}
	if _, err := entry.Write([]byte("{\"id\":1}\n")); err != nil {
		t.Fatalf("Write(zip entry) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(zip) error = %v", err)
	}
}

// TestInventoryScanClassifiesDirectoryImportsAndUploads 覆盖 D2 的工件分类：
// 未加密 v1 完整验证；加密 v1 只标记待密码验证；损坏工件记为无效但仍进入索引；
// 旧格式标记为旧格式；上传暂存以独立来源进入既有恢复选择。
func TestInventoryScanClassifiesDirectoryImportsAndUploads(t *testing.T) {
	setupInventoryScanEnvironment(t)

	plainArtifact, err := exportArtifact(models.BackupTypeManual, nil, nil)
	if err != nil {
		t.Fatalf("exportArtifact(plain) error = %v", err)
	}
	encryptedArtifact, err := exportArtifact(models.BackupTypeManual, []byte("BackupPassword1"), nil)
	if err != nil {
		t.Fatalf("exportArtifact(encrypted) error = %v", err)
	}
	// 应用生成的记录不参与本用例的目录导入判定，先清空索引再按目录重建。
	if err := db.Db.Where("1 = 1").Delete(&models.BackupRecord{}).Error; err != nil {
		t.Fatalf("清空备份记录失败：%v", err)
	}

	writeTestFile(t, filepath.Join(ArtifactDir(), "corrupt.zip"), []byte("not a zip"))
	writeTestLegacyArchive(t, filepath.Join(ArtifactDir(), "backup_manual_20250101_000000.zip"))
	writeTestFile(t, filepath.Join(ArtifactDir(), "notes.txt"), []byte("ignored"))
	writeTestFile(t, filepath.Join(ArtifactDir(), ".artifact-publishing.zip"), []byte("publishing"))
	// 上传受理写出的候选工件本身以点号开头，必须仍然作为上传暂存进入索引。
	uploadPath := filepath.Join(UploadStagingDir(), ".upload-123456.zip")
	if err := copyFile(plainArtifact.Path, uploadPath); err != nil {
		t.Fatalf("copyFile(upload) error = %v", err)
	}

	read, accepted := BeginInventoryRead()
	if !accepted {
		t.Fatal("BeginInventoryRead() 未被接受")
	}
	if err := runInventoryScan(read); err != nil {
		t.Fatalf("runInventoryScan() error = %v", err)
	}

	records := map[string]models.BackupRecord{}
	stored, err := models.InventoryRecords()
	if err != nil {
		t.Fatalf("InventoryRecords() error = %v", err)
	}
	for _, record := range stored {
		records[filepath.Base(record.InventoryPath)] = record
	}
	if len(records) != 5 {
		t.Fatalf("索引条目数 = %d，want 5：%v", len(records), records)
	}
	if _, indexed := records["notes.txt"]; indexed {
		t.Fatal("非 .zip 文件不应进入索引")
	}
	if _, indexed := records[".artifact-publishing.zip"]; indexed {
		t.Fatal("备份目录中发布中的临时文件不应进入索引")
	}

	plain := records[filepath.Base(plainArtifact.Path)]
	if plain.BackupType != models.BackupTypeImported || plain.VerificationState != models.BackupVerificationVerified {
		t.Fatalf("未加密目录导入 = %+v，want imported/verified", plain)
	}
	encrypted := records[filepath.Base(encryptedArtifact.Path)]
	if encrypted.VerificationState != models.BackupVerificationPendingPassword {
		t.Fatalf("加密目录导入 = %+v，want pending_password", encrypted)
	}
	corrupt := records["corrupt.zip"]
	if corrupt.VerificationState != models.BackupVerificationInvalid || corrupt.VerificationErrorCode == "" {
		t.Fatalf("损坏工件 = %+v，want invalid + 稳定错误码", corrupt)
	}
	legacy := records["backup_manual_20250101_000000.zip"]
	if legacy.Format != models.BackupFormatLegacy || legacy.BackupType != models.BackupTypeImported {
		t.Fatalf("旧格式工件 = %+v，want legacy 格式且只可手动删除", legacy)
	}
	upload := records[".upload-123456.zip"]
	if upload.BackupType != models.BackupTypeTemporaryUpload {
		t.Fatalf("上传暂存 = %+v，want temporary_upload", upload)
	}
	// 目录导入与上传暂存都不得进入保留天数与数量的自动清理。
	for _, record := range []models.BackupRecord{plain, encrypted, corrupt, legacy, upload} {
		if slices.Contains(models.AutoCleanupBackupTypes, record.BackupType) {
			t.Fatalf("%s 的来源 %q 不应参与自动清理", filepath.Base(record.InventoryPath), record.BackupType)
		}
	}
}

// TestInventoryScanRemovesRecordsWithoutFiles 覆盖索引重建：
// 文件已不存在的记录必须被删除，否则列表会残留无法恢复也无法下载的条目。
func TestInventoryScanRemovesRecordsWithoutFiles(t *testing.T) {
	setupInventoryScanEnvironment(t)

	orphan := models.BackupRecord{
		Status:        models.BackupStatusCompleted,
		FilePath:      filepath.Join(ArtifactDir(), "vanished.zip"),
		InventoryPath: filepath.Join(ArtifactDir(), "vanished.zip"),
		BackupType:    models.BackupTypeManual,
		Format:        models.BackupFormatV1,
	}
	if err := db.Db.Create(&orphan).Error; err != nil {
		t.Fatalf("写入孤立记录失败：%v", err)
	}

	read, accepted := BeginInventoryRead()
	if !accepted {
		t.Fatal("BeginInventoryRead() 未被接受")
	}
	if err := runInventoryScan(read); err != nil {
		t.Fatalf("runInventoryScan() error = %v", err)
	}

	stored, err := models.InventoryRecords()
	if err != nil {
		t.Fatalf("InventoryRecords() error = %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("孤立记录未被清理：%+v", stored)
	}
}

// TestInventoryScanStopsWhenOperationTakesOver 覆盖 D2 的失效语义：
// 实际备份或恢复接管协调器后，当前清点轮次立即失效且不得再写入索引。
func TestInventoryScanStopsWhenOperationTakesOver(t *testing.T) {
	setupInventoryScanEnvironment(t)
	if _, err := exportArtifact(models.BackupTypeManual, nil, nil); err != nil {
		t.Fatalf("exportArtifact() error = %v", err)
	}
	if err := db.Db.Where("1 = 1").Delete(&models.BackupRecord{}).Error; err != nil {
		t.Fatalf("清空备份记录失败：%v", err)
	}

	read, accepted := BeginInventoryRead()
	if !accepted {
		t.Fatal("BeginInventoryRead() 未被接受")
	}
	// 模拟协调器取得执行权：本轮立即失效，实际操作不等待清点的磁盘 I/O。
	globalInventoryReads.invalidate()
	t.Cleanup(func() { globalInventoryReads.resume() })

	if err := runInventoryScan(read); !errors.Is(err, ErrInventoryReadInvalidated) {
		t.Fatalf("runInventoryScan() error = %v, want ErrInventoryReadInvalidated", err)
	}
	stored, err := models.InventoryRecords()
	if err != nil {
		t.Fatalf("InventoryRecords() error = %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("失效轮次不得写入索引：%+v", stored)
	}
	if CurrentInventoryStatus() != InventoryStatusReady {
		t.Fatalf("CurrentInventoryStatus() = %q，未运行的清点应为 ready", CurrentInventoryStatus())
	}
}
