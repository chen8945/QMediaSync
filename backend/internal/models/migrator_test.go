package models

import (
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
)

func TestBatchCreateTableCreatesMigratorTable(t *testing.T) {
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := BatchCreateTable(); err != nil {
		t.Fatalf("批量建表失败: %v", err)
	}
	if !db.Db.Migrator().HasTable(Migrator{}) {
		t.Fatal("批量建表应创建 migrator 表")
	}
	if !db.Db.Migrator().HasIndex(&DbUploadTask{}, activeUploadTaskUniqueIndexName) {
		t.Fatal("批量建表应创建活跃上传任务唯一索引")
	}
}

func TestBatchCreateTableCreatesEmbyLibraryRefreshTasksTable(t *testing.T) {
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := BatchCreateTable(); err != nil {
		t.Fatalf("批量建表失败: %v", err)
	}
	if !db.Db.Migrator().HasTable(EmbyLibraryRefreshTask{}) {
		t.Fatal("批量建表应创建 emby_library_refresh_tasks 表")
	}
}

func TestBatchCreateTableCreatesUploadStrmTables(t *testing.T) {
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := BatchCreateTable(); err != nil {
		t.Fatalf("批量建表失败: %v", err)
	}
	for _, table := range []any{
		UploadSession{},
		DirectoryUploadRule{},
		StrmGenerationTask{},
	} {
		if !db.Db.Migrator().HasTable(table) {
			t.Fatalf("批量建表应创建 %s 表", GetTableName(table))
		}
	}
}

func TestBatchCreateTableCreatesDirectoryUploadProcessedFilesTable(t *testing.T) {
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := BatchCreateTable(); err != nil {
		t.Fatalf("批量建表失败: %v", err)
	}
	if !db.Db.Migrator().HasTable(DirectoryUploadProcessedFile{}) {
		t.Fatal("批量建表应创建 directory_upload_processed_files 表")
	}
}

func TestInitDBDoesNotCreateDefaultAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	helpers.AppLogger = &helpers.QLogger{
		Logger: log.New(io.Discard, "", 0),
	}

	InitDB()

	var count int64
	if err := db.Db.Model(&User{}).Count(&count).Error; err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("新库初始化后用户数量 = %d，期望 0", count)
	}
}

func createMigratorTestTable(t *testing.T) {
	t.Helper()
	if err := db.Db.Exec(`
		CREATE TABLE migrator (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			version_code integer
		)
	`).Error; err != nil {
		t.Fatalf("创建迁移表失败: %v", err)
	}
}

func setupMigratorVersion43TestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.AutoMigrate(&DbDownloadTask{}, &DbUploadTask{}, &Settings{}, &SyncPath{}); err != nil {
		t.Fatalf("创建测试表失败: %v", err)
	}
	if err := db.Db.Create(&Migrator{VersionCode: 43}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
}

type legacyUniqueNotificationChannel struct {
	ID          uint   `gorm:"primaryKey"`
	ChannelType string `gorm:"index,uniqueIndex:idx_channel_type"`
	ChannelName string
	IsEnabled   bool `gorm:"default:true"`
}

type legacyUserWithoutSingletonKey struct {
	BaseModel
	Username string `gorm:"unique;not null"`
	Password string `gorm:"not null"`
}

func (legacyUserWithoutSingletonKey) TableName() string {
	return "users"
}

func (legacyUniqueNotificationChannel) TableName() string {
	return "notification_channels"
}

func setupMigratorVersion45NotificationTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.AutoMigrate(&legacyUniqueNotificationChannel{}, &NotificationRule{}, &Settings{}, &SyncPath{}); err != nil {
		t.Fatalf("创建旧通知渠道测试表失败: %v", err)
	}
	if err := db.Db.Create(&Migrator{VersionCode: 45}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Create(&legacyUniqueNotificationChannel{ChannelType: "telegram", ChannelName: "Telegram A", IsEnabled: true}).Error; err != nil {
		t.Fatalf("创建旧通知渠道失败: %v", err)
	}
}

func TestMigrateNotificationChannelAllowsDuplicateTypesAndBackfillsRules(t *testing.T) {
	setupMigratorVersion45NotificationTestDB(t)

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}

	if err := db.Db.Create(&NotificationChannel{ChannelType: "telegram", ChannelName: "Telegram B", IsEnabled: true}).Error; err != nil {
		t.Fatalf("迁移后应允许创建同类型通知渠道: %v", err)
	}

	var channel NotificationChannel
	if err := db.Db.Where("channel_name = ?", "Telegram A").First(&channel).Error; err != nil {
		t.Fatalf("读取已有通知渠道失败: %v", err)
	}
	var total int64
	if err := db.Db.Model(&NotificationRule{}).Where("channel_id = ?", channel.ID).Count(&total).Error; err != nil {
		t.Fatalf("统计通知规则失败: %v", err)
	}
	if total != int64(len(notification.AllNotificationTypes)) {
		t.Fatalf("补齐规则数量 = %d，期望 %d", total, len(notification.AllNotificationTypes))
	}
}

