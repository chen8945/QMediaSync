package migrate

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPreflightMigrationArchiveRejectsMissingCatalogTable(t *testing.T) {
	catalog := testMigrationCatalog()
	path := filepath.Join(t.TempDir(), "migrate.zip")
	writeMigrationArchiveFixture(t, path, migrationManifest{
		Format:  migrationArchiveFormat,
		Version: migrationArchiveVersion,
	}, nil)

	_, err := openMigrationArchive(path, catalog)
	if !errors.Is(err, ErrInvalidMigrationArchive) {
		t.Fatalf("openMigrationArchive() error = %v, want ErrInvalidMigrationArchive", err)
	}
}

func TestPreflightMigrationArchiveRejectsCatalogNameDrift(t *testing.T) {
	catalog := testMigrationCatalog()
	data := map[string][]byte{
		migrationTablePath(catalog[0]): migrationUserJSONL(t, 1, "migrated"),
	}
	manifest := testMigrationManifest(catalog, data)
	manifest.Tables[0].PhysicalName = "users_drifted"
	path := filepath.Join(t.TempDir(), "migrate.zip")
	writeMigrationArchiveFixture(t, path, manifest, data)

	_, err := openMigrationArchive(path, catalog)
	if !errors.Is(err, ErrInvalidMigrationArchive) {
		t.Fatalf("openMigrationArchive() error = %v, want ErrInvalidMigrationArchive", err)
	}
}

func TestPreflightMigrationArchiveRejectsMalformedJSONL(t *testing.T) {
	catalog := testMigrationCatalog()
	data := map[string][]byte{
		migrationTablePath(catalog[0]): []byte("{not-json}\n"),
	}
	path := filepath.Join(t.TempDir(), "migrate.zip")
	writeMigrationArchiveFixture(t, path, testMigrationManifest(catalog, data), data)

	_, err := openMigrationArchive(path, catalog)
	if !errors.Is(err, ErrInvalidMigrationArchive) {
		t.Fatalf("openMigrationArchive() error = %v, want ErrInvalidMigrationArchive", err)
	}
}

