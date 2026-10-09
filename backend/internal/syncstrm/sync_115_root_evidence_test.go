package syncstrm

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/v115open"
)

func newRootEvidenceTestSync(t *testing.T) *SyncStrm {
	t.Helper()
	account, root := setupStrmExclusionTestDB(t)
	root.RemotePath = "Library/Media"
	if err := db.Db.Save(root).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.AutoMigrate(&models.SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	s := newSyncStrm(account, root.ID, root.RemotePath, root.BaseCid, root.LocalPath, SyncStrmConfig{}, false, 0, false, true)
	t.Cleanup(func() { s.Cancel(); s.Sync.Logger.Close() })
	return s
}

func rootEvidenceTestParts() []v115open.FileDetailPath {
	return []v115open.FileDetailPath{{FileId: "0"}, {FileId: "library", Name: "Library"}, {FileId: "root", Name: "Media"}}
}

func readRootEvidenceTest(t *testing.T, s *SyncStrm) models.SyncRootEvidence {
	t.Helper()
	rows, err := models.LoadSyncRootEvidenceBatch(t.Context(), []uint{s.SyncPathId})
	if err != nil {
		t.Fatal(err)
	}
	row, ok := rows[s.SyncPathId]
	if !ok {
		t.Fatal("root observation was not persisted")
	}
	return row
}

func seedRootEvidenceTest(t *testing.T, s *SyncStrm, observedAt int64) models.SyncRootEvidence {
	t.Helper()
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", observedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.saveRootEvidence(); err != nil {
		t.Fatal(err)
	}
	s.rootEvidence = nil
	return readRootEvidenceTest(t, s)
}

func Test115RootEvidenceSurvivesEmptyScanAndBackgroundTransfer(t *testing.T) {
	s := newRootEvidenceTestSync(t)
	const observedAt = int64(100)
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", observedAt); err != nil {
		t.Fatal(err)
	}
	if got := readRootEvidenceTest(t, s); got.InvalidatedAt != observedAt {
		t.Fatalf("foreground observation became authorized: %+v", got)
	}
	// 重复响应不能把第一次观察时间续期；重建 Sync115 也不能清掉根链。
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", observedAt+50); err != nil {
		t.Fatal(err)
	}
	oldList := list115FilesPage
	t.Cleanup(func() { list115FilesPage = oldList })
	requests := 0
	list115FilesPage = func(context.Context, *v115open.OpenClient, string, bool, bool, bool, int, int) (*v115open.FileListResp, error) {
		requests++
		return &v115open.FileListResp{}, nil
	}
	s.sync115 = &Sync115{pathConflict: true}
	s.Start115Sync()
	select {
	case err := <-s.PathErrChan:
		t.Fatal(err)
	default:
	}
	if requests != 1 || s.sync115.pathConflict || s.rootEvidence == nil {
		t.Fatalf("empty scan requests=%d state=%+v evidence=%+v", requests, s.sync115, s.rootEvidence)
	}
	view := s.backgroundLedgerView(s.Sync)
	s.Cancel()
	view.Context = t.Context()
	if s.rootEvidence != nil || s.memSyncCache != nil {
		t.Fatal("foreground retained transferred observation or ledger")
	}
	if err := view.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	got := readRootEvidenceTest(t, view)
	want := []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "library", ParentID: "0", Path: "/Library"}, {FileID: "root", ParentID: "library", Path: "/Library/Media"}}
	if got.ObservedAt != observedAt || got.InvalidatedAt != 0 || !slices.Equal(got.Ancestors, want) {
		t.Fatalf("saved root evidence=%+v", got)
	}
	var files int64
	if err := db.Db.Model(&models.SyncFile{}).Count(&files).Error; err != nil || files != 0 {
		t.Fatalf("root ancestors entered managed ledger: count=%d err=%v", files, err)
	}
}

func Test115RootEvidenceFailurePreservesPublishedChain(t *testing.T) {
	for _, mode := range []string{"incomplete", "conflicting_paths", "cancelled", "ledger_write", "root_write"} {
		t.Run(mode, func(t *testing.T) {
			s := newRootEvidenceTestSync(t)
			old := seedRootEvidenceTest(t, s, 100)
			if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", 200); err != nil {
				t.Fatal(err)
			}
			wantErr := errors.New("injected persistence failure")
			switch mode {
			case "incomplete":
				s.recordScanFailure(s.SourcePath, wantErr)
			case "conflicting_paths":
				s.sync115 = &Sync115{pathConflict: true}
			case "cancelled":
				s.Cancel()
			case "ledger_write", "root_write":
				if err := s.memSyncCache.Insert(&SyncFileCache{SourceType: models.SourceType115, FileId: "video", ParentId: "root", Path: s.SourcePath, FileName: "movie.mkv", FileType: v115open.TypeFile}); err != nil {
					t.Fatal(err)
				}
				table := "sync_files"
				if mode == "root_write" {
					table = "sync_root_evidences"
				}
				if err := db.Db.Callback().Create().Before("gorm:create").Register("root_evidence:fail", func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						tx.AddError(wantErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Create().Remove("root_evidence:fail") })
			}
			err := s.handleTempTableDiff()
			if mode == "cancelled" && !errors.Is(err, context.Canceled) ||
				(mode == "ledger_write" || mode == "root_write") && !errors.Is(err, wantErr) ||
				(mode == "incomplete" || mode == "conflicting_paths") && err != nil {
				t.Fatalf("unexpected completion error: %v", err)
			}
			if got := readRootEvidenceTest(t, s); !reflect.DeepEqual(got, old) {
				t.Fatalf("failed scan changed published chain: got=%+v old=%+v", got, old)
			}
		})
	}
}