func TestMigrateVersion50AddsEmbyDailyFirstFullSyncFields(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	GlobalEmbyConfig = nil

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 50}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE emby_config (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			emby_url text,
			emby_api_key text,
			enable_delete_netdisk integer,
			enable_refresh_library integer,
			enable_media_notification integer,
			enable_extract_media_info integer,
			enable_auth integer,
			sync_enabled integer,
			sync_cron text,
			last_sync_time integer,
			last_full_sync_at integer,
			last_incremental_sync_at integer,
			last_saved_cursor_at integer,
			last_processed_count integer,
			last_error text,
			is_running numeric,
			sync_mode text,
			started_at integer,
			selected_libraries text,
			sync_all_libraries integer,
			enable_playback_overview integer,
			enable_playback_progress integer
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 50 emby_config 表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO emby_config (
			created_at, updated_at, emby_url, emby_api_key,
			enable_delete_netdisk, enable_refresh_library, enable_media_notification, enable_extract_media_info, enable_auth,
			sync_enabled, sync_cron,
			last_sync_time, last_full_sync_at, last_incremental_sync_at, last_saved_cursor_at,
			last_processed_count, last_error, is_running, sync_mode, started_at,
			selected_libraries, sync_all_libraries, enable_playback_overview, enable_playback_progress
		)
		VALUES (
			1, 1, 'http://emby.local', 'key',
			0, 1, 0, 1, 1,
			1, '0 * * * *',
			200, 200, 100, 90,
			12, '', 0, 'idle', 0,
			'[]', 1, 0, 0
		)
	`).Error; err != nil {
		t.Fatalf("插入版本 50 EmbyConfig 失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	for _, column := range []string{
		"enable_daily_first_full_sync",
		"last_success_sync_mode",
	} {
		if !db.Db.Migrator().HasColumn(&EmbyConfig{}, column) {
			t.Fatalf("迁移应添加 emby_config.%s 字段", column)
		}
	}

	var config EmbyConfig
	if err := db.Db.First(&config).Error; err != nil {
		t.Fatalf("读取 EmbyConfig 失败: %v", err)
	}
	if config.EnableDailyFirstFullSync != 1 {
		t.Fatalf("EnableDailyFirstFullSync = %d, want 1", config.EnableDailyFirstFullSync)
	}
	if config.LastSuccessSyncMode != EmbySyncModeFull {
		t.Fatalf("LastSuccessSyncMode = %q, want %q", config.LastSuccessSyncMode, EmbySyncModeFull)
	}
}

func TestMigrateVersion46AddsUserSingletonKey(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.AutoMigrate(&legacyUserWithoutSingletonKey{}, &Settings{}, &SyncPath{}); err != nil {
		t.Fatalf("创建旧用户测试表失败: %v", err)
	}
	if err := db.Db.Create(&Migrator{VersionCode: 46}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Create(&legacyUserWithoutSingletonKey{Username: "admin", Password: "hashed"}).Error; err != nil {
		t.Fatalf("创建旧用户失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	if !db.Db.Migrator().HasColumn(&User{}, "singleton_key") {
		t.Fatal("迁移应添加 users.singleton_key 字段")
	}
	if err := db.Db.Create(&User{Username: "other", Password: "hashed"}).Error; err == nil {
		t.Fatal("迁移后创建第二个用户 error = nil，期望被唯一约束拒绝")
	}
}

func TestMigrateTaskSourceEnumValues(t *testing.T) {
	setupMigratorVersion43TestDB(t)

	downloadTasks := []DbDownloadTask{
		{RemoteFileId: "download-strm", Source: DownloadSource("strm同步"), SourceType: SourceType115},
		{RemoteFileId: "download-local", Source: DownloadSource("本地文件"), SourceType: SourceTypeLocal},
		{RemoteFileId: "download-emby", Source: DownloadSource("emby媒体信息提取"), SourceType: SourceType("emby媒体信息提取")},
		{RemoteFileId: "download-already-new", Source: DownloadSource("strm_sync"), SourceType: SourceType115},
		{RemoteFileId: "download-unknown", Source: DownloadSource("custom_source"), SourceType: SourceType("custom_type")},
	}
	if err := db.Db.Create(&downloadTasks).Error; err != nil {
		t.Fatalf("创建下载任务测试数据失败: %v", err)
	}

	uploadTasks := []DbUploadTask{
		{RemoteFileId: "upload-strm", LocalFullPath: "/tmp/strm.nfo", Source: UploadSource("strm同步"), SourceType: SourceType115},
		{RemoteFileId: "upload-scrape", LocalFullPath: "/tmp/scrape.nfo", Source: UploadSource("刮削整理"), SourceType: SourceType115},
		{RemoteFileId: "upload-already-new", LocalFullPath: "/tmp/already-new.nfo", Source: UploadSource("strm_sync"), SourceType: SourceType115},
		{RemoteFileId: "upload-unknown", LocalFullPath: "/tmp/unknown.nfo", Source: UploadSource("custom_source"), SourceType: SourceType("custom_type")},
	}
	if err := db.Db.Create(&uploadTasks).Error; err != nil {
		t.Fatalf("创建上传任务测试数据失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	if !db.Db.Migrator().HasTable(UserSession{}) {
		t.Fatal("迁移应创建 user_sessions 表")
	}

	assertDownloadTaskSource(t, "download-strm", "strm_sync", "115")
	assertDownloadTaskSource(t, "download-local", "local_file", "local")
	assertDownloadTaskSource(t, "download-emby", "emby_media", "emby_media")
	assertDownloadTaskSource(t, "download-already-new", "strm_sync", "115")
	assertDownloadTaskSource(t, "download-unknown", "custom_source", "custom_type")
	assertUploadTaskSource(t, "/tmp/strm.nfo", "strm_sync")
	assertUploadTaskSource(t, "/tmp/scrape.nfo", "scrape_organize")
	assertUploadTaskSource(t, "/tmp/already-new.nfo", "strm_sync")
	assertUploadTaskSource(t, "/tmp/unknown.nfo", "custom_source")
}

func TestMigrateVersion49AddsEmbySyncStateAndBatchFields(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	GlobalEmbyConfig = nil

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 49}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE emby_config (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			emby_url text,
			emby_api_key text,
			sync_enabled integer,
			sync_cron text,
			last_sync_time integer,
			selected_libraries text,
			sync_all_libraries integer
		)
	`).Error; err != nil {
		t.Fatalf("创建旧 emby_config 表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO emby_config (created_at, updated_at, emby_url, emby_api_key, sync_enabled, sync_cron, last_sync_time, selected_libraries, sync_all_libraries)
		VALUES (1, 1, 'http://emby.local', 'key', 1, '0 * * * *', 123, '[]', 1)
	`).Error; err != nil {
		t.Fatalf("插入旧 EmbyConfig 失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE emby_media_items (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			item_id text,
			item_id_int integer,
			name text,
			type text,
			parent_id text,
			library_id text
		)
	`).Error; err != nil {
		t.Fatalf("创建旧 emby_media_items 表失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	for _, column := range []string{
		"last_full_sync_at",
		"last_incremental_sync_at",
		"last_saved_cursor_at",
		"last_processed_count",
		"last_error",
		"is_running",
		"sync_mode",
		"started_at",
	} {
		if !db.Db.Migrator().HasColumn(&EmbyConfig{}, column) {
			t.Fatalf("迁移应添加 emby_config.%s 字段", column)
		}
	}
	for _, column := range []string{"last_seen_sync_run", "last_seen_at"} {
		if !db.Db.Migrator().HasColumn(&EmbyMediaItem{}, column) {
			t.Fatalf("迁移应添加 emby_media_items.%s 字段", column)
		}
	}
}

func TestMigrateVersion51AddsUploadStrmModels(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.AutoMigrate(&DbUploadTask{}, &Settings{}); err != nil {
		t.Fatalf("创建版本 51 测试表失败: %v", err)
	}
	if err := db.Db.Create(&Migrator{VersionCode: 51}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	for _, table := range []any{
		UploadSession{},
		DirectoryUploadRule{},
		StrmGenerationTask{},
	} {
		if !db.Db.Migrator().HasTable(table) {
			t.Fatalf("迁移应创建 %s 表", GetTableName(table))
		}
	}
	for _, column := range []string{
		"sync_path_id",
		"relative_path",
		"uploaded_bytes",
		"upload_result",
		"resume_state",
		"source_cleanup_status",
		"source_cleanup_error",
		"source_deleted_at",
	} {
		if !db.Db.Migrator().HasColumn(&DbUploadTask{}, column) {
			t.Fatalf("迁移应添加 db_upload_tasks.%s 字段", column)
		}
	}
	for _, column := range []string{
		"upload_rapid_wait_enabled",
		"upload_rapid_wait_timeout_seconds",
		"upload_rapid_wait_interval_seconds",
		"upload_rapid_wait_min_size",
		"upload_rapid_wait_force_size",
		"upload_rapid_wait_skip_upload",
	} {
		if !db.Db.Migrator().HasColumn(&Settings{}, column) {
			t.Fatalf("迁移应添加 settings.%s 字段", column)
		}
	}
}

func TestMigrateVersion52AddsEmbyTargetedRefreshFields(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 52}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE emby_library_refresh_tasks (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			library_id text,
			library_name text,
			sync_path_ids_str text,
			status text,
			last_event_at integer,
			refresh_after_at integer,
			deadline_at integer,
			last_checked_at integer,
			last_refresh_at integer,
			error text
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 52 Emby 刷新任务表失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	for _, column := range []string{
		"target_type",
		"item_ids_str",
		"item_recursive",
		"fallback_library_id",
		"fallback_library_name",
	} {
		if !db.Db.Migrator().HasColumn(&EmbyLibraryRefreshTask{}, column) {
			t.Fatalf("迁移应添加 emby_library_refresh_tasks.%s 字段", column)
		}
	}
}

func TestMigrateVersion59SeparatesEmbyRefreshTaskKeyFromLibraryID(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 59}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE emby_library_refresh_tasks (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			library_id varchar(128),
			library_name varchar(255),
			sync_path_ids_str text,
			target_type varchar(32),
			item_ids_str text,
			item_recursive boolean,
			fallback_library_id varchar(128),
			fallback_library_name varchar(255),
			status varchar(32),
			last_event_at integer,
			refresh_after_at integer,
			deadline_at integer,
			last_checked_at integer,
			last_refresh_at integer,
			error text
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 59 Emby 刷新任务表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO emby_library_refresh_tasks (
			library_id, library_name, sync_path_ids_str, target_type, item_ids_str,
			fallback_library_id, fallback_library_name, status
		) VALUES ('item:301', '第一季', '[10]', 'item', '["301"]', 'lib-tv', '剧集', 'pending')
	`).Error; err != nil {
		t.Fatalf("创建旧 item 刷新任务失败: %v", err)
	}
	if err := db.Db.Exec("CREATE UNIQUE INDEX idx_emby_library_refresh_tasks_library_id ON emby_library_refresh_tasks(library_id)").Error; err != nil {
		t.Fatalf("创建旧 library_id 唯一索引失败: %v", err)
	}

	Migrate()
	var migrated Migrator
	if err := db.Db.First(&migrated).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrated.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrated.VersionCode, MaxVersionCode)
	}

	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("301")).First(&task).Error; err != nil {
		t.Fatalf("查询迁移后的 item 刷新任务失败: %v", err)
	}
	if task.LibraryId != "lib-tv" {
		t.Fatalf("迁移后 library_id = %s，期望真实媒体库 lib-tv", task.LibraryId)
	}
}

func TestMigrateVersion56AddsDirectoryUploadProcessedFiles(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 56}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE db_upload_tasks (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			source text,
			account_id integer,
			sync_path_id integer,
			source_type text,
			local_full_path text,
			remote_file_id text,
			remote_path_id text,
			file_name text,
			status integer,
			file_size integer
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 56 上传任务表失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	if !db.Db.Migrator().HasTable(DirectoryUploadProcessedFile{}) {
		t.Fatal("迁移应创建 directory_upload_processed_files 表")
	}
	for _, column := range []string{
		"source_fingerprint",
		"local_mtime_ns",
	} {
		if !db.Db.Migrator().HasColumn(&DbUploadTask{}, column) {
			t.Fatalf("迁移应添加 db_upload_tasks.%s 字段", column)
		}
	}
}

func TestMigrateVersion57AddsDirectoryUploadEnabled(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 57}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE sync_paths (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			base_cid text,
			local_path text,
			remote_path text,
			source_type text,
			account_id integer,
			enable_cron numeric
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 57 同步目录表失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE directory_upload_rules (
			id integer primary key autoincrement,
			sync_path_id integer,
			enabled numeric
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 57 目录监控规则表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO sync_paths (id, created_at, updated_at, base_cid, local_path, remote_path, source_type, account_id, enable_cron)
		VALUES
			(1, 0, 0, 'enabled-root', '/strm/enabled', '/remote/enabled', '115', 1, 0),
			(2, 0, 0, 'disabled-root', '/strm/disabled', '/remote/disabled', '115', 1, 0)
	`).Error; err != nil {
		t.Fatalf("写入版本 57 同步目录失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO directory_upload_rules (sync_path_id, enabled)
		VALUES (1, true), (2, false)
	`).Error; err != nil {
		t.Fatalf("写入版本 57 目录监控规则失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	if !db.Db.Migrator().HasColumn(&SyncPath{}, "directory_upload_enabled") {
		t.Fatal("迁移应添加 sync_paths.directory_upload_enabled 字段")
	}
	var enabledSyncPath SyncPath
	if err := db.Db.First(&enabledSyncPath, 1).Error; err != nil {
		t.Fatalf("读取启用规则同步目录失败: %v", err)
	}
	if !enabledSyncPath.DirectoryUploadEnabled {
		t.Fatal("存在启用目录监控规则的同步目录应回填总开关为 true")
	}
	var disabledSyncPath SyncPath
	if err := db.Db.First(&disabledSyncPath, 2).Error; err != nil {
		t.Fatalf("读取停用规则同步目录失败: %v", err)
	}
	if disabledSyncPath.DirectoryUploadEnabled {
		t.Fatal("只有停用目录监控规则的同步目录总开关应保持 false")
	}
}

func TestMigrateVersion58AddsURLValidityCheckSettings(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 58}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE settings (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			download_threads integer,
			file_detail_threads integer,
			openlist_qps integer,
			openlist_retry integer,
			openlist_retry_delay integer,
			file_list_page_size integer
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 58 设置表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO settings (
			id,
			created_at,
			updated_at,
			download_threads,
			file_detail_threads,
			openlist_qps,
			openlist_retry,
			openlist_retry_delay,
			file_list_page_size
		) VALUES (1, 0, 0, 1, 3, 3, 1, 60, 1150)
	`).Error; err != nil {
		t.Fatalf("写入版本 58 设置失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	for _, column := range []string{
		"url_validity_check_enabled",
		"url_validity_check_timeout_seconds",
	} {
		if !db.Db.Migrator().HasColumn(&Settings{}, column) {
			t.Fatalf("迁移应添加 settings.%s 字段", column)
		}
	}
	var settings Settings
	if err := db.Db.First(&settings, 1).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if settings.URLValidityCheckEnabled != 1 {
		t.Fatalf("URLValidityCheckEnabled = %d，期望 1", settings.URLValidityCheckEnabled)
	}
	if settings.URLValidityCheckTimeoutSeconds != 3 {
		t.Fatalf("URLValidityCheckTimeoutSeconds = %d，期望 3", settings.URLValidityCheckTimeoutSeconds)
	}
}

func TestMigrateVersion59AddsSyncPathIdempotencyTableAndEmbyTaskKey(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{
			Logger: log.New(io.Discard, "", 0),
		}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 59}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE directory_upload_processed_files (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			source_key text,
			upload_task_id integer,
			result text
		)
	`).Error; err != nil {
		t.Fatalf("创建版本 59 目录监控处理记录表失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	if !db.Db.Migrator().HasTable(&SyncPathIdempotencyRecord{}) {
		t.Fatal("迁移应创建 sync_path_idempotency_records 表")
	}
	idempotencyColumns := syncPathIdempotencyColumnNames(t)
	if _, ok := idempotencyColumns["key_hash"]; !ok {
		t.Fatalf("同步目录幂等表字段 = %v，期望持久化 key_hash 字段", idempotencyColumns)
	}
	if _, ok := idempotencyColumns["key"]; ok {
		t.Fatalf("同步目录幂等表字段 = %v，不应持久化明文 key 字段", idempotencyColumns)
	}
	if _, ok := idempotencyColumns["request_hash"]; ok {
		t.Fatalf("同步目录幂等表字段 = %v，不应持久化 request_hash 字段", idempotencyColumns)
	}
}

func TestMigrateVersion60SeparatesTransferRemoteIdentityFields(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 60}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE sync_files (
			id integer primary key,
			source_type text,
			file_id text,
			pick_code text,
			sha1 text,
			path text,
			file_name text
		)`,
		`CREATE TABLE db_download_tasks (
			id integer primary key,
			source text,
			source_type text,
			sync_file_id integer,
			remote_file_id text,
			remote_path text,
			file_name text
		)`,
		`CREATE TABLE db_upload_tasks (
			id integer primary key,
			source text,
			source_type text,
			sync_file_id integer,
			remote_file_id text,
			remote_path_id text,
			file_name text,
			completed_remote_file_id text,
			completed_pick_code text
		)`,
	} {
		if err := db.Db.Exec(statement).Error; err != nil {
			t.Fatalf("创建版本 60 测试表失败: %v", err)
		}
	}
	if err := db.Db.Exec(`
		INSERT INTO sync_files (id, source_type, file_id, pick_code, sha1, path, file_name) VALUES
			(1, '115', '115-file-id', '115-pick-code', '115-sha1', '/remote/115', 'movie.mkv'),
			(2, 'baidupan', '/remote/baidu/baidu.mkv', 'baidu-fs-id', 'baidu-md5', '/remote/baidu', 'baidu.mkv')
	`).Error; err != nil {
		t.Fatalf("写入旧同步文件失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO db_download_tasks (id, source, source_type, sync_file_id, remote_file_id, remote_path, file_name) VALUES
			(1, 'strm_sync', '115', 1, 'legacy-115-pick-code', '/legacy/115', 'old.mkv'),
			(2, 'strm_sync', 'openlist', 0, 'https://openlist.example/d/open.mkv?sign=secret', '/remote/open', 'open.mkv'),
			(3, 'emby_media', 'emby_media', 0, 'https://emby.example/extract', 'emby-item-id', 'emby.mkv'),
			(4, 'local_file', 'local', 0, '/source/local.mkv', '/not-a-remote-path', 'local.mkv'),
			(5, 'strm_sync', 'baidupan', 2, '/legacy/baidu/baidu.mkv', '/remote/baidu', 'baidu.mkv'),
			(6, 'strm_sync', '115', 0, 'legacy-pick-without-sync-file', '/legacy/unknown', 'unknown.mkv')
	`).Error; err != nil {
		t.Fatalf("写入旧下载任务失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO db_upload_tasks (id, source, source_type, sync_file_id, remote_file_id, remote_path_id, file_name, completed_remote_file_id, completed_pick_code) VALUES
			(1, 'strm_sync', '115', 1, '/remote/upload/movie.mkv', '115-parent', 'movie.mkv', 'completed-115-id', 'completed-115-pick'),
			(2, 'strm_sync', '115', 1, 'replaced-115-id', '115-parent', 'movie.mkv', 'new-115-id', 'new-115-pick'),
			(3, 'directory_monitor', '115', 0, 'directory-old-id', '115-parent', 'directory.mkv', 'directory-new-id', 'directory-new-pick'),
			(4, 'scrape_organize', 'baidupan', 2, '/remote/upload/baidu.mkv', '/remote/upload', 'baidu.mkv', '', 'legacy-baidu-pick-code'),
			(5, 'strm_sync', '115', 1, 'pending-replaced-115-id', '115-parent', 'movie.mkv', '', '')
	`).Error; err != nil {
		t.Fatalf("写入旧上传任务失败: %v", err)
	}

	Migrate()

	var migrator Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if migrator.VersionCode != MaxVersionCode {
		t.Fatalf("迁移版本 = %d，期望 %d", migrator.VersionCode, MaxVersionCode)
	}
	if !db.Db.Migrator().HasIndex(&DbUploadTask{}, activeUploadTaskUniqueIndexName) {
		t.Fatal("60 到 61 迁移应创建活跃上传任务唯一索引")
	}
	if !db.Db.Migrator().HasIndex(&DbDownloadTask{}, activeDownloadTaskUniqueIndexName) {
		t.Fatal("60 到 61 迁移应创建活跃下载任务唯一索引")
	}
	for _, column := range []string{"completed_remote_file_id", "completed_pick_code"} {
		if db.Db.Migrator().HasColumn("db_upload_tasks", column) {
			var tableSQL string
			if err := db.Db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", "db_upload_tasks").Scan(&tableSQL).Error; err != nil {
				t.Fatalf("读取 db_upload_tasks DDL 失败: %v", err)
			}
			t.Fatalf("迁移后不应保留 db_upload_tasks.%s，DDL=%s", column, tableSQL)
		}
	}

	var download115 DbDownloadTask
	if err := db.Db.First(&download115, 1).Error; err != nil {
		t.Fatalf("读取 115 下载任务失败: %v", err)
	}
	if download115.RemoteFileId != "115-file-id" || download115.RemotePickCode != "115-pick-code" || download115.RemoteSha1 != "115-sha1" || download115.RemoteFullPath != "/remote/115/movie.mkv" {
		t.Fatalf("115 下载迁移结果 = %+v", download115)
	}
	if download115.DedupScopeHash == "" || download115.DedupLocatorHash == "" {
		t.Fatalf("115 下载任务应回填去重键: %+v", download115)
	}
	var download115WithoutSyncFile DbDownloadTask
	if err := db.Db.First(&download115WithoutSyncFile, 6).Error; err != nil {
		t.Fatalf("读取无关联同步文件的 115 下载任务失败: %v", err)
	}
	if download115WithoutSyncFile.RemoteFileId != "" || download115WithoutSyncFile.RemotePickCode != "legacy-pick-without-sync-file" {
		t.Fatalf("无关联同步文件的 115 下载迁移结果 = %+v", download115WithoutSyncFile)
	}

	var downloadOpenList DbDownloadTask
	if err := db.Db.First(&downloadOpenList, 2).Error; err != nil {
		t.Fatalf("读取 OpenList 下载任务失败: %v", err)
	}
	if downloadOpenList.RemoteFileId != "" || downloadOpenList.RemoteDownloadUrl == "" || downloadOpenList.RemoteFullPath != "/remote/open/open.mkv" {
		t.Fatalf("OpenList 下载迁移结果 = %+v", downloadOpenList)
	}

	var downloadEmby DbDownloadTask
	if err := db.Db.First(&downloadEmby, 3).Error; err != nil {
		t.Fatalf("读取 Emby 下载任务失败: %v", err)
	}
	if downloadEmby.RemoteFileId != "" || downloadEmby.RemotePath != "" || downloadEmby.RemoteFullPath != "" || downloadEmby.RemoteDownloadUrl == "" || downloadEmby.EmbyItemId != "emby-item-id" {
		t.Fatalf("Emby 下载迁移结果 = %+v", downloadEmby)
	}

	var downloadLocal DbDownloadTask
	if err := db.Db.First(&downloadLocal, 4).Error; err != nil {
		t.Fatalf("读取本地下载任务失败: %v", err)
	}
	if downloadLocal.RemoteFileId != "" || downloadLocal.RemotePath != "" || downloadLocal.RemoteFullPath != "" || downloadLocal.LocalSourcePath != "/source/local.mkv" {
		t.Fatalf("本地下载迁移结果 = %+v", downloadLocal)
	}

	var downloadBaidu DbDownloadTask
	if err := db.Db.First(&downloadBaidu, 5).Error; err != nil {
		t.Fatalf("读取百度下载任务失败: %v", err)
	}
	if downloadBaidu.RemoteFileId != "baidu-fs-id" || downloadBaidu.RemoteSha1 != "" || downloadBaidu.RemoteMd5 != "baidu-md5" {
		t.Fatalf("百度下载迁移结果 = %+v", downloadBaidu)
	}

	var uploadCompleted DbUploadTask
	if err := db.Db.First(&uploadCompleted, 1).Error; err != nil {
		t.Fatalf("读取完成上传任务失败: %v", err)
	}
	if uploadCompleted.RemoteFullPath != "/remote/upload/movie.mkv" || uploadCompleted.RemoteFileId != "completed-115-id" || uploadCompleted.RemotePickCode != "completed-115-pick" || uploadCompleted.ReplacedRemoteFileId != "" {
		t.Fatalf("完成上传迁移结果 = %+v", uploadCompleted)
	}

	var uploadReplaced DbUploadTask
	if err := db.Db.First(&uploadReplaced, 2).Error; err != nil {
		t.Fatalf("读取覆盖上传任务失败: %v", err)
	}
	if uploadReplaced.RemoteFullPath != "/remote/115/movie.mkv" || uploadReplaced.RemoteFileId != "new-115-id" || uploadReplaced.RemotePickCode != "new-115-pick" || uploadReplaced.ReplacedRemoteFileId != "replaced-115-id" {
		t.Fatalf("覆盖上传迁移结果 = %+v", uploadReplaced)
	}

	var uploadPendingReplacement DbUploadTask
	if err := db.Db.First(&uploadPendingReplacement, 5).Error; err != nil {
		t.Fatalf("读取待上传覆盖任务失败: %v", err)
	}
	if uploadPendingReplacement.RemoteFullPath != "/remote/115/movie.mkv" || uploadPendingReplacement.RemoteFileId != "" || uploadPendingReplacement.ReplacedRemoteFileId != "pending-replaced-115-id" {
		t.Fatalf("待上传覆盖任务迁移结果 = %+v", uploadPendingReplacement)
	}
	if uploadReplaced.Status != UploadStatusPending || uploadPendingReplacement.Status != UploadStatusCancelled {
		t.Fatalf("同一范围的旧活跃重复任务应保留最早任务并取消其余任务: 保留=%+v，取消=%+v", uploadReplaced, uploadPendingReplacement)
	}

	var uploadDirectory DbUploadTask
	if err := db.Db.First(&uploadDirectory, 3).Error; err != nil {
		t.Fatalf("读取目录上传任务失败: %v", err)
	}
	if uploadDirectory.RemoteFileId != "directory-new-id" || uploadDirectory.ReplacedRemoteFileId != "" {
		t.Fatalf("目录上传旧文件 ID 不应被误迁移为覆盖记录: %+v", uploadDirectory)
	}

	var uploadBaidu DbUploadTask
	if err := db.Db.First(&uploadBaidu, 4).Error; err != nil {
		t.Fatalf("读取百度上传任务失败: %v", err)
	}
	if uploadBaidu.RemoteFullPath != "/remote/upload/baidu.mkv" || uploadBaidu.RemoteFileId != "" || uploadBaidu.RemotePickCode != "" || uploadBaidu.RemoteMd5 != "baidu-md5" || uploadBaidu.RemotePathId != "" {
		t.Fatalf("百度上传迁移结果 = %+v", uploadBaidu)
	}

	serialized, err := json.Marshal(downloadEmby)
	if err != nil {
		t.Fatalf("序列化隐藏下载字段失败: %v", err)
	}
	for _, hiddenField := range []string{"remote_download_url", "emby_item_id", "local_source_path"} {
		if strings.Contains(string(serialized), hiddenField) {
			t.Fatalf("下载任务 JSON 不应包含 %s: %s", hiddenField, serialized)
		}
	}
}

func TestMigrateVersion61BackfillsActiveUploadTaskUniqueIndex(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.AutoMigrate(&DbDownloadTask{}, &DbUploadTask{}); err != nil {
		t.Fatalf("创建传输任务表失败: %v", err)
	}
	if err := db.Db.Create(&Migrator{VersionCode: MaxVersionCode}).Error; err != nil {
		t.Fatalf("创建当前版本迁移记录失败: %v", err)
	}
	if db.Db.Migrator().HasIndex(&DbUploadTask{}, activeUploadTaskUniqueIndexName) {
		t.Fatal("测试前不应已有活跃上传任务唯一索引")
	}
	if db.Db.Migrator().HasIndex(&DbDownloadTask{}, activeDownloadTaskUniqueIndexName) {
		t.Fatal("测试前不应已有活跃下载任务唯一索引")
	}

	Migrate()

	if !db.Db.Migrator().HasIndex(&DbUploadTask{}, activeUploadTaskUniqueIndexName) {
		t.Fatal("当前版本数据库应补齐活跃上传任务唯一索引")
	}
	if !db.Db.Migrator().HasIndex(&DbDownloadTask{}, activeDownloadTaskUniqueIndexName) {
		t.Fatal("当前版本数据库应补齐活跃下载任务唯一索引")
	}
}

func TestMigrateVersion61BackfillsActiveDownloadTaskDeduplicationFields(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.Exec(`CREATE TABLE db_download_tasks (
		id integer primary key,
		source text,
		source_type text,
		account_id integer,
		sync_path_id integer,
		remote_file_id text,
		remote_download_url text,
		local_source_path text,
		local_full_path text,
		status integer
	)`).Error; err != nil {
		t.Fatalf("创建缺少下载去重字段的版本 61 表失败: %v", err)
	}
	if err := db.Db.AutoMigrate(&DbUploadTask{}); err != nil {
		t.Fatalf("创建上传任务表失败: %v", err)
	}
	if err := db.Db.Exec(`INSERT INTO db_download_tasks (id, source, source_type, account_id, sync_path_id, remote_file_id, status)
		VALUES (1, 'strm_sync', '115', 1, 10, '115-file-id', 0)`).Error; err != nil {
		t.Fatalf("写入旧下载任务失败: %v", err)
	}
	if err := db.Db.Create(&Migrator{VersionCode: MaxVersionCode}).Error; err != nil {
		t.Fatalf("创建当前版本迁移记录失败: %v", err)
	}

	Migrate()

	if !db.Db.Migrator().HasIndex(&DbDownloadTask{}, activeDownloadTaskUniqueIndexName) {
		t.Fatal("当前版本数据库应补齐活跃下载任务唯一索引")
	}
	var task DbDownloadTask
	if err := db.Db.First(&task, 1).Error; err != nil {
		t.Fatalf("读取补迁移后的下载任务失败: %v", err)
	}
	if task.RemoteFileId != "115-file-id" || task.DedupScopeHash == "" || task.DedupLocatorHash == "" {
		t.Fatalf("当前版本补迁移应保留远端定位并回填去重键: %+v", task)
	}
}

func TestEnsureActiveUploadTaskUniqueIndexKeepsMostAdvancedDuplicate(t *testing.T) {
	setupQueueStatusTestDB(t)
	pending := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusPending,
	}
	uploading := &DbUploadTask{
		Source:         UploadSourceStrm,
		SourceType:     SourceType115,
		AccountId:      1,
		RemoteFullPath: "/remote/target/movie.mkv",
		Status:         UploadStatusUploading,
	}
	if err := db.Db.Create([]*DbUploadTask{pending, uploading}).Error; err != nil {
		t.Fatalf("创建旧活跃重复上传任务失败: %v", err)
	}

	if err := ensureActiveUploadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("迁移活跃上传任务唯一索引失败: %v", err)
	}

	var gotPending, gotUploading DbUploadTask
	if err := db.Db.First(&gotPending, pending.ID).Error; err != nil {
		t.Fatalf("读取待上传任务失败: %v", err)
	}
	if err := db.Db.First(&gotUploading, uploading.ID).Error; err != nil {
		t.Fatalf("读取上传中任务失败: %v", err)
	}
	if gotPending.Status != UploadStatusCancelled || gotUploading.Status != UploadStatusUploading {
		t.Fatalf("迁移应保留进度最高的任务: pending=%+v, uploading=%+v", gotPending, gotUploading)
	}
}

