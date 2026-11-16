package synccron

import (
	"errors"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

func scrapePlan(t *testing.T) *cron.Cron {
	t.Helper()
	scrapeCronState.Lock()
	plan := scrapeCronState.current
	scrapeCronState.Unlock()
	return plan
}

func setupScrapeCronTest(t *testing.T) chan *NewSyncTask {
	t.Helper()
	<-StopScrapeCron().Done()
	oldLoad, oldEnqueue := loadScrapeCronPaths, enqueueScrapeCronTask
	tasks := make(chan *NewSyncTask, 10)
	enqueueScrapeCronTask = func(task *NewSyncTask) error { tasks <- task; return nil }
	t.Cleanup(func() { <-StopScrapeCron().Done(); loadScrapeCronPaths = oldLoad; enqueueScrapeCronTask = oldEnqueue })
	return tasks
}

func TestScrapeCronToggleAndRetiredPlanAdmission(t *testing.T) {
	tasks := setupScrapeCronTest(t)
	enabled := true
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
		return []*models.ScrapePath{{BaseModel: models.BaseModel{ID: 7}, EnableCron: enabled, CronExpression: "0 0 1 1 *"}}, nil
	}
	if err := InitScrapeCronWithError(); err != nil {
		t.Fatal(err)
	}
	old := scrapePlan(t)
	entries := old.Entries()
	if len(entries) != 1 {
		t.Fatalf("enabled plan entries=%d", len(entries))
	}
	entries[0].Job.Run()
	select {
	case task := <-tasks:
		if task.ID != 7 || task.TaskType != SyncTaskTypeScrape {
			t.Fatalf("task=%+v", task)
		}
	default:
		t.Fatal("enabled plan did not enqueue")
	}
	enabled = false
	if err := InitScrapeCronWithError(); err != nil {
		t.Fatal(err)
	}
	if len(scrapePlan(t).Entries()) != 0 {
		t.Fatal("same expression disable left active plan")
	}
	entries[0].Job.Run()
	select {
	case <-tasks:
		t.Fatal("retired plan admitted a task")
	default:
	}
	enabled = true
	if err := InitScrapeCronWithError(); err != nil {
		t.Fatal(err)
	}
	if len(scrapePlan(t).Entries()) != 1 {
		t.Fatal("same expression enable did not restore plan")
	}
}

func TestScrapeCronUnchangedSchedulesKeepDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tasks := setupScrapeCronTest(t)
		paths := []*models.ScrapePath{{BaseModel: models.BaseModel{ID: 7}, EnableCron: true, CronExpression: "@every 1h", MaxThreads: 1}}
		loadScrapeCronPaths = func() ([]*models.ScrapePath, error) { return paths, nil }
		if err := InitScrapeCronWithError(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		deadline := time.Now().Add(time.Hour)
		time.Sleep(30 * time.Minute)
		paths[0].MaxThreads = 3
		paths = append(paths, &models.ScrapePath{BaseModel: models.BaseModel{ID: 8}, CronExpression: "@every 2h"})
		assertDeadline := func() {
			t.Helper()
			if err := InitScrapeCronWithError(); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if got := scrapePlan(t).Entries()[0].Next; !got.Equal(deadline) {
				t.Fatalf("unchanged hourly schedule moved to %s, want %s", got, deadline)
			}
		}
		assertDeadline()
		paths[1].EnableCron = true
		assertDeadline()
		paths[1].CronExpression = "@every 3h"
		assertDeadline()
		if got := scrapePlan(t).Entries()[1].Next; !got.Equal(time.Now().Add(3 * time.Hour)) {
			t.Fatalf("changed schedule deadline=%s, want=%s", got, time.Now().Add(3*time.Hour))
		}
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		select {
		case task := <-tasks:
			if task.ID != 7 || task.TaskType != SyncTaskTypeScrape {
				t.Fatalf("deadline admitted unexpected task %+v", task)
			}
		default:
			t.Fatal("hourly task was not admitted at its original deadline")
		}
	})
}

func TestScrapeCronLatePreparationCannotOverwriteDisable(t *testing.T) {
	setupScrapeCronTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return []*models.ScrapePath{{EnableCron: true, CronExpression: "0 0 1 1 *"}}, nil
		}
		return nil, nil
	}
	done := make(chan error, 1)
	go func() { done <- InitScrapeCronWithError() }()
	<-started
	if err := InitScrapeCronWithError(); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded candidate reported application success")
		}
	case <-time.After(time.Second):
		t.Fatal("old preparation did not finish")
	}
	if len(scrapePlan(t).Entries()) != 0 {
		t.Fatal("late old plan revived disabled tasks")
	}
}

