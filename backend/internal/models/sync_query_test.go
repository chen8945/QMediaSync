package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/realtime"
)

func TestSyncDisplayQueriesKeepDetailsOutOfSummary(t *testing.T) {
	setupSyncDeleteTestDB(t)
	failures := make([]realtime.SyncScanFailure, 10001)
	for i := range failures {
		failures[i] = realtime.SyncScanFailure{Kind: "file", Path: fmt.Sprintf("/%d.mkv", i), Reason: "write failed"}
	}
	failures[10000].Kind = "directory"
	task := &Sync{Status: SyncStatusIncomplete, ScanResult: &realtime.SyncScanResult{FailedFiles: 10000, Failures: failures, CleanupStatus: "partial"}}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	state, err := GetSyncTaskState(t.Context(), task.ID)
	if err != nil || state.ScanResult != nil || state.Status != task.Status {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	views, total, err := GetSyncRecordViews(t.Context(), 1, 50)
	if err != nil || total != 1 || len(views) != 1 || views[0].ScanResult == nil || views[0].ScanResult.FailedFiles != 10000 {
		t.Fatalf("views=%+v total=%d err=%v", views, total, err)
	}
	body, err := json.Marshal(views)
	if err != nil || strings.Contains(string(body), "failures") {
		t.Fatalf("summary contains detail: %s err=%v", body, err)
	}
	summary, err := GetSyncScanSummary(t.Context(), task.ID)
	if err != nil || summary == nil || summary.CleanupStatus != "partial" {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	page, count, recorded, err := GetSyncFailurePage(t.Context(), task.ID, 10000, 100)
	if err != nil || !recorded || count != 10001 || len(page) != 1 || page[0].Kind != "directory" {
		t.Fatalf("page=%+v count=%d recorded=%v err=%v", page, count, recorded, err)
	}
	page, _, _, err = GetSyncFailurePage(t.Context(), task.ID, 20000, 100)
	if err != nil || len(page) != 0 {
		t.Fatalf("past end: %v %v", page, err)
	}
	if err := db.Db.Model(&Sync{}).Where("id = ?", task.ID).Update("scan_result", "invalid JSON").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := GetSyncTaskState(t.Context(), task.ID); err != nil {
		t.Fatalf("basic information depends on JSON: %v", err)
	}
	if _, err := GetSyncScanSummary(t.Context(), task.ID); err == nil {
		t.Fatal("invalid result must report an error")
	}
}
