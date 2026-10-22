package models

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbySyncMigrationSQLite(t *testing.T) {
	scenarios := []string{"upgrade", "table_failure", "column_failure", "index_failure", "version_failure"}
	for _, index := range sqliteEmbyLookupIndexes {
		scenarios = append(scenarios, "index_failure:"+index.name)
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			reopen := setupFreshInitializationSQLite(t)
			testEmbySyncMigration(t, db.Db, reopen, scenario)
		})
	}
}

// 两种数据库都从没有根资料及映射字段的旧结构升级，并在连接重开后校验历史不变。
func testEmbySyncMigration(t *testing.T, conn *gorm.DB, reopen func() *gorm.DB, scenario string) {
	t.Helper()
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().DropIndex(&SyncFile{}, syncFileNameIndexName); err != nil {
		t.Fatal(err)
	}
	if conn.Dialector.Name() == "sqlite" {
		for _, index := range sqliteEmbyLookupIndexes {
			if err := conn.Migrator().DropIndex(index.model, index.name); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertFileNames := seedSyncFileNameIndexRows(t, conn)
	assertMediaMembers := seedEmbyMemberIndexRows(t, conn)
	evidence := EmbyItemEvidence{
		ServerID: "migration-server", ItemID: "101", Generation: 7,
		ItemJSON:     `{"item_id":"101","path":"/media/movie.strm"}`,
		SourcesJSON:  `[{"id":"source","item_id":"101","path":"https://media.invalid/play?pickcode=old","pick_code":"old"}]`,
		FilesJSON:    `[{"file_id":"old-file","account_id":1,"mtime":17}]`,
		SidecarsJSON: `{"version":2,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`,
	}
	if err := conn.Create(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	record := EmbyWebhookRecord{
		ServerID: "migration-server", ItemID: "101", ItemType: "Movie", Event: "library.deleted",
		Authorized: true, Status: EmbyWebhookPending, Attempts: 2, ObservedAt: 19,
		InputJSON: `{"item_id":"101","authorized":true,"owners":[]}`,
		PlanJSON:  `{"input":{"item_id":"101","authorized":true},"targets":[]}`,
	}
	if err := conn.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().DropTable(&SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().DropColumn(&EmbyConfig{}, "PathMappingsJSON"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&Migrator{}).Where("1 = 1").UpdateColumn("version_code", 67).Error; err != nil {
		t.Fatal(err)
	}
	assertHistory := func() {
		t.Helper()
		assertFileNames(conn)
		assertMediaMembers(conn)
		var gotEvidence EmbyItemEvidence
		if err := conn.First(&gotEvidence, evidence.ID).Error; err != nil || !reflect.DeepEqual(gotEvidence, evidence) {
			t.Fatalf("migration changed immutable evidence: got=%+v err=%v", gotEvidence, err)
		}
		var gotRecord EmbyWebhookRecord
		if err := conn.First(&gotRecord, record.ID).Error; err != nil || !reflect.DeepEqual(gotRecord, record) {
			t.Fatalf("migration changed pending deletion: got=%+v err=%v", gotRecord, err)
		}
	}
	if scenario != "upgrade" {
		failure := freshInitializationFailure{name: scenario, operation: "raw", sql: "CREATE TABLE", model: &SyncRootEvidence{}}
		if scenario == "column_failure" {
			failure.sql, failure.model = "ALTER TABLE", &EmbyConfig{}
		} else if scenario == "index_failure" {
			failure.sql, failure.model = "CREATE INDEX IF NOT EXISTS "+syncFileNameIndexName, &SyncFile{}
		} else if indexName, ok := strings.CutPrefix(scenario, "index_failure:"); ok {
			failure.sql, failure.model = "CREATE INDEX IF NOT EXISTS "+indexName, nil
		} else if scenario == "version_failure" {
			failure.operation, failure.model = "update", &Migrator{}
		}
		remove, injected, _ := failFreshInitialization(t, conn, failure)
		if err := Migrate(); err == nil || !*injected {
			t.Fatalf("migration must propagate %s: reached=%v err=%v", scenario, *injected, err)
		}
		remove()
		assertEmbyMigrationVersion(t, conn, 67)
		assertSQLiteEmbyIndexesAbsent(t, conn)
		if conn.Migrator().HasTable(&SyncRootEvidence{}) || conn.Migrator().HasColumn(&EmbyConfig{}, "PathMappingsJSON") || conn.Migrator().HasIndex(&SyncFile{}, syncFileNameIndexName) {
			t.Fatal("failed migration retained a partial new schema")
		}
		assertHistory()
	}
	conn = reopen()
	if scenario != "upgrade" {
		assertEmbyMigrationVersion(t, conn, 67)
		assertSQLiteEmbyIndexesAbsent(t, conn)
		if conn.Migrator().HasTable(&SyncRootEvidence{}) || conn.Migrator().HasColumn(&EmbyConfig{}, "PathMappingsJSON") || conn.Migrator().HasIndex(&SyncFile{}, syncFileNameIndexName) {
			t.Fatal("reopened database retained a partial failed migration")
		}
		assertHistory()
	}
	for range 2 {
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
		assertEmbyMigrationVersion(t, conn, MaxVersionCode)
		if !conn.Migrator().HasTable(&SyncRootEvidence{}) || !conn.Migrator().HasColumn(&EmbyConfig{}, "PathMappingsJSON") {
			t.Fatal("migration did not create the complete new schema")
		}
		assertSyncFileLookupIndexes(t)
		assertSQLiteEmbyLookupIndexes(t, conn)
		var roots int64
		if err := conn.Model(&SyncRootEvidence{}).Count(&roots).Error; err != nil || roots != 0 {
			t.Fatalf("migration fabricated cloud observations: roots=%d err=%v", roots, err)
		}
		var mappings string
		if err := conn.Model(&EmbyConfig{}).Select("path_mappings_json").Scan(&mappings).Error; err != nil || mappings != "[]" {
			t.Fatalf("legacy config did not receive an empty mapping: %q %v", mappings, err)
		}
		assertHistory()
		conn = reopen()
	}
}