func TestScrapeCronFailedPreparationKeepsValidPlan(t *testing.T) {
	setupScrapeCronTest(t)
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
		return []*models.ScrapePath{{EnableCron: true, CronExpression: "0 0 1 1 *"}}, nil
	}
	if err := InitScrapeCronWithError(); err != nil {
		t.Fatal(err)
	}
	before := scrapePlan(t)
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) { return nil, errors.New("database unavailable") }
	if err := InitScrapeCronWithError(); err == nil {
		t.Fatal("query error was swallowed")
	}
	if scrapePlan(t) != before {
		t.Fatal("query error discarded valid plan")
	}
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
		return []*models.ScrapePath{{EnableCron: true, CronExpression: "invalid"}}, nil
	}
	if err := InitScrapeCronWithError(); err == nil {
		t.Fatal("invalid cron was swallowed")
	}
	if scrapePlan(t) != before {
		t.Fatal("invalid cron discarded valid plan")
	}
}

func TestScrapeCronSupersededReloadWarnsWhenNewerPreparationFails(t *testing.T) {
	tasks := setupScrapeCronTest(t)
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
		return []*models.ScrapePath{{EnableCron: true, CronExpression: "0 0 1 1 *"}}, nil
	}
	if err := InitScrapeCronWithError(); err != nil {
		t.Fatal(err)
	}
	before := scrapePlan(t)
	prepared, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
		if calls.Add(1) == 1 {
			close(prepared)
			<-release
			return nil, nil
		}
		return nil, errors.New("newer preparation failed")
	}
	done := make(chan error, 1)
	go func() { done <- InitScrapeCronWithError() }()
	<-prepared
	if err := InitScrapeCronWithError(); err == nil {
		close(release)
		t.Fatal("newer preparation error was hidden")
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded disable reported success while the previous plan remained active")
		}
	case <-time.After(time.Second):
		t.Fatal("superseded preparation did not finish")
	}
	if scrapePlan(t) != before {
		t.Fatal("failed preparations replaced the valid plan")
	}
	before.Entries()[0].Job.Run()
	select {
	case <-tasks:
	default:
		t.Fatal("test did not demonstrate the previously applied plan remained active")
	}
}

func TestScrapeGlobalCronOnlyAdmitsEnabledPathsWithoutCustomExpression(t *testing.T) {
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := conn.AutoMigrate(&models.ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	oldDB, oldManager, oldLogger := db.Db, GlobalNewSyncQueueManager, helpers.AppLogger
	oldPause, oldResume, oldActive := models.PauseSyncQueuesFunc, models.ResumeSyncQueuesFunc, models.IsStrmSyncTaskActiveFunc
	db.Db = conn
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	GlobalNewSyncQueueManager = nil
	manager := InitNewSyncQueueManager()
	manager.GetQueueStatus(models.SourceTypeLocal)
	manager.PauseAll()
	t.Cleanup(func() {
		db.Db, GlobalNewSyncQueueManager, helpers.AppLogger = oldDB, oldManager, oldLogger
		models.PauseSyncQueuesFunc, models.ResumeSyncQueuesFunc, models.IsStrmSyncTaskActiveFunc = oldPause, oldResume, oldActive
	})
	cleanupTestSyncQueue(t, manager.getQueue(models.SourceTypeLocal))
	paths := []*models.ScrapePath{
		{SourceType: models.SourceTypeLocal, EnableCron: true},
		{SourceType: models.SourceTypeLocal, EnableCron: true, CronExpression: "0 0 1 1 *"},
		{SourceType: models.SourceTypeLocal, EnableCron: false},
	}
	for _, path := range paths {
		if err := conn.Create(path).Error; err != nil {
			t.Fatal(err)
		}
	}
	startScrapeCron()
	for index, path := range paths {
		expected := TaskStatusNone
		if index == 0 {
			expected = TaskStatusWaiting
		}
		if got := manager.CheckTaskStatus(path.ID, SyncTaskTypeScrape); got != expected {
			t.Fatalf("path %d admission=%d want=%d", path.ID, got, expected)
		}
	}
}
