package models

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestStrmGenerationQueueCursor(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)
	tasks := []StrmGenerationTask{
		{BaseModel: BaseModel{CreatedAt: 10}, Status: StrmGenerationStatusPending},
		{BaseModel: BaseModel{CreatedAt: 1}, Status: StrmGenerationStatusFinalizing, LastRetryTime: 10},
		{BaseModel: BaseModel{CreatedAt: 10}, Status: StrmGenerationStatusFinalizing},
		{BaseModel: BaseModel{CreatedAt: 11}, Status: StrmGenerationStatusPending},
		{BaseModel: BaseModel{CreatedAt: 2}, Status: StrmGenerationStatusFinalizing, LastRetryTime: 99},
		{BaseModel: BaseModel{CreatedAt: 3}, Status: StrmGenerationStatusCompleted},
	}
	if err := db.Db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&tasks[2]).UpdateColumn("last_retry_time", nil).Error; err != nil {
		t.Fatal(err)
	}
	first, err := GetPendingStrmGenerationTaskPage(2, nil, 100)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	cursor := first[1].QueueCursor()
	// 前页已被其他处理器终结后，游标仍须保留后页的同时间项。
	if err := db.Db.Model(&StrmGenerationTask{}).Where("id IN ?", []uint{first[0].ID, first[1].ID}).UpdateColumn("status", StrmGenerationStatusCompleted).Error; err != nil {
		t.Fatal(err)
	}
	second, err := GetPendingStrmGenerationTaskPage(2, cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint
	for _, task := range append(first, second...) {
		ids = append(ids, task.ID)
	}
	expected := []uint{tasks[0].ID, tasks[1].ID, tasks[2].ID, tasks[3].ID}
	if !reflect.DeepEqual(ids, expected) {
		t.Fatalf("order = %v, want %v", ids, expected)
	}
}

func TestStrmGenerationQueueQueryPlan(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		setupStrmGenerationTaskTestDB(t)
		testStrmGenerationQueueQueryPlan(t, db.Db)
	})
	t.Run("postgres", func(t *testing.T) {
		conn := setupStrmGenerationQueuePostgres(t)
		testStrmGenerationQueueQueryPlan(t, conn)
	})
}

func setupStrmGenerationQueuePostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	schema := fmt.Sprintf("qms_queue_%d", time.Now().UnixNano())
	if err := conn.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec("DROP SCHEMA " + schema + " CASCADE"); sqlDB.Close() })
	if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&StrmGenerationTask{}); err != nil {
		t.Fatal(err)
	}
	return conn
}

func testStrmGenerationQueueQueryPlan(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := EnsureStrmGenerationQueueIndex(conn); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStrmGenerationQueueIndex(conn); err != nil {
		t.Fatal(err)
	}
	tasks := make([]StrmGenerationTask, 2000)
	for i := range tasks {
		tasks[i] = StrmGenerationTask{BaseModel: BaseModel{CreatedAt: int64(i + 1)}, Status: StrmGenerationStatusPending}
		if i%2 == 1 {
			tasks[i].Status = StrmGenerationStatusFinalizing
			tasks[i].LastRetryTime = int64(i + 1)
		}
	}
	if err := conn.CreateInBatches(&tasks, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("ANALYZE strm_generation_tasks").Error; err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []*StrmGenerationTaskCursor{nil, {Time: 1000, ID: 1000}} {
		var result []*StrmGenerationTask
		query := pendingStrmGenerationTaskQuery(conn.Session(&gorm.Session{DryRun: true}), 5, cursor, 3000).Find(&result)
		prefix := "EXPLAIN "
		if conn.Dialector.Name() == "sqlite" {
			prefix = "EXPLAIN QUERY PLAN "
		}
		rows, err := conn.Raw(prefix+query.Statement.SQL.String(), query.Statement.Vars...).Rows()
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var detail string
			if conn.Dialector.Name() == "sqlite" {
				var id, parent, unused int
				err = rows.Scan(&id, &parent, &unused, &detail)
			} else {
				err = rows.Scan(&detail)
			}
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		t.Log(plan.String())
		if cursor != nil && conn.Dialector.Name() == "sqlite" && !strings.Contains(plan.String(), "SEARCH") {
			t.Fatalf("cursor must seek index: %s", plan.String())
		}
		if !strings.Contains(plan.String(), strmGenerationQueueIndexName) || strings.Contains(plan.String(), "TEMP B-TREE") || strings.Contains(plan.String(), "Sort") {
			t.Fatalf("queue must use ordered index without sort: %s", plan.String())
		}
		if err := pendingStrmGenerationTaskQuery(conn, 5, cursor, 3000).Find(&result).Error; err != nil {
			t.Fatal(err)
		}
		if len(result) != 5 {
			t.Fatalf("got %d tasks", len(result))
		}
		firstID := uint(1)
		if cursor != nil {
			firstID = 1001
		}
		for i, task := range result {
			if task.ID != firstID+uint(i) {
				t.Fatalf("unexpected task %d at %d", task.ID, i)
			}
		}
	}
}

func TestStrmGenerationQueueMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			testStrmGenerationQueueMigration(t, dialect)
		})
	}
}

