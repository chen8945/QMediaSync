package models

import (
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/helpers"
)

func TestSyncLogRelativePath(t *testing.T) {
	helpers.GlobalConfig.Log.SyncLogDir = "logs/sync"

	got := SyncLogRelativePath(42)
	if got != "sync/sync_42.log" {
		t.Fatalf("SyncLogRelativePath = %s，期望 sync/sync_42.log", got)
	}
}

func TestSyncLogFullPathUsesConfiguredDirectory(t *testing.T) {
	configDir := t.TempDir()
	helpers.ConfigDir = configDir
	helpers.GlobalConfig.Log.SyncLogDir = "logs/sync"

	got := SyncLogFullPath(42)
	want := filepath.Join(configDir, "logs", "sync", "sync_42.log")
	if got != want {
		t.Fatalf("SyncLogFullPath = %s，期望 %s", got, want)
	}
}

func TestLegacySyncLogFullPath(t *testing.T) {
	configDir := t.TempDir()
	helpers.ConfigDir = configDir

	got := LegacySyncLogFullPath(42)
	want := filepath.Join(configDir, "logs", "libs", "sync_42.log")
	if got != want {
		t.Fatalf("LegacySyncLogFullPath = %s，期望 %s", got, want)
	}
}

func TestExistingSyncLogRelativePathUsesLegacyWhenOnlyLegacyExists(t *testing.T) {
	configDir := t.TempDir()
	helpers.ConfigDir = configDir
	helpers.GlobalConfig.Log.SyncLogDir = "logs/sync"

	legacyLogFile := LegacySyncLogFullPath(42)
	if err := os.MkdirAll(filepath.Dir(legacyLogFile), 0755); err != nil {
		t.Fatalf("创建 legacy 日志目录失败：%v", err)
	}
	if err := os.WriteFile(legacyLogFile, []byte("legacy log"), 0644); err != nil {
		t.Fatalf("写入 legacy 日志失败：%v", err)
	}

	got := ExistingSyncLogRelativePath(42)
	if got != "libs/sync_42.log" {
		t.Fatalf("ExistingSyncLogRelativePath = %s，期望 libs/sync_42.log", got)
	}
}

func TestSyncTaskEventPayloadUsesRealSyncID(t *testing.T) {
	helpers.GlobalConfig.Log.SyncLogDir = "logs/sync"

	sync := &Sync{
		ID: 42, CreatedAt: 100, UpdatedAt: 110,
		SyncPathId:        7,
		Status:            SyncStatusInProgress,
		SubStatus:         SyncSubStatusProcessNetFileList,
		Total:             8,
		NewStrm:           3,
		NewMeta:           2,
		NewUpload:         1,
		NetFileStartAt:    120,
		NetFileFinishAt:   140,
		LocalFileStartAt:  141,
		LocalFileFinishAt: 160,
		LocalPath:         "/media",
		RemotePath:        "/cloud",
	}

	payload := sync.SyncTaskEventPayload()
	if payload.SyncID != 42 {
		t.Fatalf("sync_id = %d，期望 42", payload.SyncID)
	}
	if payload.SyncPathID != 7 {
		t.Fatalf("sync_path_id = %d，期望 7", payload.SyncPathID)
	}
	if payload.LogPath != "sync/sync_42.log" {
		t.Fatalf("log_path = %s，期望 sync/sync_42.log", payload.LogPath)
	}
	if payload.NetFileStartAt != 120 {
		t.Fatalf("net_file_start_at = %d，期望 120", payload.NetFileStartAt)
	}
	if payload.NetFileFinishAt != 140 {
		t.Fatalf("net_file_finish_at = %d，期望 140", payload.NetFileFinishAt)
	}
	if payload.LocalFileStartAt != 141 {
		t.Fatalf("local_file_start_at = %d，期望 141", payload.LocalFileStartAt)
	}
	if payload.LocalFileFinishAt != 160 {
		t.Fatalf("local_file_finish_at = %d，期望 160", payload.LocalFileFinishAt)
	}
}