func TestEnsureActiveDownloadTaskUniqueIndexKeepsDownloadingDuplicate(t *testing.T) {
	setupQueueStatusTestDB(t)
	pending := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusPending,
	}
	downloading := &DbDownloadTask{
		Source:       DownloadSourceStrm,
		SourceType:   SourceType115,
		AccountId:    1,
		SyncPathId:   10,
		RemoteFileId: "115-file-id",
		Status:       DownloadStatusDownloading,
	}
	if err := db.Db.Create([]*DbDownloadTask{pending, downloading}).Error; err != nil {
		t.Fatalf("创建旧活跃重复下载任务失败: %v", err)
	}

	if err := ensureActiveDownloadTaskUniqueIndex(db.Db); err != nil {
		t.Fatalf("迁移活跃下载任务唯一索引失败: %v", err)
	}

	var gotPending, gotDownloading DbDownloadTask
	if err := db.Db.First(&gotPending, pending.ID).Error; err != nil {
		t.Fatalf("读取待下载任务失败: %v", err)
	}
	if err := db.Db.First(&gotDownloading, downloading.ID).Error; err != nil {
		t.Fatalf("读取下载中任务失败: %v", err)
	}
	if gotPending.Status != DownloadStatusCancelled || gotDownloading.Status != DownloadStatusDownloading {
		t.Fatalf("迁移应保留进度最高的任务: pending=%+v, downloading=%+v", gotPending, gotDownloading)
	}
	if gotPending.DedupScopeHash == "" || gotPending.DedupLocatorHash == "" || gotDownloading.DedupScopeHash == "" || gotDownloading.DedupLocatorHash == "" {
		t.Fatalf("迁移应回填下载去重键: pending=%+v, downloading=%+v", gotPending, gotDownloading)
	}
}

