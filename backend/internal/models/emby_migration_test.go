package models

import (
	"io"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestEmbyDeletionMigrationSQLite(t *testing.T) {
	for _, scenario := range []string{"fresh", "upgrade", "table_failure", "version_failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousLogger := db.Db, helpers.AppLogger
			db.Db = conn
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() {
				db.Db, helpers.AppLogger = previousDB, previousLogger
				if err := sqlDB.Close(); err != nil {
					t.Error(err)
				}
			})
			testEmbyDeletionMigration(t, conn, scenario)
		})
	}
}

// 两种数据库共享同一组旧 65 结构、数据与事务断言。
func testEmbyDeletionMigration(t *testing.T, conn *gorm.DB, scenario string) {
	t.Helper()
	if MaxVersionCode != 66 {
		t.Fatalf("latest schema = %d, want 66", MaxVersionCode)
	}
	if scenario == "fresh" {
		for range 2 {
			Migrate()
			assertEmbyMigrationVersion(t, conn, 66)
			assertEmbyDeletionSchema(t, conn)
		}
		assertEmbyMigrationRetainsCurrentRows(t, conn)
		return
	}
	items, links := seedEmbyMigrationVersion65(t, conn)
	if scenario == "table_failure" || scenario == "version_failure" {
		var migrationLog strings.Builder
		previousLogger := helpers.AppLogger
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(&migrationLog, "", 0)}
		t.Cleanup(func() { helpers.AppLogger = previousLogger })
		var removeFailure func()
		failureMessage := "injected emby version failure"
		if scenario == "table_failure" {
			removeFailure = failEmbyMigrationTableCreation(t, conn)
			failureMessage = "FAIL_EMBY_TABLE_CREATE"
		} else {
			removeFailure = failEmbyMigrationVersionUpdate(t, conn)
		}
		Migrate()
		if !strings.Contains(migrationLog.String(), failureMessage) {
			t.Fatalf("migration did not reach %s: %s", scenario, migrationLog.String())
		}
		assertEmbyMigrationVersion(t, conn, 65)
		for _, model := range embyMigrationNewTables() {
			if conn.Migrator().HasTable(model) {
				t.Fatalf("%s retained new table %T", scenario, model)
			}
		}
		for _, column := range embyMigrationAddedColumns() {
			if conn.Migrator().HasColumn(column.model, column.field) {
				t.Fatalf("%s retained %T.%s", scenario, column.model, column.field)
			}
		}
		assertEmbyMigrationLegacyRows(t, conn, items, links)
		removeFailure()
	}
	for range 2 {
		Migrate()
		assertEmbyMigrationVersion(t, conn, 66)
		assertEmbyDeletionSchema(t, conn)
		assertEmbyMigrationLegacyRows(t, conn, items, links)
		for _, model := range embyMigrationNewTables() {
			var count int64
			if err := conn.Model(model).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("migration fabricated %T evidence/work: count=%d err=%v", model, count, err)
			}
		}
	}
	assertEmbyMigrationRetainsCurrentRows(t, conn)
	assertEmbyMigrationLegacyRows(t, conn, items, links)
}