func TestPreflightMigrationArchiveRejectsMissingPersistedColumn(t *testing.T) {
	catalog := testMigrationCatalog()
	modelSchema, err := migrationModelSchema(models.User{})
	if err != nil {
		t.Fatalf("读取用户模型结构失败: %v", err)
	}
	record := migrationRecordMap(reflect.ValueOf(models.User{
		BaseModel:    models.BaseModel{ID: 1},
		SingletonKey: 1,
		Username:     "migrated",
		Password:     "hash",
	}), modelSchema)
	delete(record, "two_factor_secret")
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("编码测试记录失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "migrate.zip")
	files := map[string][]byte{migrationTablePath(catalog[0]): append(data, '\n')}
	writeMigrationArchiveFixture(t, path, testMigrationManifest(catalog, files), files)

	_, err = openMigrationArchive(path, catalog)
	if !errors.Is(err, ErrInvalidMigrationArchive) {
		t.Fatalf("openMigrationArchive() error = %v, want ErrInvalidMigrationArchive", err)
	}
}

func TestImportMigrationArchiveRollsBackOnInsertFailureAndRetainsPackage(t *testing.T) {
	catalog := testMigrationCatalog()
	target := openMigrationTestDatabase(t)
	if err := target.AutoMigrate(catalog[0].Model); err != nil {
		t.Fatalf("迁移目标测试表失败: %v", err)
	}
	if err := target.Create(&models.User{Username: "before", Password: "hash"}).Error; err != nil {
		t.Fatalf("创建迁移前数据失败: %v", err)
	}

	path := filepath.Join(t.TempDir(), "migrate.zip")
	data := map[string][]byte{
		migrationTablePath(catalog[0]): append(migrationUserJSONL(t, 10, "duplicated"), migrationUserJSONL(t, 10, "duplicated")...),
	}
	writeMigrationArchiveFixture(t, path, testMigrationManifest(catalog, data), data)

	if err := importMigrationArchive(path, target, catalog, nil, nil); err == nil {
		t.Fatal("importMigrationArchive() error = nil, want insert failure")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("导入失败必须保留迁移包: %v", err)
	}
	var users []models.User
	if err := target.Order("id").Find(&users).Error; err != nil {
		t.Fatalf("读取回滚后的用户失败: %v", err)
	}
	if len(users) != 1 || users[0].Username != "before" {
		t.Fatalf("插入失败后目标数据 = %+v, want 保留迁移前用户", users)
	}
}

func TestMigrationArchiveRoundTripPublishesOnlyAfterSuccessfulImport(t *testing.T) {
	catalog := testMigrationCatalog()
	source := openMigrationTestDatabase(t)
	if err := source.AutoMigrate(catalog[0].Model); err != nil {
		t.Fatalf("迁移源测试表失败: %v", err)
	}
	if err := source.Create(&models.User{
		BaseModel:              models.BaseModel{ID: 7},
		Username:               "migrated",
		Password:               "hash",
		TwoFactorSecret:        "encrypted-secret",
		TwoFactorPendingSecret: "pending-secret",
	}).Error; err != nil {
		t.Fatalf("创建迁移源数据失败: %v", err)
	}

	path := filepath.Join(t.TempDir(), "migrate.zip")
	if err := writeMigrationArchive(path, source, catalog, nil); err != nil {
		t.Fatalf("writeMigrationArchive() error = %v", err)
	}
	if err := preflightMigrationArchive(path, catalog); err != nil {
		t.Fatalf("迁移包预检失败: %v", err)
	}

	target := openMigrationTestDatabase(t)
	if err := target.AutoMigrate(catalog[0].Model); err != nil {
		t.Fatalf("迁移目标测试表失败: %v", err)
	}
	if err := target.Create(&models.User{Username: "before", Password: "hash"}).Error; err != nil {
		t.Fatalf("创建迁移前数据失败: %v", err)
	}
	if err := importMigrationArchive(path, target, catalog, nil, nil); err != nil {
		t.Fatalf("importMigrationArchive() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("成功导入后应删除迁移包，stat error = %v", err)
	}

	var users []models.User
	if err := target.Order("id").Find(&users).Error; err != nil {
		t.Fatalf("读取迁移结果失败: %v", err)
	}
	if len(users) != 1 || users[0].ID != 7 || users[0].Username != "migrated" ||
		users[0].TwoFactorSecret != "encrypted-secret" || users[0].TwoFactorPendingSecret != "pending-secret" {
		t.Fatalf("迁移结果 = %+v, want ID=7 username=migrated", users)
	}
}

func TestMigrationArchiveImportsForeignKeyRelatedTablesInCatalogOrder(t *testing.T) {
	catalog := migrationCatalogEntries(t, "users", "api_keys")
	source := openMigrationTestDatabase(t)
	if err := source.AutoMigrate(catalog[0].Model, catalog[1].Model); err != nil {
		t.Fatalf("迁移源测试表失败: %v", err)
	}
	if err := source.Create(&models.User{BaseModel: models.BaseModel{ID: 7}, Username: "migrated", Password: "hash"}).Error; err != nil {
		t.Fatalf("创建迁移源用户失败: %v", err)
	}
	if err := source.Create(&models.ApiKey{BaseModel: models.BaseModel{ID: 8}, UserID: 7, Name: "migrated", KeyHash: "migrated"}).Error; err != nil {
		t.Fatalf("创建迁移源 API Key 失败: %v", err)
	}

	path := filepath.Join(t.TempDir(), "migrate.zip")
	if err := writeMigrationArchive(path, source, catalog, nil); err != nil {
		t.Fatalf("writeMigrationArchive() error = %v", err)
	}

	target := openMigrationTestDatabase(t)
	if err := target.AutoMigrate(catalog[0].Model, catalog[1].Model); err != nil {
		t.Fatalf("迁移目标测试表失败: %v", err)
	}
	if !target.Migrator().HasConstraint(&models.ApiKey{}, "User") {
		t.Fatal("迁移测试需要 api_keys.user_id 外键")
	}
	if err := target.Create(&models.User{Username: "before", Password: "hash"}).Error; err != nil {
		t.Fatalf("创建迁移前用户失败: %v", err)
	}
	if err := target.Create(&models.ApiKey{UserID: 1, Name: "before", KeyHash: "before"}).Error; err != nil {
		t.Fatalf("创建迁移前 API Key 失败: %v", err)
	}

	if err := importMigrationArchive(path, target, catalog, nil, nil); err != nil {
		t.Fatalf("importMigrationArchive() error = %v", err)
	}
	var apiKey models.ApiKey
	if err := target.First(&apiKey, 8).Error; err != nil || apiKey.UserID != 7 || apiKey.KeyHash != "migrated" {
		t.Fatalf("迁移后的 API Key = %+v, error = %v", apiKey, err)
	}
}

func TestImportMigrationArchiveRetainsPackageWhenPostImportUpgradeFails(t *testing.T) {
	catalog := testMigrationCatalog()
	path := filepath.Join(t.TempDir(), "migrate.zip")
	data := map[string][]byte{
		migrationTablePath(catalog[0]): migrationUserJSONL(t, 7, "migrated"),
	}
	writeMigrationArchiveFixture(t, path, testMigrationManifest(catalog, data), data)

	target := openMigrationTestDatabase(t)
	if err := importMigrationArchive(path, target, catalog, nil, func() error {
		return errors.New("version upgrade failed")
	}); err == nil {
		t.Fatal("importMigrationArchive() error = nil, want post-import upgrade failure")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("升级失败必须保留迁移包: %v", err)
	}
}

func preflightMigrationArchive(path string, catalog []models.TableCatalogEntry) error {
	archive, err := openMigrationArchive(path, catalog)
	if err != nil {
		return err
	}
	return archive.Close()
}

func testMigrationCatalog() []models.TableCatalogEntry {
	return []models.TableCatalogEntry{{
		ID:           "users",
		Model:        models.User{},
		PhysicalName: "users",
		ImportOrder:  0,
	}}
}

func migrationCatalogEntries(t *testing.T, ids ...string) []models.TableCatalogEntry {
	t.Helper()
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	entries := make([]models.TableCatalogEntry, 0, len(ids))
	for _, entry := range models.SQLitePostgresMigrationTableCatalog() {
		if _, ok := wanted[entry.ID]; ok {
			entries = append(entries, entry)
		}
	}
	if len(entries) != len(wanted) {
		t.Fatalf("迁移目录中未找到所需表：%v", ids)
	}
	return entries
}

func openMigrationTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := database.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("启用 SQLite 外键失败: %v", err)
	}
	return database
}