func TestMigrateVersion60KeepsLegacy115PickCodeWhenSyncFilePickCodeIsEmpty(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 60}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE sync_files (
			id integer primary key,
			source_type text,
			file_id text,
			pick_code text,
			sha1 text,
			path text,
			file_name text
		)`,
		`CREATE TABLE db_download_tasks (
			id integer primary key,
			source text,
			source_type text,
			sync_file_id integer,
			remote_file_id text,
			remote_path text,
			file_name text
		)`,
	} {
		if err := db.Db.Exec(statement).Error; err != nil {
			t.Fatalf("创建版本 60 测试表失败: %v", err)
		}
	}
	if err := db.Db.Exec(`
		INSERT INTO sync_files (id, source_type, file_id, pick_code, sha1, path, file_name)
		VALUES (1, '115', '115-file-id', '', '115-sha1', '/remote/115', 'movie.mkv')
	`).Error; err != nil {
		t.Fatalf("写入 PickCode 为空的同步文件失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO db_download_tasks (id, source, source_type, sync_file_id, remote_file_id, remote_path, file_name)
		VALUES (1, 'strm_sync', '115', 1, 'legacy-115-pick-code', '/legacy/115', 'old.mkv')
	`).Error; err != nil {
		t.Fatalf("写入旧 115 下载任务失败: %v", err)
	}

	Migrate()

	var task DbDownloadTask
	if err := db.Db.First(&task, 1).Error; err != nil {
		t.Fatalf("读取迁移后的 115 下载任务失败: %v", err)
	}
	if task.RemoteFileId != "115-file-id" || task.RemotePickCode != "legacy-115-pick-code" || task.RemoteSha1 != "115-sha1" || task.RemoteFullPath != "/remote/115/movie.mkv" {
		t.Fatalf("关联记录 PickCode 为空时应保留旧任务 PickCode，实际 %+v", task)
	}
}