func seedEmbyMigrationVersion65(t *testing.T, conn *gorm.DB) ([]EmbyMediaItem, []EmbyMediaSyncFile) {
	t.Helper()
	if err := conn.AutoMigrate(&Migrator{}, &EmbyMediaItem{}, &EmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	items := []EmbyMediaItem{
		{
			BaseModel: BaseModel{ID: 7, CreatedAt: 11, UpdatedAt: 12},
			ItemId:    "9101", ItemIdInt: 9101, Name: "legacy", Type: "Movie",
			Path: "/local/movie.strm", PickCode: "old-pick", LastSeenSyncRun: "old-run",
		},
		{
			BaseModel: BaseModel{ID: 8, CreatedAt: 21, UpdatedAt: 22},
			ItemId:    "9102", ItemIdInt: 9102, ServerId: "old-server", Type: "Episode",
			SeasonId: "season-1", LibraryId: "library-1", Path: "/local/episode.strm",
		},
	}
	links := []EmbyMediaSyncFile{
		{
			BaseModel:  BaseModel{ID: 3, CreatedAt: 13, UpdatedAt: 14},
			EmbyItemId: 9101, SyncFileId: 29, SyncPathId: 41, PickCode: "old-pick",
		},
	}
	if err := conn.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&links).Error; err != nil {
		t.Fatal(err)
	}
	// 去掉本次新增列，保留真实旧版的远端数值 ID 与媒体字段。
	for _, column := range embyMigrationAddedColumns() {
		if err := conn.Migrator().DropColumn(column.model, column.field); err != nil {
			t.Fatal(err)
		}
	}
	// SQLite 的 DropColumn 会重建表并丢掉索引；只恢复真实 65 版索引，不再迁入新列。
	for _, index := range embyMigrationLegacyIndexes() {
		if !conn.Migrator().HasIndex(index.model, index.name) {
			if err := conn.Migrator().CreateIndex(index.model, index.name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := conn.Create(&Migrator{VersionCode: 65}).Error; err != nil {
		t.Fatal(err)
	}
	assertEmbyMigrationLegacyRows(t, conn, items, links)
	return items, links
}

// 在最后一张工作表的 CREATE TABLE 中注入无效语法，让真实数据库拒绝后段 DDL。
func failEmbyMigrationTableCreation(t *testing.T, conn *gorm.DB) func() {
	t.Helper()
	const callback = "test:emby_migration_table_failure"
	if err := conn.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if strings.HasPrefix(query, "CREATE TABLE") && strings.Contains(query, "emby_webhook_targets") {
			tx.Statement.SQL.WriteString(" FAIL_EMBY_TABLE_CREATE")
		}
	}); err != nil {
		t.Fatal(err)
	}
	removed := false
	remove := func() {
		if !removed {
			if err := conn.Callback().Raw().Remove(callback); err != nil {
				t.Fatal(err)
			}
			removed = true
		}
	}
	t.Cleanup(remove)
	return remove
}

// 用数据库触发器令最后的版本写入真正失败，覆盖事务内所有 DDL 已完成的情况。
func failEmbyMigrationVersionUpdate(t *testing.T, conn *gorm.DB) func() {
	t.Helper()
	statements := []string{
		`CREATE TRIGGER fail_emby_version BEFORE UPDATE OF version_code ON migrator
		 BEGIN SELECT RAISE(ABORT, 'injected emby version failure'); END`,
	}
	if conn.Dialector.Name() == "postgres" {
		statements = []string{
			`CREATE FUNCTION fail_emby_version() RETURNS trigger LANGUAGE plpgsql AS $$
			 BEGIN RAISE EXCEPTION 'injected emby version failure'; END $$`,
			`CREATE TRIGGER fail_emby_version BEFORE UPDATE OF version_code ON migrator
			 FOR EACH ROW EXECUTE FUNCTION fail_emby_version()`,
		}
	}
	for _, statement := range statements {
		if err := conn.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		statement := "DROP TRIGGER fail_emby_version"
		if conn.Dialector.Name() == "postgres" {
			statement += " ON migrator"
		}
		if err := conn.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
		if conn.Dialector.Name() == "postgres" {
			if err := conn.Exec("DROP FUNCTION fail_emby_version()").Error; err != nil {
				t.Fatal(err)
			}
		}
	}
}

func embyMigrationNewTables() []any {
	return []any{&EmbyIndexState{}, &EmbyItemState{}, &EmbyItemEvidence{}, &EmbyWebhookRecord{}, &EmbyWebhookTarget{}}
}

func embyMigrationAddedColumns() []struct {
	model any
	field string
} {
	return []struct {
		model any
		field string
	}{
		{&EmbyMediaItem{}, "PartCount"},
		{&EmbyMediaItem{}, "PartOfItemID"},
		{&EmbyMediaItem{}, "VersionOfItemID"},
		{&EmbyMediaItem{}, "SnapshotID"},
		{&EmbyMediaItem{}, "Generation"},
		{&EmbyMediaSyncFile{}, "SnapshotID"},
		{&EmbyMediaSyncFile{}, "SourceID"},
	}
}

// ac76cf9（物理快照迁移之前）的媒体及关联索引；这些字段的当前标签未变化。
func embyMigrationLegacyIndexes() []struct {
	model any
	name  string
} {
	return []struct {
		model any
		name  string
	}{
		{&EmbyMediaItem{}, "idx_emby_item_id"},
		{&EmbyMediaItem{}, "idx_emby_item_id_int"},
		{&EmbyMediaItem{}, "idx_emby_server_id"},
		{&EmbyMediaItem{}, "idx_emby_type"},
		{&EmbyMediaItem{}, "idx_emby_parent_id"},
		{&EmbyMediaItem{}, "idx_emby_series_id"},
		{&EmbyMediaItem{}, "idx_emby_season_id"},
		{&EmbyMediaItem{}, "idx_emby_library_id"},
		{&EmbyMediaItem{}, "idx_emby_pick_code"},
		{&EmbyMediaItem{}, "idx_emby_date_created_time"},
		{&EmbyMediaItem{}, "idx_emby_media_items_last_seen_sync_run"},
		{&EmbyMediaItem{}, "idx_emby_media_items_last_seen_at"},
		{&EmbyMediaSyncFile{}, "idx_emby_sync_path_id"},
		{&EmbyMediaSyncFile{}, "idx_emby_media_item_id"},
		{&EmbyMediaSyncFile{}, "idx_emby_sync_file_id"},
		{&EmbyMediaSyncFile{}, "idx_emby_sf_pick_code"},
	}
}

func assertEmbyMigrationVersion(t *testing.T, conn *gorm.DB, want int) {
	t.Helper()
	var version Migrator
	if err := conn.First(&version).Error; err != nil || version.VersionCode != want {
		t.Fatalf("schema version=%+v want=%d err=%v", version, want, err)
	}
}

func assertEmbyMigrationLegacyRows(t *testing.T, conn *gorm.DB, wantItems []EmbyMediaItem, wantLinks []EmbyMediaSyncFile) {
	t.Helper()
	var items []EmbyMediaItem
	var links []EmbyMediaSyncFile
	// 同一连接跨越 DDL 后仍按实际结构显式选列，避免 PostgreSQL 复用旧 SELECT * 的返回类型。
	itemQuery := conn.Session(&gorm.Session{QueryFields: true})
	linkQuery := conn.Session(&gorm.Session{QueryFields: true})
	if !conn.Migrator().HasColumn(&EmbyMediaItem{}, "SnapshotID") {
		itemQuery = itemQuery.Omit("PartCount", "PartOfItemID", "VersionOfItemID", "SnapshotID", "Generation")
		linkQuery = linkQuery.Omit("SnapshotID", "SourceID")
	}
	if err := itemQuery.Order("id").Find(&items).Error; err != nil {
		t.Fatalf("read historical media rows: %v", err)
	}
	if err := linkQuery.Order("id").Find(&links).Error; err != nil {
		t.Fatalf("read historical media links: %v", err)
	}
	if !reflect.DeepEqual(items, wantItems) || !reflect.DeepEqual(links, wantLinks) {
		t.Fatalf("migration changed historical rows: items=%+v links=%+v", items, links)
	}
	for _, index := range embyMigrationLegacyIndexes() {
		if !conn.Migrator().HasIndex(index.model, index.name) {
			t.Fatalf("missing version 65 index %s", index.name)
		}
	}
	// 显式使用不冲突的本地主键，确保失败来自旧 item_id 唯一约束；只写两个旧版字段。
	err := conn.Table("emby_media_items").Create(map[string]any{"id": 99, "item_id": wantItems[0].ItemId}).Error
	wantConstraint := "UNIQUE constraint failed: emby_media_items.item_id"
	if conn.Dialector.Name() == "postgres" {
		wantConstraint = "idx_emby_item_id"
	}
	if err == nil || !strings.Contains(err.Error(), wantConstraint) {
		t.Fatalf("item_id uniqueness not enforced: %v", err)
	}
}

func assertEmbyMigrationRetainsCurrentRows(t *testing.T, conn *gorm.DB) {
	t.Helper()
	base := BaseModel{ID: 1, CreatedAt: 31, UpdatedAt: 32}
	rows := []any{
		&EmbyIndexState{
			BaseModel: base, ServerID: "server-current", ConfigKey: "scope-key", ServerConfigKey: "connection-key", Revision: 9,
		},
		&EmbyItemState{
			BaseModel: base, ServerID: "server-current", ItemID: "9201", Generation: 3, SnapshotID: 1,
			IdentityKey: "identity-key", Deleted: true, Revision: 9,
		},
		&EmbyItemEvidence{
			BaseModel: base, ServerID: "server-current", ConfigKey: "scope-key", ServerConfigKey: "connection-key",
			ItemID: "9201", Generation: 3, IdentityKey: "identity-key", EvidenceKey: "evidence-key",
			ItemJSON:     `{"item_id":"9201","type":"Movie","generation":3}`,
			SourcesJSON:  `[{"id":"source-1","item_id":"9201"}]`,
			FilesJSON:    `[{"file_id":"video-1","account_id":1}]`,
			SidecarsJSON: `[{"file_id":"sidecar-1","account_id":1}]`,
		},
		&EmbyWebhookRecord{
			BaseModel: base, Event: "library.deleted", ServerID: "server-current", ServerConfigKey: "connection-key",
			ItemID: "9201", ItemType: "Movie", Authorized: true, PayloadJSON: `{"event":"library.deleted","item_id":"9201"}`,
			InputJSON: `{"item_id":"9201","authorized":true}`, PlanJSON: `{"targets":[{"key":"video-1"},{"key":"sidecar-1"}]}`,
			ObservationJSON: `{"snapshots":[]}`, Status: EmbyWebhookRetry, Reason: "incomplete_targets",
			Attempts: 2, NextAttemptAt: 45, DeletionRevision: 9, ObservedAt: 30,
		},
		&EmbyWebhookTarget{
			BaseModel: base, RecordID: 1, TargetKey: "video-1", ServerID: "server-current", ServerConfigKey: "connection-key",
			TargetJSON: `{"key":"video-1","kind":"video"}`, Outcome: EmbyDeletionDeleted, Attempts: 1,
		},
		&EmbyWebhookTarget{
			BaseModel: BaseModel{ID: 2, CreatedAt: 31, UpdatedAt: 32}, RecordID: 1, TargetKey: "sidecar-1",
			ServerID: "server-current", ServerConfigKey: "connection-key", TargetJSON: `{"key":"sidecar-1","kind":"sidecar"}`,
			Outcome: EmbyDeletionFailed, Reason: "provider_timeout", Attempts: 2,
		},
	}
	for _, row := range rows {
		if err := conn.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		Migrate()
		assertEmbyMigrationVersion(t, conn, 66)
		for _, before := range rows {
			value := reflect.Indirect(reflect.ValueOf(before))
			after := reflect.New(value.Type()).Interface()
			if err := conn.First(after, value.FieldByName("ID").Uint()).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("repeated startup changed %T: before=%+v after=%+v", before, before, after)
			}
		}
		for _, model := range embyMigrationNewTables() {
			want := int64(1)
			if _, ok := model.(*EmbyWebhookTarget); ok {
				want = 2
			}
			var count int64
			if err := conn.Model(model).Count(&count).Error; err != nil || count != want {
				t.Fatalf("repeated startup changed %T count: got=%d want=%d err=%v", model, count, want, err)
			}
		}
	}
}