func Test115RootEvidenceKnownChangesInvalidateBeforeCompletion(t *testing.T) {
	for _, mode := range []string{"ancestor_id", "root_replaced", "root_moved"} {
		t.Run(mode, func(t *testing.T) {
			s := newRootEvidenceTestSync(t)
			old := seedRootEvidenceTest(t, s, 100)
			parts := rootEvidenceTestParts()
			switch mode {
			case "ancestor_id":
				parts[1].FileId = "new-library"
			case "root_replaced":
				parts[2].FileId = "new-root"
			case "root_moved":
				parts[1].Name = "Moved"
			}
			err := s.observe115RootPaths(s.Context, parts, "root", 200)
			if mode == "ancestor_id" && err != nil || mode != "ancestor_id" && err == nil {
				t.Fatalf("unexpected changed-root result: %v", err)
			}
			got := readRootEvidenceTest(t, s)
			if got.InvalidatedAt != 200 || got.ObservedAt != old.ObservedAt || !slices.Equal(got.Ancestors, old.Ancestors) {
				t.Fatalf("known change did not preserve and invalidate old chain: %+v", got)
			}
			s.recordScanFailure(s.SourcePath, errors.New("later scan failure"))
			if err := s.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			if next := readRootEvidenceTest(t, s); !reflect.DeepEqual(next, got) {
				t.Fatalf("failed scan revived invalidated chain: %+v", next)
			}
		})
	}
}

func Test115RootEvidenceConflictingResponsesCannotPublish(t *testing.T) {
	s := newRootEvidenceTestSync(t)
	seedRootEvidenceTest(t, s, 100)
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", 200); err != nil {
		t.Fatal(err)
	}
	changed := rootEvidenceTestParts()
	changed[1].FileId = "replacement"
	if err := s.observe115RootPaths(s.Context, changed, "root", 201); err == nil {
		t.Fatal("contradictory root observations accepted")
	}
	view := s.backgroundLedgerView(s.Sync)
	view.Context = t.Context()
	if err := view.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	if got := readRootEvidenceTest(t, view); got.InvalidatedAt != 201 || got.ObservedAt != 100 {
		t.Fatalf("conflicting evidence was published: %+v", got)
	}
}

func Test115RootEvidenceScopeRefreshesExpectedRootID(t *testing.T) {
	s := newRootEvidenceTestSync(t)
	if err := db.Db.Model(&models.SyncPath{}).Where("id = ?", s.SyncPathId).Update("base_cid", "previous-scan-root").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.acquireScope(); err != nil {
		t.Fatal(err)
	}
	defer s.releaseScope()
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", 100); err != nil {
		t.Fatal(err)
	}
	s.SourcePathId = "root"
	if err := s.saveRootEvidence(); err != nil {
		t.Fatal(err)
	}
	var root models.SyncPath
	if err := db.Db.First(&root, s.SyncPathId).Error; err != nil || root.BaseCid != "root" {
		t.Fatalf("new root ID not saved against post-wait configuration: %+v err=%v", root, err)
	}
}

func Test115RootEvidenceBackgroundFailureKeepsMarker(t *testing.T) {
	s := newRootEvidenceTestSync(t)
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.completeSync(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Query().Before("gorm:query").Register("root_evidence:ledger_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			tx.AddError(errors.New("ledger unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("root_evidence:ledger_failure") })
	view := s.backgroundLedgerView(s.Sync)
	view.Context = t.Context()
	view.runBackgroundLedger()
	got := readRootEvidenceTest(t, view)
	record := readBackgroundTestSync(t, s.Sync.ID)
	if got.InvalidatedAt != 100 || record.LedgerStatus == nil || *record.LedgerStatus != realtime.SyncLedgerFailed {
		t.Fatalf("failed background became usable: evidence=%+v status=%v", got, record.LedgerStatus)
	}
}

func Test115RootEvidenceCancelledResponseDoesNotTeach(t *testing.T) {
	s := newRootEvidenceTestSync(t)
	s.Cancel()
	if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled observation result=%v", err)
	}
	rows, err := models.LoadSyncRootEvidenceBatch(t.Context(), []uint{s.SyncPathId})
	if err != nil || len(rows) != 0 || s.rootEvidence != nil {
		t.Fatalf("cancelled response was persisted: rows=%+v err=%v", rows, err)
	}
}

func Test115RootEvidenceIgnoresPartialGeneration(t *testing.T) {
	for _, mode := range []string{"temporary", "single_file", "generation_service"} {
		t.Run(mode, func(t *testing.T) {
			s := newRootEvidenceTestSync(t)
			switch mode {
			case "temporary":
				s.TmpSyncPath = true
			case "single_file":
				s.IsFile = true
			case "generation_service":
				s.Sync.ID = 0
			}
			if err := s.observe115RootPaths(s.Context, rootEvidenceTestParts(), "root", 100); err != nil {
				t.Fatal(err)
			}
			rows, err := models.LoadSyncRootEvidenceBatch(t.Context(), []uint{s.SyncPathId})
			if err != nil || len(rows) != 0 || s.rootEvidence != nil {
				t.Fatalf("partial generation published root: rows=%+v err=%v", rows, err)
			}
		})
	}
}
