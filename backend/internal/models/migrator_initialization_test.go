package models

import (
	"errors"
	"io"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

type freshInitializationFailure struct {
	name      string
	operation string
	model     any
	sql       string
	skip      int
}

func freshInitializationFailures() []freshInitializationFailure {
	return []freshInitializationFailure{
		{name: "late_table", operation: "raw", sql: `CREATE TABLE`, model: &EmbyWebhookTarget{}},
		{name: "required_index", operation: "raw", sql: "idx_sync_files_sibling_path"},
		{name: "transfer_index", operation: "raw", sql: activeUploadTaskUniqueIndexName},
		{name: "queue_index", operation: "raw", sql: strmGenerationQueueIndexName},
		{name: "settings_query", operation: "query", model: &Settings{}},
		{name: "settings_save", operation: "create", model: &Settings{}},
		{name: "scrape_query", operation: "query", model: &ScrapeSettings{}},
		{name: "scrape_save", operation: "create", model: &ScrapeSettings{}},
		{name: "movie_category", operation: "create", model: &MovieCategory{}, skip: 1},
		{name: "tv_category", operation: "create", model: &TvShowCategory{}, skip: 7},
		{name: "emby_config", operation: "create", model: &EmbyConfig{}},
		{name: "version_save", operation: "version", model: &Migrator{}},
	}
}

func TestMigrateFreshSQLiteRollsBackAndRetries(t *testing.T) {
	for _, failure := range freshInitializationFailures() {
		t.Run(failure.name, func(t *testing.T) {
			reopen := setupFreshInitializationSQLite(t)
			testFreshInitializationFailure(t, db.Db, reopen, failure)
		})
	}
}

func TestMigrateFreshSQLiteDefaultsAndRepeatedStartup(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	assertFreshInitializationDefaults(t, db.Db)
	assertFreshInitializationPreservesSettings(t, db.Db)
}

func TestBatchCreateTableKeepsRepairContinuation(t *testing.T) {
	setupFreshInitializationSQLite(t)
	_, injected, _ := failFreshInitialization(t, db.Db, freshInitializationFailure{
		name: "late_table", operation: "raw", sql: "CREATE TABLE", model: &EmbyWebhookTarget{},
	})
	if err := BatchCreateTable(); err == nil || !strings.Contains(err.Error(), "INITDB_FAILURE") || !*injected {
		t.Fatalf("repair did not report the table failure: %v", err)
	}
	if !db.Db.Migrator().HasTable(&NotificationRule{}) || db.Db.Migrator().HasTable(&EmbyWebhookTarget{}) {
		t.Fatal("repair no longer attempts tables after a failure")
	}
}

func TestInitScrapeSettingKeepsLegacyFailureContinuation(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := db.Db.AutoMigrate(&ScrapeSettings{}, &MovieCategory{}, &TvShowCategory{}); err != nil {
		t.Fatal(err)
	}
	_, injected, _ := failFreshInitialization(t, db.Db, freshInitializationFailure{
		name: "movie_category", operation: "create", model: &MovieCategory{}, skip: 1,
	})
	InitScrapeSetting()
	if !*injected {
		t.Fatal("did not reach legacy category failure")
	}
	var names []string
	if err := db.Db.Model(&MovieCategory{}).Order("id").Pluck("name", &names).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"外语电影", "动画电影"}) {
		t.Fatalf("legacy initialization did not continue after failed category: %v", names)
	}
	var count int64
	if err := db.Db.Model(&TvShowCategory{}).Count(&count).Error; err != nil || count != 8 {
		t.Fatalf("legacy initialization skipped later categories: count=%d err=%v", count, err)
	}
}

func setupFreshInitializationSQLite(t *testing.T) func() *gorm.DB {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "fresh.db")
	previousDB, previousLogger := db.Db, helpers.AppLogger
	var conn *gorm.DB
	open := func() *gorm.DB {
		if conn != nil {
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		conn, err = gorm.Open(sqlite.Open(filename), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		db.Db = conn
		return conn
	}
	open()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		sqlDB, err := conn.DB()
		if err != nil {
			t.Error(err)
		} else if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
		db.Db, helpers.AppLogger = previousDB, previousLogger
	})
	return open
}

