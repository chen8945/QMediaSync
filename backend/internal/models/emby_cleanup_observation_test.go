package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
)

var embyObservationTestAccountID atomic.Uint64

func setupEmbyDirectoryObservation(t *testing.T) (EmbyIndexToken, SyncFile, EmbyDirectoryEvidenceCollector) {
	t.Helper()
	_, token, file := setupEmbySnapshotModelTest(t)
	file.Path, file.ParentId = "/movies/Show/Season 1", "season-root"
	file.LocalFilePath = "/strm/movies/Show/Season 1/episode.strm"
	file.FileName = "episode.mkv"
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := freezeEmbyFile(db.Db, file, "source-101", file.LocalFilePath)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []EmbyDirectoryAncestor{
		{FileID: "0", Path: "/"}, {FileID: "root", ParentID: "0", Path: "/movies"},
		{FileID: "show-root", ParentID: "root", Path: "/movies/Show"},
		{FileID: "season-root", ParentID: "show-root", Path: file.Path},
	}
	key := embyDigest([]any{frozen.SourceType, frozen.AccountIdentity, frozen.AccountID, frozen.ParentID, frozen.Path})
	collector := EmbyDirectoryEvidenceCollector{chains: map[string]embyObservedDirectoryChain{key: {Nodes: nodes, At: time.Now()}}}
	return token, file, collector
}

type embyEvidenceBaiduReader struct {
	listings map[string][]*baidupan.FileInfo
	reads    int
	fail     bool
}

func (r *embyEvidenceBaiduReader) GetFileListWithOptions(_ context.Context, directory string, _ int, _, _, _ int32, _ baidupan.FileListOptions) ([]*baidupan.FileInfo, error) {
	r.reads++
	if r.fail {
		return nil, errors.New("unavailable")
	}
	return r.listings[directory], nil
}

