package models

import (
	"errors"
	"testing"
	"testing/synctest"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/realtime"
)

func TestSyncUpdatePublishesBeforeConcurrentDelete(t *testing.T) {
	setupSyncLedgerTestDB(t)
	task := &Sync{Status: SyncStatusCompleted, Logger: helpers.AppLogger}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, "", 8)
	defer unsubscribe()
	synctest.Test(t, func(t *testing.T) {
		committed, resume := make(chan struct{}), make(chan struct{})
		if err := db.Db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("test:pause_committed_update", func(tx *gorm.DB) {
			if tx.Error == nil {
				close(committed)
				<-resume
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer db.Db.Callback().Update().Remove("test:pause_committed_update")
		updated, deleted := make(chan error, 1), make(chan error, 1)
		go func() { updated <- task.UpdateProgress(t.Context(), 2, 1, 0, 0) }()
		<-committed
		var stored Sync
		if err := db.Db.First(&stored, task.ID).Error; err != nil || stored.Total != 2 {
			t.Fatalf("update not committed: %+v %v", stored, err)
		}
		go func() { deleted <- DeleteSyncRecordById(task.ID) }()
		synctest.Wait()
		select {
		case err := <-deleted:
			t.Fatalf("delete passed unpublished update: %v", err)
		default:
		}
		close(resume)
		if err := <-updated; err != nil {
			t.Fatal(err)
		}
		if err := <-deleted; err != nil {
			t.Fatal(err)
		}
		first, second := <-events, <-events
		if first.Payload.Deleted || !second.Payload.Deleted {
			t.Fatalf("event order: %+v, %+v", first, second)
		}
		_, _, sequence, _, cancel := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, "", 8)
		defer cancel()
		if sequence != 0 {
			t.Fatalf("deleted sequence restored: %d", sequence)
		}
	})
}

func TestSyncDeletedRecordRejectsAllLateUpdates(t *testing.T) {
	tests := []struct {
		name   string
		update func(*Sync) error
	}{
		{"generation", func(s *Sync) error { return s.SaveGeneration(t.Context(), SyncStatusCompleted, "", false) }},
		{"ledger", func(s *Sync) error { return s.UpdateLedger(t.Context(), realtime.SyncLedgerRunning, nil, "") }},
		{"progress", func(s *Sync) error { return s.UpdateProgress(t.Context(), 2, 1, 0, 0) }},
		{"status", func(s *Sync) error { return s.UpdateStatus(t.Context(), SyncStatusCompleted) }},
		{"substatus", func(s *Sync) error { return s.UpdateSubStatus(t.Context(), SyncSubStatusProcessLocalFileList) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSyncLedgerTestDB(t)
			pending := realtime.SyncLedgerPending
			task := &Sync{Status: SyncStatusCompleted, LedgerStatus: &pending, Logger: helpers.AppLogger}
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatal(err)
			}
			events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, "", 8)
			defer unsubscribe()
			if err := DeleteSyncRecordById(task.ID); err != nil {
				t.Fatal(err)
			}
			if event := <-events; !event.Payload.Deleted {
				t.Fatalf("expected delete: %+v", event)
			}
			err := tt.update(task)
			if !errors.Is(err, errSyncRecordNotFound) && !errors.Is(err, ErrSyncLedgerRecordDeleted) {
				t.Fatalf("late update: %v", err)
			}
			select {
			case event := <-events:
				t.Fatalf("late event: %+v", event)
			default:
			}
		})
	}
}

func TestSyncCreateDoesNotPublishAfterTemporaryDeletion(t *testing.T) {
	setupSyncLedgerTestDB(t)
	var deletedID uint
	var deleteErr error
	if err := db.Db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register("test:delete_before_created_event", func(tx *gorm.DB) {
		if tx.Error != nil {
			return
		}
		task, ok := tx.Statement.Dest.(*Sync)
		if !ok {
			return
		}
		deletedID = task.ID
		deleteErr = DeleteTemporarySyncRecordById(task.ID)
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Create().Remove("test:delete_before_created_event")
	task := CreateSync(0, "/remote", "remote", "/local")
	if task == nil || task.ID != deletedID || deleteErr != nil {
		t.Fatalf("creation/deletion: %+v %d %v", task, deletedID, deleteErr)
	}
	_, _, sequence, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, "", 8)
	defer unsubscribe()
	if sequence != 0 {
		t.Fatalf("created event restored deleted sequence: %d", sequence)
	}
	var count int64
	if err := db.Db.Model(&Sync{}).Where("id = ?", task.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted record exists: %d %v", count, err)
	}
}
