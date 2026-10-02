package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
)

func TestRestorePartialFailureInvalidatesSyncPositions(t *testing.T) {
	conn := setupBackupTest(t)
	if err := conn.AutoMigrate(&models.SyncPath{}, &models.SyncFile{}); err != nil {
		t.Fatal(err)
	}
	sp := &models.SyncPath{SourceType: models.SourceType115, AccountId: 1, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/movies"}
	if err := conn.Create(sp).Error; err != nil {
		t.Fatal(err)
	}
	_, release, err := models.AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	file := models.SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: "/restored", LocalFilePath: filepath.Join(t.TempDir(), "restored.strm")}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SyncFile.json"), append(data, []byte("\ninvalid-json\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := restoreFromJsonFile(dir, "SyncFile", 1, &count, &models.SyncFile{}); err == nil {
		t.Fatal("expected partial restore failure")
	}
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{SourceType: "115", AccountID: 1, RemotePath: "/restored"})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, release, err = models.AcquireSyncPathScope(ctx, sp.ID)
	if release != nil {
		release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restore reused empty cached positions: %v", err)
	}
}