func TestEmbyBaiduDirectoryEvidenceReusesCompleteParentListings(t *testing.T) {
	reader := &embyEvidenceBaiduReader{listings: map[string][]*baidupan.FileInfo{
		"/":   {{FsId: 11, Path: "/tv", ServerFilename: "tv", IsDir: 1}},
		"/tv": {{FsId: 12, Path: "/tv/Show", ServerFilename: "Show", IsDir: 1}},
		"/tv/Show": {
			{FsId: 13, Path: "/tv/Show/Season 1", ServerFilename: "Season 1", IsDir: 1},
			{FsId: 14, Path: "/tv/Show/Specials", ServerFilename: "Specials", IsDir: 1},
		},
	}}
	collector := EmbyDirectoryEvidenceCollector{chains: map[string]embyObservedDirectoryChain{}}
	file := EmbyFrozenFile{SourceType: SourceTypeBaiduPan, AccountID: 1, AccountIdentity: "account", Path: "/tv/Show/Season 1"}
	chain, err := collector.baiduDirectoryChain(t.Context(), file, reader)
	if err != nil || len(chain) != 4 || chain[3].FileID != "13" || reader.reads != 3 {
		t.Fatalf("首次百度真实目录链失败：%#v reads=%d %v", chain, reader.reads, err)
	}
	file.Path = "/tv/Show/Specials"
	chain, err = collector.baiduDirectoryChain(t.Context(), file, reader)
	if err != nil || len(chain) != 4 || chain[3].FileID != "14" || reader.reads != 3 {
		t.Fatalf("同父目录完整清单未复用：%#v reads=%d %v", chain, reader.reads, err)
	}
	collector.chains = map[string]embyObservedDirectoryChain{}
	collector.baiduFiles = nil
	reader.fail = true
	if _, err := collector.baiduDirectoryChain(t.Context(), file, reader); err == nil {
		t.Fatal("百度父链请求失败被当成空目录")
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

func TestEmbyMovieBaiduScrapeDirectoryRequiresSameFSID(t *testing.T) {
	for _, tt := range []struct {
		name           string
		scrapeFSID     string
		physicalParent bool
		wantKind       string
	}{
		{name: "matching_fsid", scrapeFSID: "2002", wantKind: "qms_scrape_output"},
		{name: "missing_fsid"},
		{name: "same_path_replacement", scrapeFSID: "1001"},
		{name: "matching_fsid_with_parent", scrapeFSID: "2002", physicalParent: true, wantKind: "emby_physical_ancestor"},
		{name: "missing_fsid_with_parent", physicalParent: true, wantKind: "emby_physical_ancestor"},
		{name: "same_path_replacement_with_parent", scrapeFSID: "1001", physicalParent: true, wantKind: "emby_physical_ancestor"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, token, file := setupEmbySnapshotModelTest(t)
			if err := db.Db.AutoMigrate(&Media{}, &ScrapePath{}); err != nil {
				t.Fatal(err)
			}
			embyProviderTestLogger(t)
			// 账号共享客户端保留构造时的 HTTP transport，避免不同用例复用。
			account := Account{BaseModel: BaseModel{ID: uint(900000 + embyObservationTestAccountID.Add(1))}, SourceType: SourceTypeBaiduPan, UserId: "baidu-movie-evidence"}
			if err := db.Db.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&SyncPath{}).Where("id = ?", file.SyncPathId).Updates(map[string]any{"source_type": SourceTypeBaiduPan, "account_id": account.ID, "base_cid": "/movies"}).Error; err != nil {
				t.Fatal(err)
			}
			file.SourceType, file.AccountId = SourceTypeBaiduPan, account.ID
			file.Path, file.ParentId, file.FileId = "/movies/Work", "/movies/Work", "/movies/Work/A.mkv"
			file.FileName, file.LocalFilePath, file.PickCode = "A.mkv", "/strm/movies/Work/A.strm", "2002"
			if err := db.Db.Save(&file).Error; err != nil {
				t.Fatal(err)
			}
			scrapePath := ScrapePath{SourceType: file.SourceType, AccountId: file.AccountId}
			if err := db.Db.Create(&scrapePath).Error; err != nil {
				t.Fatal(err)
			}
			output := Media{ScrapePathId: scrapePath.ID, MediaType: MediaTypeMovie, Status: MediaStatusRenamed,
				VideoFileId: file.FileId, VideoFileName: file.FileName, VideoPickCode: tt.scrapeFSID, Path: file.Path, PathId: file.ParentId}
			if err := db.Db.Create(&output).Error; err != nil {
				t.Fatal(err)
			}
			listings := map[string][]*baidupan.FileInfo{
				"/":       {{FsId: 11, Path: "/movies", ServerFilename: "movies", IsDir: 1}},
				"/movies": {{FsId: 33, Path: file.Path, ServerFilename: "Work", IsDir: 1}},
				file.Path: {{FsId: 2002, Path: file.FileId, ServerFilename: file.FileName, Size: uint64(file.FileSize), ServerMtime: uint64(file.MTime), Md5: file.Sha1}},
			}
			oldClient := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = oldClient })
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				entries, ok := listings[r.URL.Query().Get("dir")]
				if r.Method != http.MethodGet || r.URL.Query().Get("method") != "list" || !ok {
					t.Errorf("非预期的网盘请求：%s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries})
			})}
			snapshot := snapshotForFile(file)
			var ancestors []EmbyMediaDirectory
			if tt.physicalParent {
				ancestors = []EmbyMediaDirectory{{ItemID: snapshot.Item.ItemId, MediaType: "Movie", LocalPath: filepath.Dir(file.LocalFilePath)}}
			}
			var collector EmbyDirectoryEvidenceCollector
			if err := collector.Enrich(t.Context(), &snapshot, ancestors); err != nil {
				t.Fatal(err)
			}
			wantScopes := 0
			if tt.wantKind != "" {
				wantScopes = 1
			}
			if len(snapshot.DirectoryScopes) != wantScopes {
				t.Fatalf("刮削 fsid %q 与当前 fsid %q 的目录授权错误：%#v", tt.scrapeFSID, file.PickCode, snapshot.DirectoryScopes)
			}
			if wantScopes != 0 {
				scope := snapshot.DirectoryScopes[0]
				if scope.EvidenceKind != tt.wantKind || scope.Root.FileID != "33" || scope.Root.ParentID != "11" || scope.ItemID != snapshot.Item.ItemId {
					t.Fatalf("目录证据来源或真实身份错误：%#v", scope)
				}
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			input, err := CaptureEmbyDeletionTx(db.Db, token.ServerID, snapshot.Item.ItemId, "Movie")
			if err != nil || len(input.DirectoryScopes) != wantScopes {
				t.Fatalf("删除收件没有保留已核验目录范围：%#v %v", input.DirectoryScopes, err)
			}
			if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
				t.Fatalf("可选刮削证据影响了明确文件范围：%#v", input.Owners)
			}
			frozen := input.Owners[0].Files[0]
			if frozen.Reason != "" || frozen.PickCode != file.PickCode || frozen.FileID != file.FileId {
				t.Fatalf("明确文件身份未保留：%#v", frozen)
			}
		})
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