func testFreshInitializationFailure(t *testing.T, conn *gorm.DB, reopen func() *gorm.DB, failure freshInitializationFailure) {
	t.Helper()
	var output strings.Builder
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	remove, injected, expected := failFreshInitialization(t, conn, failure)
	err := Migrate()
	if failure.operation == "raw" {
		if err == nil || !strings.Contains(err.Error(), "INITDB_FAILURE") {
			t.Fatalf("lost original database initialization error: %v", err)
		}
	} else if !errors.Is(err, expected) {
		t.Fatalf("lost initialization error: got=%v want=%v", err, expected)
	}
	if !*injected {
		t.Fatal("did not reach injected initialization failure")
	}
	assertFreshInitializationRolledBack(t, conn)
	if strings.Contains(output.String(), "已完成数据库初始化") || strings.Contains(output.String(), "已默认添加") || strings.Contains(output.String(), "初始化数据库版本表，当前版本") {
		t.Fatalf("failed initialization logged success: %s", output.String())
	}
	remove()
	conn = reopen()
	assertFreshInitializationRolledBack(t, conn)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	assertFreshInitializationDefaults(t, conn)
	assertFreshInitializationPreservesSettings(t, conn)
}

func failFreshInitialization(t *testing.T, conn *gorm.DB, failure freshInitializationFailure) (func(), *bool, error) {
	t.Helper()
	injected := false
	expected := errors.New("injected initialization failure: " + failure.name)
	hits := 0
	callback := func(tx *gorm.DB) {
		if failure.operation == "raw" {
			query := tx.Statement.SQL.String()
			if !strings.Contains(query, failure.sql) || failure.model != nil && !strings.Contains(query, GetTableName(failure.model)) {
				return
			}
		} else if tx.Statement.Table != GetTableName(failure.model) {
			return
		}
		hits++
		if hits != failure.skip+1 {
			return
		}
		injected = true
		if failure.operation == "raw" {
			tx.Statement.SQL.WriteString(" INITDB_FAILURE")
		} else {
			tx.AddError(expected)
		}
	}
	operations := []string{failure.operation}
	if failure.operation == "version" {
		operations = []string{"create", "update"}
	}
	removers := []func(){}
	for _, operation := range operations {
		processor := conn.Callback().Raw()
		switch operation {
		case "create":
			processor = conn.Callback().Create()
		case "update":
			processor = conn.Callback().Update()
		case "query":
			processor = conn.Callback().Query()
		}
		const name = "test:fresh_initialization_failure"
		if err := processor.Before("gorm:"+operation).Register(name, callback); err != nil {
			t.Fatal(err)
		}
		removed := false
		removers = append(removers, func() {
			if !removed {
				if err := processor.Remove(name); err != nil {
					t.Error(err)
				}
				removed = true
			}
		})
	}
	remove := func() {
		for _, remove := range removers {
			remove()
		}
	}
	t.Cleanup(remove)
	return remove, &injected, expected
}

func assertFreshInitializationRolledBack(t *testing.T, conn *gorm.DB) {
	t.Helper()
	tables := []string{}
	for _, model := range AllTables {
		if conn.Migrator().HasTable(model) {
			tables = append(tables, GetTableName(model))
		}
	}
	if len(tables) != 0 {
		t.Fatalf("failed fresh initialization retained tables: %v", tables)
	}
}

