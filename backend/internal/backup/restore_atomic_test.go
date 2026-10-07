package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
)

type restoreStoredValues struct {
	ID        uint   `gorm:"primaryKey"`
	Enabled   bool   `gorm:"default:true"`
	Count     int    `gorm:"default:7"`
	Hidden    string `json:"-"`
	Note      string
	Number    int64
	UpdatedAt int64 `gorm:"autoUpdateTime"`
	Timestamp time.Time
}

func logicalArchiveForRestore(t *testing.T, conn *gorm.DB) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeLogicalBackup(conn, dir, nil); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if err := helpers.ZipDir(dir, archive); err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestRestorePreservesDatabaseZeroNullAndHiddenValues(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{&restoreStoredValues{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 7, 12, 34, 56, 123456000, time.UTC)
	const id = uint(9007199254740993)
	if err := conn.Table("restore_stored_values").Create(map[string]any{
		"id": id, "enabled": false, "count": 0, "hidden": "private persisted value",
		"note": nil, "number": int64(math.MaxInt64), "updated_at": int64(0), "timestamp": when,
	}).Error; err != nil {
		t.Fatal(err)
	}
	archive := logicalArchiveForRestore(t, conn)
	if err := conn.Exec("DELETE FROM restore_stored_values").Error; err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	var got restoreStoredValues
	if err := conn.First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Enabled || got.Count != 0 || got.Hidden != "private persisted value" || got.Number != math.MaxInt64 || got.UpdatedAt != 0 || !got.Timestamp.Equal(when) {
		t.Fatalf("stored values changed during restore: %+v", got)
	}
	var note sql.NullString
	if err := conn.Raw("SELECT note FROM restore_stored_values WHERE id = ?", id).Row().Scan(&note); err != nil || note.Valid {
		t.Fatalf("SQL NULL was not preserved: %+v %v", note, err)
	}
}

func TestRestoreAuthenticationAndSessionPolicy(t *testing.T) {
	testRestoreAuthenticationAndSessionPolicy(t, setupBackupTest(t))
}

func testRestoreAuthenticationAndSessionPolicy(t *testing.T, conn *gorm.DB) {
	t.Helper()
	// 注册顺序有意把引用表放在用户之前，恢复必须自己解析外键依赖。
	models.AllTables = []any{&models.ApiKey{}, &models.UserSession{}, &models.User{}, &models.UploadSession{}}
	if conn.Dialector.Name() == "sqlite" {
		if err := conn.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	secret, err := helpers.EncryptLocalSecret("restore-totp-secret")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := helpers.EncryptLocalSecret("restore-pending-secret")
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{BaseModel: models.BaseModel{ID: 5}, Username: "restore-admin", Password: "password-hash", TwoFactorEnabled: true, TwoFactorSecret: secret, TwoFactorPendingSecret: pending}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	key := models.ApiKey{UserID: user.ID, Name: "test key", KeyHash: strings.Repeat("a", 64), KeyPrefix: "qms_example"}
	if err := conn.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&key).UpdateColumn("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	session := models.UserSession{UserID: user.ID, Username: user.Username, SessionID: "browser", TokenID: "browser-token", CSRFTokenHash: "browser-csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := conn.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	upload := models.UploadSession{UploadTaskId: 42, UploadId: "oss-multipart-checkpoint", UploadedParts: 3}
	if err := conn.Create(&upload).Error; err != nil {
		t.Fatal(err)
	}
	archive := logicalArchiveForRestore(t, conn)
	if err := conn.Model(&user).Updates(map[string]any{"two_factor_secret": "changed", "two_factor_pending_secret": "changed"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	var restoredUser models.User
	if err := conn.First(&restoredUser, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !restoredUser.TwoFactorEnabled || restoredUser.TwoFactorSecret != secret || restoredUser.TwoFactorPendingSecret != pending {
		t.Fatal("TOTP state or encrypted secrets were lost")
	}
	var restoredKey models.ApiKey
	if err := conn.First(&restoredKey, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredKey.KeyHash != key.KeyHash || restoredKey.UserID != user.ID || restoredKey.IsActive {
		t.Fatal("API key hash, owner or disabled state changed")
	}
	var count int64
	if err := conn.Model(&models.UserSession{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("browser sessions were restored or retained: %d %v", count, err)
	}
	var restoredUpload models.UploadSession
	if err := conn.First(&restoredUpload).Error; err != nil || restoredUpload.UploadId != upload.UploadId || restoredUpload.UploadedParts != upload.UploadedParts {
		t.Fatalf("business upload checkpoint was lost: %+v %v", restoredUpload, err)
	}
	if !conn.Migrator().HasConstraint(&models.ApiKey{}, "User") {
		t.Fatal("API key user foreign key disappeared")
	}
	if err := conn.Model(&models.ApiKey{}).Where("id = ?", key.ID).UpdateColumn("user_id", 999999).Error; err == nil {
		t.Fatal("restored foreign key accepts an absent user")
	}
}

func TestRestoreLegacyIgnoresSessionFilesAndPreservesUploadSessions(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{&backupTestItem{}, &models.UserSession{}, &models.UploadSession{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.UserSession{SessionID: "existing", TokenID: "existing-token"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.UploadSession{UploadTaskId: 123, UploadId: "keep-resume-checkpoint"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json": "{\"ID\":1,\"Name\":\"restored\"}\n",
		"UserSession.json":    "this old session file must be ignored even if malformed\n",
	}, zip.Deflate)
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	var sessions int64
	if err := conn.Model(&models.UserSession{}).Count(&sessions).Error; err != nil || sessions != 0 {
		t.Fatalf("old browser sessions remain: %d %v", sessions, err)
	}
	var upload models.UploadSession
	if err := conn.First(&upload).Error; err != nil || upload.UploadId != "keep-resume-checkpoint" {
		t.Fatalf("missing legacy business table was not preserved: %+v %v", upload, err)
	}
}

func TestRestoreLateInsertFailureRollsBackAllTablesAndSessions(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{&backupTestItem{}, &backupOtherTestItem{}, &models.UserSession{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&backupTestItem{ID: 7, Name: "original"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.UserSession{SessionID: "original-session", TokenID: "original-token"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json":      "{\"ID\":1,\"Name\":\"new\"}\n",
		"backupOtherTestItem.json": "{\"ID\":2,\"Name\":\"new\"}\n{\"ID\":2,\"Name\":\"duplicate\"}\n",
	}, zip.Store)
	if err := Restore(archive); err == nil {
		t.Fatal("duplicate key must fail whole restore")
	}
	var items []backupTestItem
	if err := conn.Find(&items).Error; err != nil || !reflect.DeepEqual(items, []backupTestItem{{ID: 7, Name: "original"}}) {
		t.Fatalf("earlier table did not roll back: %+v %v", items, err)
	}
	var session models.UserSession
	if err := conn.First(&session).Error; err != nil || session.SessionID != "original-session" {
		t.Fatalf("failed transaction changed existing session: %+v %v", session, err)
	}
}

func TestRestoreManifestMissingDataDoesNotFallBackToLegacy(t *testing.T) {
	conn := setupBackupTest(t)
	if err := conn.Create(&backupTestItem{ID: 9, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(conn, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "tables", "backupTestItem.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRestorePlan(dir, conn); err == nil {
		t.Fatal("new backup with missing data must never use legacy missing-table semantics")
	}
	var got backupTestItem
	if err := conn.First(&got).Error; err != nil || got.Name != "before" {
		t.Fatalf("preflight changed the target database: %+v %v", got, err)
	}
}

func TestRestoreLegacyRechecksDependenciesAfterMaintenance(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{models.User{}, models.ApiKey{}, models.Migrator{}}
	if err := conn.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.User{Username: "original", Password: "original-hash"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"Migrator.json": "{\"id\":1,\"version_code\":67}\n",
		"User.json":     "{\"id\":1,\"username\":\"restored\",\"password\":\"restored-hash\"}\n",
	}, zip.Store)
	entered := false
	beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
		entered = true
		// 模拟完整预检之后、进入维护之前创建了旧包未包含的引用表。
		if err := conn.AutoMigrate(&models.ApiKey{}); err != nil {
			return nil, err
		}
		return &restoreMaintenance{database: conn}, nil
	}
	if err := Restore(archive); err == nil || !strings.Contains(err.Error(), "关联表") {
		t.Fatalf("changed legacy dependencies were not rejected: %v", err)
	}
	if !entered {
		t.Fatal("test must reach maintenance after a successful preflight")
	}
	var got models.User
	if err := conn.First(&got).Error; err != nil || got.Username != "original" {
		t.Fatalf("dependency rejection changed target: %v", err)
	}
	if !conn.Migrator().HasTable(&models.ApiKey{}) {
		t.Fatal("dependency rejection removed the newly created table")
	}
}

func TestRestoreLegacyVersionMismatchRejectedBeforeChanges(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{&backupTestItem{}, &models.Migrator{}}
	if err := conn.Create(&backupTestItem{ID: 9, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	version, err := json.Marshal(models.Migrator{VersionCode: 65})
	if err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json": "{\"ID\":1,\"Name\":\"new\"}\n",
		"Migrator.json":       string(version) + "\n",
	}, zip.Deflate)
	if err := Restore(archive); err == nil {
		t.Fatal("legacy schema version mismatch must require explicit migration")
	}
	var got backupTestItem
	if err := conn.First(&got).Error; err != nil || got.Name != "before" {
		t.Fatalf("incompatible archive changed data: %+v %v", got, err)
	}
}

func TestRestoreLegacyAuthenticationLossIsRejected(t *testing.T) {
	for _, scenario := range []string{"enabled TOTP missing secret", "enabled TOTP null secret", "API key missing hash", "API key null hash", "disabled TOTP"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			models.AllTables = []any{&models.User{}}
			files := map[string]string{"User.json": "{\"id\":1,\"username\":\"restored\",\"Password\":\"password-hash\",\"two_factor_enabled\":false}\n"}
			if strings.HasPrefix(scenario, "enabled TOTP") {
				files["User.json"] = strings.ReplaceAll(files["User.json"], "false", "true")
			}
			if scenario == "enabled TOTP null secret" {
				files["User.json"] = strings.ReplaceAll(files["User.json"], "true", "true,\"two_factor_secret\":null")
			}
			if strings.HasPrefix(scenario, "API key") {
				models.AllTables = append(models.AllTables, &models.ApiKey{})
				files["ApiKey.json"] = "{\"id\":1,\"user_id\":1,\"name\":\"old key\",\"key_prefix\":\"qms_old\",\"is_active\":false}\n"
			}
			if scenario == "API key null hash" {
				files["ApiKey.json"] = strings.ReplaceAll(files["ApiKey.json"], "false", "false,\"key_hash\":null")
			}
			if err := conn.AutoMigrate(models.AllTables...); err != nil {
				t.Fatal(err)
			}
			if err := conn.Create(&models.User{Username: "before", Password: "existing-hash"}).Error; err != nil {
				t.Fatal(err)
			}
			maintenanceStarted := false
			beginMaintenance := beginRestoreMaintenance
			beginRestoreMaintenance = func(ctx context.Context) (*restoreMaintenance, error) {
				maintenanceStarted = true
				return beginMaintenance(ctx)
			}
			err := Restore(writeBackupArchive(t, files, zip.Deflate))
			if (err == nil) != (scenario == "disabled TOTP") {
				t.Fatalf("unexpected legacy authentication result: %v", err)
			}
			if maintenanceStarted != (scenario == "disabled TOTP") {
				t.Fatalf("invalid authentication data reached restore maintenance: %v", err)
			}
			var user models.User
			if err := conn.First(&user).Error; err != nil {
				t.Fatal(err)
			}
			want := "before"
			if scenario == "disabled TOTP" {
				want = "restored"
			}
			if user.Username != want || user.SingletonKey != 1 {
				t.Fatalf("legacy user conversion violated atomicity or singleton: %+v", user)
			}
		})
	}
}

func TestRestoreLegacySchema66TextAdapter(t *testing.T) {
	conn := setupBackupTest(t)
	if models.MaxVersionCode != 67 {
		t.Skip("66 → 67 adapter is intentionally limited to the text-only migration")
	}
	models.AllTables = []any{&backupTestItem{}, &models.Migrator{}}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json": "{\"ID\":1,\"Name\":\"preserved\"}\n",
		"Migrator.json":       "{\"id\":1,\"version_code\":66}\n",
	}, zip.Deflate)
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	var version models.Migrator
	if err := conn.First(&version).Error; err != nil || version.VersionCode != 67 {
		t.Fatalf("explicit adapter did not advance restored schema version: %+v %v", version, err)
	}
}

func TestRestoreLegacySchema66DownloadHiddenFields(t *testing.T) {
	testRestoreLegacySchema66DownloadHiddenFields(t, setupBackupTest(t))
}

func testRestoreLegacySchema66DownloadHiddenFields(t *testing.T, conn *gorm.DB) {
	t.Helper()
	models.AllTables = []any{&models.Migrator{}, &models.DbDownloadTask{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	// 固定旧版专用表示的 JSON，不依赖当前模型编码，防止隐藏字段被同时漏写、漏读。
	archive := writeBackupArchive(t, map[string]string{
		"Migrator.json": "{\"id\":1,\"version_code\":66}\n",
		"DbDownloadTask.json": `{"id":42,"status":1,"source":"emby_media",` +
			`"replace_baseline":"3:100:oldhash","published_sha256":"newhash",` +
			`"remote_download_url":"https://example.invalid/file?token=legacy",` +
			`"local_source_path":"/source/旧媒体.nfo","emby_item_id":"legacy-emby-item",` +
			`"dedup_scope_hash":"legacy-scope","dedup_locator_hash":"legacy-locator"}` + "\n",
	}, zip.Deflate)
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	var got models.DbDownloadTask
	if err := conn.First(&got, 42).Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		name string
		got  string
		want string
	}{
		{"replace_baseline", got.ReplaceBaseline, "3:100:oldhash"},
		{"published_sha256", got.PublishedSHA256, "newhash"},
		{"remote_download_url", got.RemoteDownloadUrl, "https://example.invalid/file?token=legacy"},
		{"local_source_path", got.LocalSourcePath, "/source/旧媒体.nfo"},
		{"emby_item_id", got.EmbyItemId, "legacy-emby-item"},
		{"dedup_scope_hash", got.DedupScopeHash, "legacy-scope"},
		{"dedup_locator_hash", got.DedupLocatorHash, "legacy-locator"},
	} {
		t.Run(field.name, func(t *testing.T) {
			if field.got != field.want {
				t.Errorf("旧下载任务隐藏字段未恢复：got %q, want %q", field.got, field.want)
			}
		})
	}
}

func TestRestoreLegacyRegisteredVersionFileIsRequired(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{&backupTestItem{}, &models.Migrator{}}
	archive := writeBackupArchive(t, map[string]string{"backupTestItem.json": "{\"ID\":1,\"Name\":\"new\"}\n"}, zip.Deflate)
	if err := Restore(archive); err == nil {
		t.Fatal("registered schema version cannot be guessed from an archive without Migrator.json")
	}
	var count int64
	if err := conn.Model(&backupTestItem{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unversioned archive mutated target data: %d %v", count, err)
	}
}

func TestRestoreActiveTransferIndexesDoNotCancelDuplicates(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "indexes restored", true: "duplicates roll back"}[duplicate], func(t *testing.T) {
			conn := setupBackupTest(t)
			models.AllTables = []any{&models.DbDownloadTask{}, &models.DbUploadTask{}}
			if err := conn.AutoMigrate(models.AllTables...); err != nil {
				t.Fatal(err)
			}
			original := models.DbDownloadTask{BaseModel: models.BaseModel{ID: 8}, DedupScopeHash: "original-scope", DedupLocatorHash: "original-locator", Status: models.DownloadStatusPending}
			if err := conn.Create(&original).Error; err != nil {
				t.Fatal(err)
			}
			if err := models.EnsureRestoreIndexes(conn, models.AllTables); err != nil {
				t.Fatal(err)
			}
			archive := logicalArchiveForRestore(t, conn)
			if duplicate {
				archive = writeBackupArchive(t, map[string]string{
					"DbDownloadTask.json": "{\"id\":1,\"status\":0,\"dedup_scope_hash\":\"same\",\"dedup_locator_hash\":\"same\"}\n{\"id\":2,\"status\":0,\"dedup_scope_hash\":\"same\",\"dedup_locator_hash\":\"same\"}\n",
					"DbUploadTask.json":   "",
				}, zip.Deflate)
			}
			err := Restore(archive)
			if (err != nil) != duplicate {
				t.Fatalf("unexpected duplicate handling: %v", err)
			}
			for _, index := range []struct {
				model any
				name  string
			}{
				{&models.DbDownloadTask{}, "idx_db_download_tasks_active_target"},
				{&models.DbUploadTask{}, "idx_db_upload_tasks_active_target"},
			} {
				if !conn.Migrator().HasIndex(index.model, index.name) {
					t.Fatalf("required active transfer index is missing: %s", index.name)
				}
			}
			var got []models.DbDownloadTask
			if err := conn.Find(&got).Error; err != nil || len(got) != 1 || got[0].ID != original.ID || got[0].Status != original.Status {
				t.Fatalf("restore changed or cancelled original task: %+v %v", got, err)
			}
		})
	}
}

func TestRestoreSQLiteChecksForeignKeysWhenConnectionDoesNot(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{&models.ApiKey{}, &models.User{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.User{Username: "original", Password: "original-hash"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"User.json":   "",
		"ApiKey.json": "{\"id\":1,\"user_id\":999,\"name\":\"orphan\",\"key_hash\":\"stored-hash\",\"key_prefix\":\"qms_old\",\"is_active\":false}\n",
	}, zip.Deflate)
	if err := Restore(archive); err == nil {
		t.Fatal("restore committed an orphaned foreign key with connection checks disabled")
	}
	var user models.User
	if err := conn.First(&user).Error; err != nil || user.Username != "original" {
		t.Fatalf("foreign key validation failure did not roll back: %+v %v", user, err)
	}
}

func TestRestoreFailureDoesNotLogCredentialValues(t *testing.T) {
	conn := setupBackupTest(t)
	const secret = "private-backup-credential"
	var output bytes.Buffer
	conn.Logger = logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	injected := errors.New("database rejected " + secret)
	if err := conn.Callback().Create().Before("gorm:create").Register("test:restore_sensitive_error", func(tx *gorm.DB) {
		if tx.Statement.Table == "backup_test_items" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json": "{\"ID\":1,\"Name\":\"" + secret + "\"}\n",
	}, zip.Deflate)
	err := Restore(archive)
	if !errors.Is(err, injected) {
		t.Fatalf("restore lost underlying error identity: %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(output.String(), secret) {
		t.Fatal("restore error exposed backed-up credential values")
	}
	if !strings.Contains(err.Error(), "backup_test_items") || !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("restore error lost safe table/row location: %v", err)
	}
}

func TestRestoreLegacyRejectsInvalidUnicodeBeforeMutation(t *testing.T) {
	conn := setupBackupTest(t)
	if err := conn.Create(&backupTestItem{ID: 9, Name: "original"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json": "{\"ID\":1,\"Name\":\"\\ud800\"}\n",
	}, zip.Deflate)
	if err := Restore(archive); err == nil {
		t.Fatal("invalid Unicode must not be silently replaced during legacy import")
	}
	var got backupTestItem
	if err := conn.First(&got).Error; err != nil || got.Name != "original" {
		t.Fatalf("invalid Unicode preflight changed target rows: %+v %v", got, err)
	}
}

func TestRestoreLegacySchema66JSONSerializer(t *testing.T) {
	testRestoreLegacySchema66JSONSerializer(t, setupBackupTest(t))
}

func testRestoreLegacySchema66JSONSerializer(t *testing.T, conn *gorm.DB) {
	t.Helper()
	models.AllTables = []any{&models.Migrator{}, &models.Sync{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	wantResult := &realtime.SyncScanResult{
		SucceededFiles: 3, FailedFiles: 1, SkippedFiles: 2,
		Failures: []realtime.SyncScanFailure{}, CleanupStatus: "skipped", CleanupReason: "扫描结果包含中文和 \"引号\"",
	}
	want := []models.Sync{
		{BaseModel: models.BaseModel{ID: 1}, ScanResult: wantResult, Status: models.SyncStatusPartial},
		{BaseModel: models.BaseModel{ID: 2}, ScanResult: nil, Status: models.SyncStatusCompleted},
		{BaseModel: models.BaseModel{ID: 3}, ScanResult: &realtime.SyncScanResult{}},
		{BaseModel: models.BaseModel{ID: 4}, ScanResult: nil},
	}
	// 旧版本直接编码模型，因此 serializer 字段在文件中是对象或 null。
	var lines strings.Builder
	for _, row := range want[:2] {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(data)
		lines.WriteByte('\n')
	}
	// 手写边界值，避免模型编码把空对象补齐字段或把省略值改成 null。
	lines.WriteString("{\"id\":3,\"scan_result\":{}}\n{\"id\":4}\n")
	archive := writeBackupArchive(t, map[string]string{
		"Migrator.json": "{\"id\":1,\"version_code\":66}\n",
		"Sync.json":     lines.String(),
	}, zip.Deflate)
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	var restored []models.Sync
	if err := conn.Order("id").Find(&restored).Error; err != nil || !reflect.DeepEqual(restored, want) {
		t.Fatalf("旧扫描结果未完整恢复：%+v %v", restored, err)
	}
	for _, row := range want {
		wantJSON, err := json.Marshal(row.ScanResult)
		if err != nil {
			t.Fatal(err)
		}
		var stored sql.NullString
		if err := conn.Raw("SELECT scan_result FROM syncs WHERE id = ?", row.ID).Row().Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if row.ScanResult == nil {
			if stored.Valid {
				t.Fatalf("旧 null 扫描结果未保留 SQL NULL：%q", stored.String)
			}
		} else if !stored.Valid || stored.String != string(wantJSON) {
			t.Fatalf("旧扫描结果必须只编码一层数据库 JSON 文本：%q", stored.String)
		}
	}
}