func TestEmby115ObservedDirectoryChainRejectsWrongAncestry(t *testing.T) {
	file := EmbyFrozenFile{SourceType: SourceType115, ParentID: "season-root", Path: "movies/Show/Season 1", RemoteRoot: "movies", RootFileID: "root"}
	base := v115open.FileDetail{FileId: "season-root", FileName: "Season 1", FileCategory: v115open.TypeDir, Path: "/movies/Show",
		Paths: []v115open.FileDetailPath{{FileId: "0"}, {FileId: "root", Name: "movies"}, {FileId: "show-root", Name: "Show"}}}
	for _, name := range []string{"valid", "changed_root", "duplicate_id", "wrong_path", "file_instead_of_directory"} {
		t.Run(name, func(t *testing.T) {
			detail := base
			detail.Paths = append([]v115open.FileDetailPath(nil), base.Paths...)
			switch name {
			case "changed_root":
				detail.Paths[1].FileId = "other"
			case "duplicate_id":
				detail.Paths[2].FileId = "root"
			case "wrong_path":
				detail.Path = "/movies/Other"
			case "file_instead_of_directory":
				detail.FileCategory = v115open.TypeFile
			}
			nodes, err := emby115ObservedDirectoryChain(file, &detail)
			if name == "valid" {
				if err != nil || len(nodes) != 4 || nodes[3].ParentID != "show-root" {
					t.Fatalf("有效父链被拒绝：%#v %v", nodes, err)
				}
			} else if err == nil {
				raw, _ := json.Marshal(nodes)
				t.Fatalf("错误父链未被拒绝：%s", raw)
			}
		})
	}
}

func TestEmbyBaiduDirectoryEvidenceProductionCollection(t *testing.T) {
	for _, mode := range []string{"original_member", "same_path_replacement", "changed_generation"} {
		t.Run(mode, func(t *testing.T) { testEmbyBaiduDirectoryEvidenceProductionCollection(t, mode) })
	}
}

