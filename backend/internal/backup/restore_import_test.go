package backup

import (
	"strings"
	"testing"

	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestImportCatalogJSONLPreservesAPIKeyHashes(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败：%v", err)
	}
	if err := database.AutoMigrate(&models.User{}, &models.ApiKey{}); err != nil {
		t.Fatalf("创建测试表失败：%v", err)
	}
	if err := database.Create(&models.User{BaseModel: models.BaseModel{ID: 1}, Username: "backup-user", Password: "password-hash"}).Error; err != nil {
		t.Fatalf("创建测试用户失败：%v", err)
	}

	entry, found := apiKeyCatalogEntry()
	if !found {
		t.Fatal("备份表目录缺少 api_keys")
	}
	source := strings.NewReader("{\"id\":1,\"created_at\":1,\"updated_at\":1,\"user_id\":1,\"name\":\"first\",\"key_hash\":\"first-hash\",\"key_prefix\":\"qms_firs\",\"last_used_at\":0,\"is_active\":true}\n" +
		"{\"id\":2,\"created_at\":1,\"updated_at\":1,\"user_id\":1,\"name\":\"second\",\"key_hash\":\"second-hash\",\"key_prefix\":\"qms_seco\",\"last_used_at\":0,\"is_active\":true}\n")

	count, err := importCatalogJSONL(database, entry, source)
	if err != nil {
		t.Fatalf("importCatalogJSONL() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("导入记录数 = %d，want 2", count)
	}
	var keys []models.ApiKey
	if err := database.Order("id").Find(&keys).Error; err != nil {
		t.Fatalf("读取导入的 API Key 失败：%v", err)
	}
	if len(keys) != 2 || keys[0].KeyHash != "first-hash" || keys[1].KeyHash != "second-hash" {
		t.Fatalf("导入的 API Key = %+v，必须保留哈希", keys)
	}
}

func apiKeyCatalogEntry() (models.TableCatalogEntry, bool) {
	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		if entry.ID == "api_keys" {
			return entry, true
		}
	}
	return models.TableCatalogEntry{}, false
}
