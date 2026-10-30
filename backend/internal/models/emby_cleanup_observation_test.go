package models

import (
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	v115open "qmediasync/internal/v115/openapi"
)

func setupEmbyDirectoryObservation(t *testing.T) (EmbyIndexToken, SyncFile, EmbyDirectoryEvidenceCollector) {
	t.Helper()
	_, token, file := setupEmbySnapshotModelTest(t)
	file.Path, file.ParentId = "/movies/Show/Season 1", "season-root"
	file.LocalFilePath = "/strm/movies/Show/Season 1/episode.strm"
	file.FileName = "episode.mkv"
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	seedEmbyLocalRoot(t, file)
	return token, file, EmbyDirectoryEvidenceCollector{}
}

func seedEmbyLocalRoot(t *testing.T, file SyncFile) {
	t.Helper()
	if err := db.Db.AutoMigrate(&SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	var root SyncPath
	var account Account
	if err := db.Db.First(&root, file.SyncPathId).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&account, file.AccountId).Error; err != nil {
		t.Fatal(err)
	}
	evidence := SyncRootEvidence{SyncPathID: root.ID, SourceType: root.SourceType, AccountID: account.ID,
		AccountIdentity: EmbyAccountIdentity(account), RootFileID: root.BaseCid, RootPath: "/movies", ObservedAt: 123,
		Ancestors: []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: "/movies"}}}
	if err := SaveSyncRootEvidenceTx(db.Db, evidence, root.BaseCid); err != nil {
		t.Fatal(err)
	}
	directories := []SyncFile{
		{SourceType: file.SourceType, AccountId: file.AccountId, SyncPathId: file.SyncPathId, FileType: v115open.TypeDir, FileId: "show-root", ParentId: "root", Path: "/movies", FileName: "Show"},
		{SourceType: file.SourceType, AccountId: file.AccountId, SyncPathId: file.SyncPathId, FileType: v115open.TypeDir, FileId: "season-root", ParentId: "show-root", Path: "/movies/Show", FileName: "Season 1"},
	}
	if err := db.Db.Create(&directories).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEmbyDirectoryObservationPersistsPhysicalScopesAndSeasonPoster(t *testing.T) {
	token, file, collector := setupEmbyDirectoryObservation(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.Type, snapshot.Item.SeriesId, snapshot.Item.SeasonId, snapshot.Item.ParentIndexNumber = "Episode", "201", "202", 1
	poster := file
	poster.BaseModel = BaseModel{}
	poster.FileId, poster.FileName, poster.ParentId = "poster", "season01-poster.jpg", "show-root"
	poster.Path, poster.LocalFilePath = "/movies/Show", "/strm/movies/Show/season01-poster.jpg"
	poster.PickCode, poster.IsVideo, poster.IsMeta = "poster-code", false, true
	if err := db.Db.Create(&poster).Error; err != nil {
		t.Fatal(err)
	}
	seasonNumber := 1
	ancestors := []EmbyMediaDirectory{
		{ItemID: "201", MediaType: "Series", LocalPath: "/strm/movies/Show"},
		{ItemID: "202", MediaType: "Season", LocalPath: "/strm/movies/Show/Season 1", SeasonNumber: &seasonNumber},
		{ItemID: "999", MediaType: "Series", LocalPath: "/strm/movies"},
	}
	if err := collector.Enrich(t.Context(), &snapshot, ancestors); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.DirectoryScopes) != 2 {
		t.Fatalf("目录范围错误：%#v", snapshot.DirectoryScopes)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil || len(metadata.DirectoryScopes) != 2 || len(metadata.ScopedFiles) != 1 || metadata.ScopedFiles[0].File.FileID != "poster" {
		t.Fatalf("目录和季海报证据丢失：%#v，%v", metadata, err)
	}
	if len(metadata.ExclusiveFiles) != 0 {
		t.Fatal("剧根季海报不能伪装成视频同名旁车")
	}
	input, err := CaptureEmbyDeletionTx(db.Db, token.ServerID, "202", "Season")
	if err != nil || len(input.DirectoryScopes) != 1 || input.DirectoryScopes[0].MediaType != "Season" {
		t.Fatalf("整季收件应仅冻结季根：%#v，%v", input.DirectoryScopes, err)
	}
}

func TestEmbyDirectoryObservationDoesNotGrantFromNameOrChangedLedger(t *testing.T) {
	for _, name := range []string{"unknown_id", "sync_root", "sibling_path", "changed_ledger"} {
		t.Run(name, func(t *testing.T) {
			token, file, collector := setupEmbyDirectoryObservation(t)
			snapshot := snapshotForFile(file)
			snapshot.Item.Type, snapshot.Item.SeriesId = "Episode", "201"
			candidate := EmbyMediaDirectory{ItemID: "201", MediaType: "Series", LocalPath: "/strm/movies/Show"}
			switch name {
			case "unknown_id":
				candidate.ItemID = "999"
			case "sync_root":
				candidate.LocalPath = "/strm/movies"
			case "sibling_path":
				candidate.LocalPath = "/strm/movies/Other"
			}
			if err := collector.Enrich(t.Context(), &snapshot, []EmbyMediaDirectory{candidate}); err != nil {
				t.Fatal(err)
			}
			if name == "changed_ledger" {
				file.FileId, file.Sha1 = "replacement", "different"
				if err := db.Db.Save(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			var evidence EmbyItemEvidence
			db.Db.First(&evidence)
			metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil || len(metadata.DirectoryScopes) != 0 {
				t.Fatalf("不完整／变化证据获得目录授权：%#v，%v", metadata, err)
			}
		})
	}
}

func TestEmbyMovieDirectoryRequiresPositiveEvidence(t *testing.T) {
	_, file, collector := setupEmbyDirectoryObservation(t)
	if err := db.Db.AutoMigrate(&Media{}, &ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	if err := collector.Enrich(t.Context(), &snapshot, nil); err != nil || len(snapshot.DirectoryScopes) != 0 {
		t.Fatalf("孤立 Movie 不能从 dirname 获得作品根：%#v %v", snapshot.DirectoryScopes, err)
	}
	physical := EmbyMediaDirectory{ItemID: snapshot.Item.ItemId, MediaType: "Movie", LocalPath: "/strm/movies/Show"}
	if err := collector.Enrich(t.Context(), &snapshot, []EmbyMediaDirectory{physical}); err != nil || len(snapshot.DirectoryScopes) != 1 {
		t.Fatalf("Movie 物理父目录没有获得作品根：%#v %v", snapshot.DirectoryScopes, err)
	}
	scope := snapshot.DirectoryScopes[0]
	if scope.MediaType != "Movie" || scope.ItemID != "101" || scope.EvidenceKind != "emby_physical_ancestor" ||
		scope.LocalPath != physical.LocalPath || scope.Root.FileID != "show-root" || scope.Root.FileName != "Show" {
		t.Fatalf("Movie 物理父目录证据错误：%#v", scope)
	}
	unrelated := physical
	unrelated.ItemID = "999"
	if err := collector.Enrich(t.Context(), &snapshot, []EmbyMediaDirectory{unrelated}); err != nil || len(snapshot.DirectoryScopes) != 0 {
		t.Fatalf("其他条目的电影目录角色不能获得授权：%#v %v", snapshot.DirectoryScopes, err)
	}
	scrapePath := ScrapePath{SourceType: file.SourceType, AccountId: file.AccountId}
	if err := db.Db.Create(&scrapePath).Error; err != nil {
		t.Fatal(err)
	}
	output := Media{ScrapePathId: scrapePath.ID, MediaType: MediaTypeMovie, Status: MediaStatusRenamed,
		VideoFileId: file.FileId, VideoFileName: file.FileName, Path: file.Path, PathId: file.ParentId}
	if err := db.Db.Create(&output).Error; err != nil {
		t.Fatal(err)
	}
	if err := collector.Enrich(t.Context(), &snapshot, nil); err != nil || len(snapshot.DirectoryScopes) != 1 {
		t.Fatalf("明确 Movie 整理输出没有获得作品根：%#v %v", snapshot.DirectoryScopes, err)
	}
	if scope := snapshot.DirectoryScopes[0]; scope.EvidenceKind != "qms_scrape_output" || scope.LocalPath != filepath.Dir(file.LocalFilePath) {
		t.Fatalf("Movie 根证据错误：%#v", scope)
	}
	// 115 以稳定文件 ID 核对刮削输出，不能要求历史 PickCode 同时存在或相等。
	if err := db.Db.Model(&output).Update("video_pick_code", "historical-pick-code").Error; err != nil {
		t.Fatal(err)
	}
	if err := collector.Enrich(t.Context(), &snapshot, nil); err != nil || len(snapshot.DirectoryScopes) != 1 {
		t.Fatalf("115 稳定文件 ID 的整理证据被 PickCode 变化拒绝：%#v %v", snapshot.DirectoryScopes, err)
	}
	// 同一目录两种证据并存时按 EmbyDirectoryScopeKey 去重，物理祖先在先。
	both := EmbyMediaDirectory{ItemID: snapshot.Item.ItemId, MediaType: "Movie", LocalPath: filepath.Dir(file.LocalFilePath)}
	if err := collector.Enrich(t.Context(), &snapshot, []EmbyMediaDirectory{both}); err != nil || len(snapshot.DirectoryScopes) != 1 {
		t.Fatalf("同一目录两种证据没有去重：%#v %v", snapshot.DirectoryScopes, err)
	}
	if scope := snapshot.DirectoryScopes[0]; scope.EvidenceKind != "emby_physical_ancestor" {
		t.Fatalf("物理祖先证据应优先保留：%#v", scope)
	}
}

func TestEmbyMoviePhysicalDirectoryReachesDeletionCapture(t *testing.T) {
	token, file, collector := setupEmbyDirectoryObservation(t)
	if err := db.Db.AutoMigrate(&Media{}, &ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	physical := EmbyMediaDirectory{ItemID: "101", MediaType: "Movie", LocalPath: "/strm/movies/Show"}
	if err := collector.Enrich(t.Context(), &snapshot, []EmbyMediaDirectory{physical}); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.DirectoryScopes) != 1 {
		t.Fatalf("物理父目录范围缺失：%#v", snapshot.DirectoryScopes)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	input, err := CaptureEmbyDeletionTx(db.Db, token.ServerID, "101", "Movie")
	if err != nil || len(input.DirectoryScopes) != 1 || input.DirectoryScopes[0].MediaType != "Movie" || input.DirectoryScopes[0].ItemID != "101" {
		t.Fatalf("Movie 收件应冻结物理目录范围：%#v，%v", input.DirectoryScopes, err)
	}
}