func testEmbyBaiduDirectoryEvidenceProductionCollection(t *testing.T, mode string) {
	t.Helper()
	token, file, _ := setupEmbyDirectoryObservation(t)
	embyProviderTestLogger(t)
	// 账号共享客户端会保存构造时的 HTTP transport；每个用例使用独立账号。
	account := Account{BaseModel: BaseModel{ID: uint(900000 + embyObservationTestAccountID.Add(1))}, SourceType: SourceTypeBaiduPan, UserId: "baidu-evidence"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", file.SyncPathId).Updates(map[string]any{"source_type": SourceTypeBaiduPan, "account_id": account.ID, "base_cid": "/movies"}).Error; err != nil {
		t.Fatal(err)
	}
	file.AccountId = account.ID
	file.SourceType, file.ParentId, file.FileId = SourceTypeBaiduPan, file.Path, file.Path+"/"+file.FileName
	file.PickCode = "1001"
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	poster := file
	poster.BaseModel, poster.IsVideo, poster.IsMeta = BaseModel{}, false, true
	poster.FileName, poster.ParentId, poster.Path, poster.PickCode = "season01-poster.jpg", "/movies/Show", "/movies/Show", "1002"
	poster.FileId, poster.LocalFilePath = "/movies/Show/season01-poster.jpg", "/strm/movies/Show/season01-poster.jpg"
	if err := db.Db.Create(&poster).Error; err != nil {
		t.Fatal(err)
	}
	oldClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = oldClient })
	reads := 0
	member := &baidupan.FileInfo{FsId: 1001, Path: file.FileId, ServerFilename: file.FileName, Size: uint64(file.FileSize), ServerMtime: uint64(file.MTime), Md5: file.Sha1}
	if mode == "same_path_replacement" {
		// 原目录及视频已移走，同一路径的新目录只有另一个视频 fsid。
		member.FsId = 2001
	} else if mode == "changed_generation" {
		member.Md5 = "replacement-content"
	}
	listings := map[string][]*baidupan.FileInfo{
		"/":       {{FsId: 11, Path: "/movies", ServerFilename: "movies", IsDir: 1}},
		"/movies": {{FsId: 12, Path: "/movies/Show", ServerFilename: "Show", IsDir: 1}},
		"/movies/Show": {
			{FsId: 13, Path: "/movies/Show/Season 1", ServerFilename: "Season 1", IsDir: 1},
			{FsId: 1002, Path: poster.FileId, ServerFilename: poster.FileName, Size: uint64(poster.FileSize), ServerMtime: uint64(poster.MTime), Md5: poster.Sha1},
		},
		"/movies/Show/Season 1": {member},
	}
	http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
		reads++
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Query().Get("method") != "list" {
			t.Errorf("unexpected cloud request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		entries, ok := listings[r.URL.Query().Get("dir")]
		if !ok {
			t.Errorf("unexpected directory: %s", r.URL.Query().Get("dir"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries})
	})}
	snapshot := snapshotForFile(file)
	snapshot.Item.Type, snapshot.Item.SeriesId, snapshot.Item.SeasonId, snapshot.Item.ParentIndexNumber = "Episode", "201", "202", 1
	var collector EmbyDirectoryEvidenceCollector
	seasonNumber := 1
	ancestors := []EmbyMediaDirectory{
		{ItemID: "201", MediaType: "Series", LocalPath: "/strm/movies/Show"},
		{ItemID: "202", MediaType: "Season", LocalPath: "/strm/movies/Show/Season 1", SeasonNumber: &seasonNumber},
	}
	err := collector.Enrich(t.Context(), &snapshot, ancestors)
	if (err == nil) != (mode == "original_member") {
		t.Fatalf("member identity verification: %v", err)
	}
	if mode == "original_member" {
		if err := collector.Enrich(t.Context(), &snapshot, ancestors); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
	wantScopes := 0
	if mode == "original_member" {
		wantScopes = 2
	}
	if err != nil || len(metadata.DirectoryScopes) != wantScopes || reads != 4 {
		t.Fatalf("production Baidu evidence lost: scopes=%+v reads=%d err=%v", metadata.DirectoryScopes, reads, err)
	}
	if mode == "original_member" && (len(metadata.ScopedFiles) != 1 || metadata.ScopedFiles[0].File.FileID != poster.FileId) {
		t.Fatalf("real Baidu path-valued poster parent lost scoped evidence: %+v", metadata.ScopedFiles)
	}
	var frozen []EmbyFrozenFile
	if err := json.Unmarshal([]byte(evidence.FilesJSON), &frozen); err != nil || len(frozen) != 1 || frozen[0].PickCode != file.PickCode {
		t.Fatalf("optional directory failure erased original file evidence: %+v %v", frozen, err)
	}
	for _, scope := range metadata.DirectoryScopes {
		if scope.Root.RootFileID != "/movies" || scope.Root.FileID != "12" && scope.Root.FileID != "13" {
			t.Fatalf("directory root did not freeze actual fsid: %+v", scope.Root)
		}
	}
}