func testStrmGenerationQueueMigration(t *testing.T, dialect string) {
	t.Helper()
	for _, failIndex := range []bool{false, true} {
		t.Run(fmt.Sprintf("index_failure_%t", failIndex), func(t *testing.T) {
			previousDB := db.Db
			t.Cleanup(func() { db.Db = previousDB })
			setupStrmGenerationTaskTestDB(t)
			if dialect == "postgres" {
				db.Db = setupStrmGenerationQueuePostgres(t)
			}
			if err := db.Db.AutoMigrate(&Migrator{}, &DbUploadTask{}, &DbDownloadTask{}); err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&Migrator{ID: 1, VersionCode: 64}).Error; err != nil {
				t.Fatal(err)
			}
			want := StrmGenerationTask{
				BaseModel: BaseModel{CreatedAt: 11, UpdatedAt: 12},
				Status:    StrmGenerationStatusFinalizing, LastRetryTime: 15,
				SkipReason: "existing reason", SkippedItems: 2,
			}
			if err := db.Db.Create(&want).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Callback().Raw().Before("gorm:raw").Register("test:fail_queue_index", func(tx *gorm.DB) {
				if failIndex && strings.Contains(tx.Statement.SQL.String(), "CREATE INDEX IF NOT EXISTS "+strmGenerationQueueIndexName) {
					tx.AddError(errors.New("queue index creation failed"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			Migrate()
			var version Migrator
			if err := db.Db.First(&version).Error; err != nil {
				t.Fatal(err)
			}
			if failIndex {
				if version.VersionCode != 64 || db.Db.Migrator().HasIndex(&StrmGenerationTask{}, strmGenerationQueueIndexName) {
					t.Fatalf("索引创建失败不能推进版本：%+v", version)
				}
				failIndex = false
				Migrate()
			}
			Migrate()
			if err := db.Db.First(&version).Error; err != nil {
				t.Fatal(err)
			}
			if version.VersionCode != MaxVersionCode || !db.Db.Migrator().HasIndex(&StrmGenerationTask{}, strmGenerationQueueIndexName) {
				t.Fatalf("queue index migration incomplete: version %d", version.VersionCode)
			}
			var got StrmGenerationTask
			if err := db.Db.First(&got, want.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("迁移及重试不能改写任务：got %+v, want %+v", got, want)
			}
		})
	}
}

func TestStrmGenerationQueueRepair(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)
	if err := BatchCreateTable(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Migrator().DropIndex(&StrmGenerationTask{}, strmGenerationQueueIndexName); err != nil {
		t.Fatal(err)
	}
	if err := BatchCreateTable(); err != nil {
		t.Fatal(err)
	}
	if !db.Db.Migrator().HasIndex(&StrmGenerationTask{}, strmGenerationQueueIndexName) {
		t.Fatal("repair missed queue index")
	}
}