func assertEmbyDeletionSchema(t *testing.T, conn *gorm.DB) {
	t.Helper()
	models := append(embyMigrationNewTables(), &EmbyMediaItem{}, &EmbyMediaSyncFile{})
	for _, model := range models {
		if !conn.Migrator().HasTable(model) {
			t.Fatalf("missing %T", model)
		}
		registrations := 0
		for _, registered := range AllTables {
			if reflect.Indirect(reflect.ValueOf(registered)).Type() == reflect.Indirect(reflect.ValueOf(model)).Type() {
				registrations++
			}
		}
		if registrations != 1 {
			t.Fatalf("%T has %d schema/backup registrations, want 1", model, registrations)
		}
	}
	for _, column := range embyMigrationAddedColumns() {
		if !conn.Migrator().HasColumn(column.model, column.field) {
			t.Fatalf("missing %T.%s", column.model, column.field)
		}
	}
	if !conn.Migrator().HasColumn(&EmbyItemEvidence{}, "SidecarsJSON") {
		t.Fatal("missing sidecar history column")
	}
	indexes := append(embyMigrationLegacyIndexes(), []struct {
		model any
		name  string
	}{
		{&EmbyItemState{}, "idx_emby_item_state"},
		{&EmbyItemEvidence{}, "idx_emby_evidence_item"},
		{&EmbyWebhookRecord{}, "idx_emby_webhook_queue"},
		{&EmbyWebhookTarget{}, "idx_emby_webhook_target"},
		{&EmbyWebhookTarget{}, "idx_emby_webhook_success"},
	}...)
	for _, index := range indexes {
		if !conn.Migrator().HasIndex(index.model, index.name) {
			t.Fatalf("missing index %s", index.name)
		}
	}
}