func assertFreshInitializationDefaults(t *testing.T, conn *gorm.DB) {
	t.Helper()
	assertEmbyMigrationVersion(t, conn, MaxVersionCode)
	for _, model := range AllTables {
		if !conn.Migrator().HasTable(model) {
			t.Fatalf("missing initialized table %s", GetTableName(model))
		}
	}
	for _, entry := range []struct {
		model any
		count int64
	}{
		{&Migrator{}, 1}, {&Settings{}, 1}, {&ScrapeSettings{}, 1}, {&MovieCategory{}, 3},
		{&TvShowCategory{}, 8}, {&EmbyConfig{}, 1}, {&User{}, 0},
	} {
		var count int64
		if err := conn.Model(entry.model).Count(&count).Error; err != nil || count != entry.count {
			t.Fatalf("initial %T count=%d want=%d err=%v", entry.model, count, entry.count, err)
		}
	}
	var movies []MovieCategory
	var tvShows []TvShowCategory
	if err := conn.Order("id").Find(&movies).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Order("id").Find(&tvShows).Error; err != nil {
		t.Fatal(err)
	}
	if movies[0].ID != 1 || tvShows[0].ID != 1 {
		t.Fatalf("default protected category IDs changed: movie=%d tv=%d", movies[0].ID, tvShows[0].ID)
	}
	for i := range movies {
		movies[i].BaseModel = BaseModel{}
	}
	for i := range tvShows {
		tvShows[i].BaseModel = BaseModel{}
	}
	wantMovies := []MovieCategory{
		{Name: "外语电影", GenreIds: "[]", Language: "[]"},
		{Name: "华语电影", GenreIds: "[]", Language: `["zh", "cn", "bo","za"]`},
		{Name: "动画电影", GenreIds: "[16]"},
	}
	wantTVShows := []TvShowCategory{
		{Name: "其他剧"},
		{Name: "国产剧", Countries: `["CN","TW", "HK", "MO"]`},
		{Name: "欧美剧", Countries: `["US","GB", "DE", "FR", "ES", "IT", "PT", "RU", "UA"]`},
		{Name: "日韩泰剧", Countries: `["JP","KR", "KP", "TH", "IN", "SG"]`},
		{Name: "国漫", GenreIds: "[16]", Countries: `["CN","TW", "HK","MO"]`},
		{Name: "日番", GenreIds: "[16]", Countries: `["JP"]`},
		{Name: "综艺", GenreIds: "[10764, 10767]"},
		{Name: "纪录片", GenreIds: "[99]"},
	}
	if !reflect.DeepEqual(movies, wantMovies) || !reflect.DeepEqual(tvShows, wantTVShows) {
		t.Fatalf("changed default categories or order: movies=%+v tv=%+v", movies, tvShows)
	}
	assertSyncFileLookupIndexes(t)
	if !conn.Migrator().HasIndex(&DbUploadTask{}, activeUploadTaskUniqueIndexName) {
		t.Fatal("missing required upload uniqueness index")
	}
	if !conn.Migrator().HasIndex(&StrmGenerationTask{}, strmGenerationQueueIndexName) {
		t.Fatal("missing required queue index")
	}
}

func assertFreshInitializationPreservesSettings(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := conn.Model(&Settings{}).Where("id > 0").Update("cron", "saved-user-cron").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&EmbyConfig{}).Where("id > 0").Update("emby_url", "http://saved.invalid").Error; err != nil {
		t.Fatal(err)
	}
	models := []any{&Settings{}, &ScrapeSettings{}, &MovieCategory{}, &TvShowCategory{}, &EmbyConfig{}, &Migrator{}}
	before := make([]any, len(models))
	for i, model := range models {
		before[i] = reflect.New(reflect.SliceOf(reflect.Indirect(reflect.ValueOf(model)).Type())).Interface()
		if err := conn.Order("id").Find(before[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
		for i, model := range models {
			after := reflect.New(reflect.SliceOf(reflect.Indirect(reflect.ValueOf(model)).Type())).Interface()
			if err := conn.Order("id").Find(after).Error; err != nil || !reflect.DeepEqual(before[i], after) {
				t.Fatalf("repeated startup changed %T: before=%+v after=%+v err=%v", model, before[i], after, err)
			}
		}
	}
}