func TestMigrateVersion60RetriesAfterCompletedRemoteFileIDColumnWasDropped(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	createMigratorTestTable(t)
	if err := db.Db.Create(&Migrator{VersionCode: 60}).Error; err != nil {
		t.Fatalf("创建迁移版本记录失败: %v", err)
	}
	if err := db.Db.Exec(`
		CREATE TABLE db_upload_tasks (
			id integer primary key,
			source text,
			source_type text,
			remote_file_id text,
			remote_full_path text,
			remote_pick_code text,
			remote_path_id text,
			file_name text,
			completed_pick_code text
		)
	`).Error; err != nil {
		t.Fatalf("创建部分完成迁移的上传任务表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO db_upload_tasks (id, source, source_type, remote_file_id, remote_full_path, remote_pick_code, remote_path_id, file_name, completed_pick_code)
		VALUES (1, 'strm_sync', '115', 'already-migrated-file-id', '/remote/movie.mkv', 'already-migrated-pick-code', 'parent-id', 'movie.mkv', 'already-migrated-pick-code')
	`).Error; err != nil {
		t.Fatalf("写入部分完成迁移的上传任务失败: %v", err)
	}

	Migrate()

	var task DbUploadTask
	if err := db.Db.First(&task, 1).Error; err != nil {
		t.Fatalf("读取迁移后的上传任务失败: %v", err)
	}
	if task.RemoteFileId != "already-migrated-file-id" || task.RemotePickCode != "already-migrated-pick-code" || task.ReplacedRemoteFileId != "" {
		t.Fatalf("部分迁移重试不应清空已回填身份: %+v", task)
	}
	if db.Db.Migrator().HasColumn("db_upload_tasks", "completed_pick_code") {
		t.Fatal("重试后应删除剩余的 completed_pick_code 列")
	}
}

func TestMigrateTransferRemoteIdentityPreservesAlreadyMovedDownloadLocators(t *testing.T) {
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(SyncFile{}, DbDownloadTask{}, DbUploadTask{}); err != nil {
		t.Fatalf("创建当前传输任务表失败: %v", err)
	}
	if err := db.Db.Create(&SyncFile{
		BaseModel:  BaseModel{ID: 1},
		SourceType: SourceType115,
		FileId:     "already-migrated-file-id",
	}).Error; err != nil {
		t.Fatalf("创建 PickCode 为空的已关联同步文件失败: %v", err)
	}

	tasks := []DbDownloadTask{
		{
			Source:         DownloadSourceStrm,
			SourceType:     SourceType115,
			SyncFileId:     1,
			FileName:       "115.mkv",
			RemoteFileId:   "already-migrated-file-id",
			RemotePickCode: "legacy-115-pick-code",
			LocalFullPath:  "/library/115.mkv",
			Status:         DownloadStatusPending,
		},
		{
			Source:            DownloadSourceStrm,
			SourceType:        SourceTypeOpenList,
			FileName:          "openlist.mkv",
			RemoteDownloadUrl: "https://openlist.example/d/openlist.mkv?sign=secret",
			LocalFullPath:     "/library/openlist.mkv",
			Status:            DownloadStatusPending,
		},
		{
			Source:            DownloadSourceEmbyMedia,
			SourceType:        SourceTypeEmbyMedia,
			FileName:          "emby.mkv",
			RemoteDownloadUrl: "https://emby.example/extract",
			EmbyItemId:        "emby-item-id",
			Status:            DownloadStatusPending,
		},
		{
			Source:          DownloadSourceLocalFile,
			SourceType:      SourceTypeLocal,
			FileName:        "local.mkv",
			LocalSourcePath: "/source/local.mkv",
			LocalFullPath:   "/library/local.mkv",
			Status:          DownloadStatusPending,
		},
	}
	if err := db.Db.Create(&tasks).Error; err != nil {
		t.Fatalf("创建部分完成迁移下载任务失败: %v", err)
	}

	if err := migrateTransferRemoteIdentity(db.Db); err != nil {
		t.Fatalf("重试传输身份迁移失败: %v", err)
	}

	var got115, gotOpenList, gotEmby, gotLocal DbDownloadTask
	for _, result := range []struct {
		task *DbDownloadTask
		id   uint
	}{
		{task: &got115, id: tasks[0].ID},
		{task: &gotOpenList, id: tasks[1].ID},
		{task: &gotEmby, id: tasks[2].ID},
		{task: &gotLocal, id: tasks[3].ID},
	} {
		if err := db.Db.First(result.task, result.id).Error; err != nil {
			t.Fatalf("读取重试后的下载任务失败: %v", err)
		}
	}
	if got115.RemotePickCode != "legacy-115-pick-code" || got115.RemoteFileId != "already-migrated-file-id" {
		t.Fatalf("115 已迁入 PickCode 被重试破坏: %+v", got115)
	}
	if gotOpenList.RemoteDownloadUrl != "https://openlist.example/d/openlist.mkv?sign=secret" || gotOpenList.RemoteFileId != "" {
		t.Fatalf("OpenList 已迁入直链被重试破坏: %+v", gotOpenList)
	}
	if gotEmby.RemoteDownloadUrl != "https://emby.example/extract" || gotEmby.EmbyItemId != "emby-item-id" || gotEmby.RemoteFileId != "" || gotEmby.RemotePath != "" {
		t.Fatalf("Emby 已迁入执行定位被重试破坏: %+v", gotEmby)
	}
	if gotLocal.LocalSourcePath != "/source/local.mkv" || gotLocal.RemoteFileId != "" || gotLocal.RemotePath != "" {
		t.Fatalf("本地已迁入源路径被重试破坏: %+v", gotLocal)
	}
}

func syncPathIdempotencyColumnNames(t *testing.T) map[string]struct{} {
	t.Helper()
	columns, err := db.Db.Migrator().ColumnTypes(&SyncPathIdempotencyRecord{})
	if err != nil {
		t.Fatalf("读取同步目录幂等表字段失败: %v", err)
	}
	names := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		names[column.Name()] = struct{}{}
	}
	return names
}

func assertDownloadTaskSource(t *testing.T, remoteFileId string, wantSource string, wantSourceType string) {
	t.Helper()
	var task DbDownloadTask
	column := "remote_file_id"
	switch remoteFileId {
	case "download-strm", "download-already-new":
		column = "remote_pick_code"
	case "download-local":
		column = "local_source_path"
	case "download-emby":
		column = "remote_download_url"
	}
	if err := db.Db.Where(column+" = ?", remoteFileId).First(&task).Error; err != nil {
		t.Fatalf("读取下载任务 %s 失败: %v", remoteFileId, err)
	}
	if string(task.Source) != wantSource {
		t.Fatalf("下载任务 %s source = %s，期望 %s", remoteFileId, task.Source, wantSource)
	}
	if string(task.SourceType) != wantSourceType {
		t.Fatalf("下载任务 %s source_type = %s，期望 %s", remoteFileId, task.SourceType, wantSourceType)
	}
}

func assertUploadTaskSource(t *testing.T, localFullPath string, wantSource string) {
	t.Helper()
	var task DbUploadTask
	if err := db.Db.Where("local_full_path = ?", localFullPath).First(&task).Error; err != nil {
		t.Fatalf("读取上传任务 %s 失败: %v", localFullPath, err)
	}
	if string(task.Source) != wantSource {
		t.Fatalf("上传任务 %s source = %s，期望 %s", localFullPath, task.Source, wantSource)
	}
}