func migrationUserJSONL(t *testing.T, id uint, username string) []byte {
	t.Helper()
	modelSchema, err := migrationModelSchema(models.User{})
	if err != nil {
		t.Fatalf("读取用户模型结构失败: %v", err)
	}
	data, err := json.Marshal(migrationRecordMap(reflect.ValueOf(models.User{
		BaseModel:    models.BaseModel{ID: id},
		SingletonKey: 1,
		Username:     username,
		Password:     "hash",
	}), modelSchema))
	if err != nil {
		t.Fatalf("编码迁移测试用户失败: %v", err)
	}
	return append(data, '\n')
}

func testMigrationManifest(catalog []models.TableCatalogEntry, files map[string][]byte) migrationManifest {
	manifest := migrationManifest{
		Format:  migrationArchiveFormat,
		Version: migrationArchiveVersion,
		Tables:  make([]migrationManifestTable, 0, len(catalog)),
	}
	for _, entry := range catalog {
		path := migrationTablePath(entry)
		content := files[path]
		digest := sha256.Sum256(content)
		manifest.Tables = append(manifest.Tables, migrationManifestTable{
			ID:           entry.ID,
			PhysicalName: entry.PhysicalName,
			ImportOrder:  entry.ImportOrder,
			Path:         path,
			SHA256:       hex.EncodeToString(digest[:]),
			RecordCount:  int64(len(splitMigrationJSONL(content))),
		})
	}
	return manifest
}

func splitMigrationJSONL(content []byte) [][]byte {
	var lines [][]byte
	for start := 0; start < len(content); {
		end := start
		for end < len(content) && content[end] != '\n' {
			end++
		}
		if end > start {
			lines = append(lines, content[start:end])
		}
		start = end + 1
	}
	return lines
}

func writeMigrationArchiveFixture(t *testing.T, path string, manifest migrationManifest, files map[string][]byte) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建迁移测试包失败: %v", err)
	}
	writer := zip.NewWriter(output)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		output.Close()
		t.Fatalf("编码迁移测试清单失败: %v", err)
	}
	writeMigrationArchiveFixtureEntry(t, writer, migrationManifestPath, manifestJSON)
	for name, content := range files {
		writeMigrationArchiveFixtureEntry(t, writer, name, content)
	}
	if err := writer.Close(); err != nil {
		output.Close()
		t.Fatalf("关闭迁移测试包失败: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("关闭迁移测试文件失败: %v", err)
	}
}

func writeMigrationArchiveFixtureEntry(t *testing.T, writer *zip.Writer, name string, content []byte) {
	t.Helper()
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatalf("创建迁移测试包条目 %s 失败: %v", name, err)
	}
	if _, err := entry.Write(content); err != nil {
		t.Fatalf("写入迁移测试包条目 %s 失败: %v", name, err)
	}
}
