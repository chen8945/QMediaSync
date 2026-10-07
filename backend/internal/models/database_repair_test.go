package models

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestDatabaseRepairAndStartupRejectUntrustedVersion(t *testing.T) {
	for _, test := range []struct {
		name     string
		versions []int
		missing  bool
		wantErr  string
	}{
		{name: "missing_table", missing: true, wantErr: "migrator 缺失"},
		{name: "empty_table", wantErr: "仅包含一条版本记录"},
		{name: "multiple_records", versions: []int{65, 66}, wantErr: "仅包含一条版本记录"},
		{name: "zero_version", versions: []int{0}, wantErr: "不在支持范围"},
		{name: "future_version", versions: []int{MaxVersionCode + 1}, wantErr: "不在支持范围"},
	} {
		for _, operation := range []string{"repair", "startup"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				setupFreshInitializationSQLite(t)
				if err := db.Db.AutoMigrate(&Account{}); err != nil {
					t.Fatal(err)
				}
				account := Account{Name: "saved account"}
				if err := db.Db.Create(&account).Error; err != nil {
					t.Fatal(err)
				}
				if !test.missing {
					if err := db.Db.AutoMigrate(&Migrator{}); err != nil {
						t.Fatal(err)
					}
					for _, version := range test.versions {
						if err := db.Db.Create(&Migrator{VersionCode: version}).Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				before, err := db.Db.Migrator().GetTables()
				if err != nil {
					t.Fatal(err)
				}
				if operation == "repair" {
					err = RepairDatabase(db.Db)
				} else {
					err = Migrate()
				}
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, test.wantErr)
				}
				after, err := db.Db.Migrator().GetTables()
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected operation changed tables: before=%v after=%v err=%v", before, after, err)
				}
				var saved Account
				if err := db.Db.First(&saved, account.ID).Error; err != nil || !reflect.DeepEqual(account, saved) {
					t.Fatalf("rejected operation changed account: got=%+v want=%+v err=%v", saved, account, err)
				}
				if !test.missing {
					var versions []int
					if err := db.Db.Model(&Migrator{}).Order("id").Pluck("version_code", &versions).Error; err != nil || !reflect.DeepEqual(versions, append([]int{}, test.versions...)) {
						t.Fatalf("rejected operation changed versions: %v err=%v", versions, err)
					}
				}
			})
		}
	}
}

func TestRepairDatabaseRestoresMissingSchemaWithVersion(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Migrator().DropTable(&SyncFile{}); err != nil {
		t.Fatal(err)
	}
	if err := RepairDatabase(db.Db); err != nil {
		t.Fatal(err)
	}
	if !db.Db.Migrator().HasTable(&SyncFile{}) {
		t.Fatal("repair did not restore missing business table")
	}
	assertSyncFileLookupIndexes(t)
	version, err := readMigrationRecord(db.Db)
	if err != nil || version.VersionCode != MaxVersionCode {
		t.Fatalf("repair changed trusted version metadata: %+v, %v", version, err)
	}
}

func TestRestoreSchemaUsesCallerTransaction(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		tables := []any{ApiKey{}, User{}, DbDownloadTask{}, DbUploadTask{}, SyncFile{}, StrmGenerationTask{}}
		if err := CreateRestoreSchema(tx, tables); err != nil {
			return err
		}
		if !tx.Migrator().HasConstraint(&ApiKey{}, "fk_api_keys_user") {
			t.Fatal("restore did not create the API key foreign key")
		}
		if err := EnsureRestoreIndexes(tx, tables); err != nil {
			return err
		}
		for _, test := range []struct {
			model any
			index string
		}{
			{DbDownloadTask{}, activeDownloadTaskUniqueIndexName},
			{DbUploadTask{}, activeUploadTaskUniqueIndexName},
			{SyncFile{}, syncFileSiblingPathIndexName},
			{StrmGenerationTask{}, strmGenerationQueueIndexName},
		} {
			if !tx.Migrator().HasIndex(test.model, test.index) {
				t.Fatalf("restore omitted index %s", test.index)
			}
		}
		return RepairSequencesTx(tx, tables)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreIndexesRejectDuplicatesWithoutChangingTasks(t *testing.T) {
	for _, test := range []struct {
		name  string
		model any
		sql   string
	}{
		{
			name: "download", model: DbDownloadTask{},
			sql: `INSERT INTO db_download_tasks (id, source, source_type, account_id, dedup_scope_hash, dedup_locator_hash, status)
				VALUES (1, 'sync', '115', 1, 'scope', 'locator', 0), (2, 'sync', '115', 1, 'scope', 'locator', 0)`,
		},
		{
			name: "upload", model: DbUploadTask{},
			sql: `INSERT INTO db_upload_tasks (id, source, source_type, account_id, remote_full_path, status)
				VALUES (1, 'sync', '115', 1, '/duplicate/file', 0), (2, 'sync', '115', 1, '/duplicate/file', 0)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			setupFreshInitializationSQLite(t)
			if err := db.Db.AutoMigrate(test.model); err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Exec(test.sql).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Transaction(func(tx *gorm.DB) error {
				return EnsureRestoreIndexes(tx, []any{test.model})
			}); err == nil {
				t.Fatal("duplicate active tasks should reject index creation")
			}
			var statuses []int
			if err := db.Db.Model(test.model).Order("id").Pluck("status", &statuses).Error; err != nil || !reflect.DeepEqual(statuses, []int{0, 0}) {
				t.Fatalf("restore changed duplicate task status: %v, %v", statuses, err)
			}
		})
	}
}
