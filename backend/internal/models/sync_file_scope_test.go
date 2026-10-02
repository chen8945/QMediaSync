package models

import (
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
)

func TestSyncFileScopeCoveredRefinedIdentity(t *testing.T) {
	sp := setupSyncScopeTest(t)
	files := []SyncFile{
		{SyncPathId: sp.ID, AccountId: sp.AccountId, SourceType: sp.SourceType,
			FileId: "inside", Path: sp.RemotePath, LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "inside.strm")},
		{SyncPathId: sp.ID, AccountId: sp.AccountId, SourceType: sp.SourceType,
			FileId: "outside", Path: "/old", LocalFilePath: filepath.Join(t.TempDir(), "outside.strm")},
	}
	if err := db.Db.Create(&files).Error; err != nil {
		t.Fatal(err)
	}
	held, release, err := AcquireSyncFileScope(t.Context(), sp.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, tt := range []struct {
		name string
		want bool
	}{{"inside", true}, {"outside", false}, {"new", true}} {
		t.Run(tt.name, func(t *testing.T) {
			covered, err := SyncFileScopeCovered(t.Context(), held, tt.name, "")
			if err != nil || covered != tt.want {
				t.Fatalf("covered=%v want=%v err=%v", covered, tt.want, err)
			}
		})
	}
}
