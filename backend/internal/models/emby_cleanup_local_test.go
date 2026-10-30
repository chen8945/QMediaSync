package models

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	v115open "qmediasync/internal/v115/openapi"
)

func localEpisodeSnapshot(file SyncFile) (EmbyItemSnapshot, []EmbyMediaDirectory) {
	snapshot := snapshotForFile(file)
	snapshot.Item.Type, snapshot.Item.SeriesId, snapshot.Item.SeasonId, snapshot.Item.ParentIndexNumber = "Episode", "201", "202", 1
	season := 1
	return snapshot, []EmbyMediaDirectory{
		{ItemID: "201", MediaType: "Series", LocalPath: "/strm/movies/Show"},
		{ItemID: "202", MediaType: "Season", LocalPath: "/strm/movies/Show/Season 1", SeasonNumber: &season},
	}
}

func forbidEmbyObservationHTTP(t *testing.T) *int {
	t.Helper()
	reads := 0
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
		reads++
		t.Errorf("普通 Emby 观察不得访问网盘：%s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected cloud access", http.StatusBadRequest)
	})}
	return &reads
}

func TestEmbyLocalDirectoryColdEvidenceRepeatsWithoutHTTP(t *testing.T) {
	token, file, _ := setupEmbyDirectoryObservation(t)
	reads := forbidEmbyObservationHTTP(t)
	snapshot, roles := localEpisodeSnapshot(file)
	for range 2 {
		collector := EmbyDirectoryEvidenceCollector{}
		if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.DirectoryScopes) != 2 {
			t.Fatalf("冷库目录证据缺失：%+v", snapshot.DirectoryScopes)
		}
		if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	if *reads != 0 {
		t.Fatalf("网盘请求=%d", *reads)
	}
	var root SyncRootEvidence
	if err := db.Db.First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.ObservedAt != 123 {
		t.Fatalf("本地命中伪造了观察时间：%d", root.ObservedAt)
	}
}

func TestEmbyLocalDirectoryUsesOnlyMatchingCurrentEvidence(t *testing.T) {
	for _, scenario := range []string{"current", "sync_scope_changed", "connection_changed", "removed_index", "generation_changed", "file_replaced", "root_invalidated", "conflicting_directory"} {
		t.Run(scenario, func(t *testing.T) {
			token, file, collector := setupEmbyDirectoryObservation(t)
			snapshot, roles := localEpisodeSnapshot(file)
			if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			if scenario != "root_invalidated" {
				if err := db.Db.Where("sync_path_id = ?", file.SyncPathId).Delete(&SyncRootEvidence{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "sync_scope_changed", "connection_changed":
				var config EmbyConfig
				if err := db.Db.First(&config).Error; err != nil {
					t.Fatal(err)
				}
				if scenario == "sync_scope_changed" {
					config.SyncAllLibraries, config.SelectedLibraries = 0, `["lib"]`
				} else {
					config.EmbyApiKey = "changed-key"
				}
				if err := db.Db.Save(&config).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := BeginEmbyIndexRead("server-a", &config); err != nil {
					t.Fatal(err)
				}
			case "removed_index":
				if err := db.Db.Where("item_id = ?", "101").Delete(&EmbyMediaItem{}).Error; err != nil {
					t.Fatal(err)
				}
			case "generation_changed":
				if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", "101").Update("generation", 77).Error; err != nil {
					t.Fatal(err)
				}
			case "file_replaced":
				if err := db.Db.Model(&file).Update("sha1", "replacement").Error; err != nil {
					t.Fatal(err)
				}
			case "root_invalidated":
				if err := db.Db.Model(&SyncRootEvidence{}).Where("sync_path_id = ?", file.SyncPathId).Update("invalidated_at", 124).Error; err != nil {
					t.Fatal(err)
				}
			case "conflicting_directory":
				if err := db.Db.Model(&SyncFile{}).Where("file_id = ?", "show-root").Update("file_id", "replacement-show").Error; err != nil {
					t.Fatal(err)
				}
			}
			collector = EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "current" || scenario == "sync_scope_changed" {
				want = 2
			}
			if len(snapshot.DirectoryScopes) != want {
				t.Fatalf("%s 目录=%+v", scenario, snapshot.DirectoryScopes)
			}
			var count int64
			if err := db.Db.Model(&SyncRootEvidence{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if scenario != "root_invalidated" && count != 0 {
				t.Fatal("历史快照回退不得伪造新的根链观察")
			}
		})
	}
}

func TestEmbyLocalDirectoryTransactionRejectsPreparedDirectoryChanges(t *testing.T) {
	for _, change := range []string{"directory_id", "directory_parent", "directory_moved", "root_invalidated", "root_ancestors"} {
		t.Run(change, func(t *testing.T) {
			token, file, collector := setupEmbyDirectoryObservation(t)
			snapshot, roles := localEpisodeSnapshot(file)
			if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "directory_id":
				if err := db.Db.Model(&SyncFile{}).Where("file_id = ?", "show-root").Update("file_id", "new-show").Error; err != nil {
					t.Fatal(err)
				}
			case "directory_parent":
				if err := db.Db.Model(&SyncFile{}).Where("file_id = ?", "show-root").Update("parent_id", "new-parent").Error; err != nil {
					t.Fatal(err)
				}
			case "directory_moved":
				if err := db.Db.Model(&SyncFile{}).Where("file_id = ?", "show-root").Updates(map[string]any{"path": "/movies/moved", "parent_id": "new-parent"}).Error; err != nil {
					t.Fatal(err)
				}
			case "root_invalidated":
				if err := db.Db.Model(&SyncRootEvidence{}).Where("sync_path_id = ?", file.SyncPathId).Update("invalidated_at", 124).Error; err != nil {
					t.Fatal(err)
				}
			case "root_ancestors":
				if err := db.Db.Model(&SyncRootEvidence{}).Where("sync_path_id = ?", file.SyncPathId).Update("root_file_id", "new-root").Error; err != nil {
					t.Fatal(err)
				}
			}
			err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot})
			if change == "root_ancestors" {
				if err == nil {
					t.Fatal("损坏根链应返回错误")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var evidence EmbyItemEvidence
			if err := db.Db.First(&evidence).Error; err != nil {
				t.Fatal(err)
			}
			metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil || len(metadata.DirectoryScopes) != 0 {
				t.Fatalf("旧准备目录获得授权：%+v %v", metadata, err)
			}
		})
	}
}

func TestEmbyLocalDirectoryReadFailurePreservesEvidence(t *testing.T) {
	token, file, collector := setupEmbyDirectoryObservation(t)
	snapshot, roles := localEpisodeSnapshot(file)
	if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var before EmbyItemState
	if err := db.Db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected local read failure")
	const callback = "test:emby-local-evidence-failure"
	if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_root_evidences" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
	collector = EmbyDirectoryEvidenceCollector{}
	if err := collector.Enrich(t.Context(), &snapshot, roles); !errors.Is(err, injected) {
		t.Fatalf("查询错误被吞：%v", err)
	}

	// 失败观察的调用方不得提交；单独验证已有非空准备范围在事务读取失败时回滚。
	if err := db.Db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	collector = EmbyDirectoryEvidenceCollector{}
	if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_root_evidences" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, injected) {
		t.Fatalf("事务未返回读取错误：%v", err)
	}
	var after EmbyItemState
	if err := db.Db.First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before.SnapshotID != after.SnapshotID {
		t.Fatal("读取失败替换了旧快照")
	}
}

func TestEmbySeasonPosterQueryUsesExactDirectory(t *testing.T) {
	token, file, collector := setupEmbyDirectoryObservation(t)
	snapshot, roles := localEpisodeSnapshot(file)
	for i := range 101 {
		poster := file
		poster.BaseModel = BaseModel{}
		poster.FileId, poster.PickCode, poster.FileName = fmt.Sprint("poster-", i), fmt.Sprint("poster-", i), "season01-poster.jpg"
		poster.IsVideo, poster.IsMeta, poster.ParentId = false, true, "show-root"
		poster.Path = fmt.Sprintf("/movies/Other-%d", i)
		if i == 0 {
			poster.Path = "/movies/Show"
		}
		poster.LocalFilePath = filepath.Join("/strm", poster.Path, poster.FileName)
		if err := db.Db.Create(&poster).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
		t.Fatal(err)
	}
	const callback = "test:scoped-poster-query"
	queries, returned := 0, int64(0)
	if err := db.Db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if tx.Statement.Table == "sync_files" && strings.Contains(sql, "file_name =") {
			for _, arg := range tx.Statement.Vars {
				if arg == "season01-poster.jpg" {
					queries++
					returned += tx.RowsAffected
					if !strings.Contains(sql, "path IN") {
						t.Errorf("海报查询缺少精确目录条件：%s", sql)
					}
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if queries != 1 || returned != 1 {
		t.Fatalf("查询=%d 返回=%d，期望仅目标目录一张", queries, returned)
	}
	for i := range snapshot.DirectoryScopes {
		snapshot.DirectoryScopes[i].Root.Path = strings.TrimPrefix(snapshot.DirectoryScopes[i].Root.Path, "/")
	}
	scoped, err := freezeEmbyScopedMetadata(db.Db, snapshot, snapshot.DirectoryScopes)
	if err != nil || len(scoped) != 1 || queries != 2 || returned != 2 {
		t.Fatalf("根相对路径的海报查询不一致：files=%+v queries=%d returned=%d err=%v", scoped, queries, returned, err)
	}
}

func TestEmbyLocalDirectoryKeepsRolesWithoutFileAndUsesExplicitMapping(t *testing.T) {
	for _, mode := range []string{"unmapped", "mapped", "mapped_unknown_child", "mapped_windows", "mapped_unc"} {
		t.Run(mode, func(t *testing.T) {
			mapped := mode != "unmapped"
			_, file, _ := setupEmbyDirectoryObservation(t)
			setupEmbySourceSettings(t, "https://qms.test")
			var config EmbyConfig
			if err := db.Db.First(&config).Error; err != nil {
				t.Fatal(err)
			}
			mapping := EmbyPathMapping{EmbyRoot: "/external", LocalRoot: "/mounted"}
			if mode == "mapped_windows" {
				mapping.EmbyRoot = `D:\External`
			} else if mode == "mapped_unc" {
				mapping.EmbyRoot = `\\server\media\External`
			}
			if mapped {
				mapping.SyncPathID = file.SyncPathId
			}
			config.PathMappings = []EmbyPathMapping{mapping}
			if err := db.Db.Model(&config).Update("path_mappings_json", embyJSON(config.PathMappings)).Error; err != nil {
				t.Fatal(err)
			}
			token, err := BeginEmbyIndexRead("server-a", &config)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, roles := localEpisodeSnapshot(file)
			snapshot.Item.Path = "/external/Show/Season 1/external.strm"
			snapshot.Sources = []EmbySnapshotSource{{ID: "outside", ItemID: "101", Path: "https://qms.test/115/url/video.mkv?userid=owner&pickcode=untracked", PickCode: "untracked"}}
			for i := range roles {
				roles[i].LocalPath = strings.Replace(roles[i].LocalPath, "/strm/movies", "/external", 1)
			}
			if mode == "mapped_windows" || mode == "mapped_unc" {
				snapshot.Item.Path = mapping.EmbyRoot + `\Show\Season 1\external.strm`
				roles[0].LocalPath = mapping.EmbyRoot + `\Show`
				roles[1].LocalPath = mapping.EmbyRoot + `\Show\Season 1`
			}
			if mode == "mapped_unknown_child" {
				snapshot.Item.Path = "/external/Show/Unknown/external.strm"
				roles[1].LocalPath = "/external/Show/Unknown"
			}
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.MediaDirectories) != 2 || len(snapshot.cleanupFiles) != 0 {
				t.Fatalf("无文件时角色丢失或伪造视频：%+v", snapshot)
			}
			want := 0
			if mapped {
				want = 2
			}
			if mode == "mapped_unknown_child" {
				want = 1
			}
			if len(snapshot.DirectoryScopes) != want {
				t.Fatalf("显式映射=%v，scope=%+v", mapped, snapshot.DirectoryScopes)
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			var state EmbyItemState
			var evidence EmbyItemEvidence
			if err := db.Db.First(&state).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.First(&evidence, state.SnapshotID).Error; err != nil {
				t.Fatal(err)
			}
			metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil || len(metadata.DirectoryScopes) != want || len(metadata.MediaDirectories) != 2 {
				t.Fatalf("持久化目录能力丢失：%+v %v", metadata, err)
			}
		})
	}
}

func TestEmbyLocalDirectorySameCodeDifferentAccountKeepsUnboundScope(t *testing.T) {
	_, file, _ := setupEmbyDirectoryObservation(t)
	setupEmbySourceSettings(t, "https://qms.test")
	originalAccount, originalRoot := file.AccountId, file.SyncPathId
	other := Account{SourceType: SourceType115, UserId: "other"}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherRoot := SyncPath{SourceType: SourceType115, AccountId: other.ID, LocalPath: "/other", RemotePath: "/movies", BaseCid: "other-root"}
	if err := db.Db.Create(&otherRoot).Error; err != nil {
		t.Fatal(err)
	}
	file.AccountId, file.SyncPathId = other.ID, otherRoot.ID
	file.LocalFilePath = filepath.Join(otherRoot.GetFullLocalPath(), "Show", "Season 1", "episode.strm")
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	var config EmbyConfig
	if err := db.Db.First(&config).Error; err != nil {
		t.Fatal(err)
	}
	config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/external", LocalRoot: "/mounted", SyncPathID: originalRoot}}
	if err := db.Db.Model(&config).Update("path_mappings_json", embyJSON(config.PathMappings)).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", &config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, roles := localEpisodeSnapshot(file)
	snapshot.Item.Path = "/external/Show/Season 1/episode.strm"
	snapshot.Sources = []EmbySnapshotSource{
		{ID: "bound-other", ItemID: "101", Path: "https://qms.test/115/newurl?userid=other&pickcode=p1", PickCode: "p1"},
		{ID: "unbound-owner", ItemID: "101", Path: "https://qms.test/115/newurl?userid=owner&pickcode=p1", PickCode: "p1"},
	}
	for i := range roles {
		roles[i].LocalPath = strings.Replace(roles[i].LocalPath, "/strm/movies", "/external", 1)
	}
	collector := EmbyDirectoryEvidenceCollector{}
	if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.DirectoryScopes) != 2 {
		t.Fatalf("unbound account lost its independently mapped directories: %+v", snapshot.DirectoryScopes)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	evidence, sources, files := readSnapshotSources(t, "101")
	if len(files) != 1 || files[0].AccountID != other.ID || len(sources) != 2 || sources[1].SyncFileID != 0 || sources[1].AccountID != originalAccount {
		t.Fatalf("mixed account bindings changed: sources=%+v files=%+v", sources, files)
	}
	metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil || len(metadata.DirectoryScopes) != 2 {
		t.Fatalf("mapped directory evidence lost: %+v %v", metadata, err)
	}
	for _, scope := range metadata.DirectoryScopes {
		if scope.Root.AccountID != originalAccount || scope.Root.SourceID != "unbound-owner" {
			t.Fatalf("directory adopted the other account's source: %+v", scope)
		}
	}
}

func TestEmbyManagedMediaDirectoryHonorsExplicitMappingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, embyRoot, directory, want string
		syncPathID                      uint
	}{
		{name: "Windows", embyRoot: `D:\External`, directory: `d:\external\Show`, want: "/strm/movies/Show", syncPathID: 1},
		{name: "UNC", embyRoot: `\\server\share\External`, directory: `\\SERVER\share\External\Show`, want: "/strm/movies/Show", syncPathID: 1},
		{name: "sibling prefix", embyRoot: `D:\External`, directory: `D:\External-other\Show`, syncPathID: 1},
		{name: "traversal", embyRoot: `D:\External`, directory: `D:\External\..\Show`, syncPathID: 1},
		{name: "same text mount only", embyRoot: "/strm/movies", directory: "/strm/movies/Show"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := EmbyFrozenFile{SyncPathID: 1, LocalRoot: "/strm/movies", LocalFilePath: "/strm/movies/Show/video.strm",
				Reference: EmbySTRMReference{EmbyPath: "/strm/movies/Show/video.strm", EmbyRoot: tc.embyRoot, LocalRoot: "/mounted", SyncPathID: tc.syncPathID}}
			got, valid := embyManagedMediaDirectory(file, tc.directory)
			if valid != (tc.want != "") || got != tc.want {
				t.Fatalf("directory = %q, %v; want %q", got, valid, tc.want)
			}
		})
	}
}

func TestEmbyLocalBaiduDirectoryReusesHistoricalFSIDsWithoutHTTP(t *testing.T) {
	for _, scenario := range []string{"same_file", "file_replaced", "content_changed", "directory_replaced", "directory_moved_after_prepare", "no_history"} {
		t.Run(scenario, func(t *testing.T) {
			token, file, _ := setupEmbyDirectoryObservation(t)
			var account Account
			if err := db.Db.First(&account, file.AccountId).Error; err != nil {
				t.Fatal(err)
			}
			account.SourceType = SourceTypeBaiduPan
			if err := db.Db.Save(&account).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&SyncPath{}).Where("id = ?", file.SyncPathId).Updates(map[string]any{"source_type": SourceTypeBaiduPan, "base_cid": "/movies"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Where("sync_path_id = ?", file.SyncPathId).Delete(&SyncRootEvidence{}).Error; err != nil {
				t.Fatal(err)
			}
			file.SourceType, file.FileId, file.ParentId, file.PickCode = SourceTypeBaiduPan, file.Path+"/"+file.FileName, file.Path, "1001"
			if err := db.Db.Save(&file).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, roles := localEpisodeSnapshot(file)
			frozen, err := resolveEmbySnapshotFiles(db.Db, snapshot)
			if err != nil || len(frozen) != 1 {
				t.Fatalf("fixture files=%+v err=%v", frozen, err)
			}
			if scenario != "no_history" {
				// 旧版本已经记录了真实 fsid 父链；升级不能把它换成路径 ID 或重新查询。
				chain := []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "11", ParentID: "0", Path: "/movies"}, {FileID: "12", ParentID: "11", Path: "/movies/Show"}, {FileID: "13", ParentID: "12", Path: file.Path}}
				for i, role := range roles {
					nodeIndex := i + 2
					node := chain[nodeIndex]
					root := frozen[0]
					root.SyncFileID, root.FileID, root.ParentID, root.FileName, root.Path, root.LocalFilePath = 0, node.FileID, node.ParentID, filepath.Base(node.Path), filepath.Dir(node.Path), role.LocalPath
					snapshot.DirectoryScopes = append(snapshot.DirectoryScopes, EmbyDirectoryScope{Root: root, ItemID: role.ItemID, MediaType: role.MediaType, LocalPath: role.LocalPath, SeasonNumber: role.SeasonNumber, EvidenceKind: "emby_physical_ancestor", Ancestors: chain[:nodeIndex]})
				}
				snapshot.cleanupFiles = frozen
				if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "file_replaced":
				file.PickCode = "replacement-fsid"
				if err := db.Db.Save(&file).Error; err != nil {
					t.Fatal(err)
				}
				snapshot = snapshotForFile(file)
				snapshot.Item.Type, snapshot.Item.SeriesId, snapshot.Item.SeasonId = "Episode", "201", "202"
			case "content_changed":
				if err := db.Db.Model(&file).Update("sha1", "replacement").Error; err != nil {
					t.Fatal(err)
				}
			case "directory_replaced":
				directory := SyncFile{SourceType: SourceTypeBaiduPan, AccountId: file.AccountId, SyncPathId: file.SyncPathId, FileType: v115open.TypeDir, FileId: "/movies/Show", ParentId: "/movies", Path: "/movies", FileName: "Show", PickCode: "replaced-directory"}
				if err := db.Db.Create(&directory).Error; err != nil {
					t.Fatal(err)
				}
			}
			reads := forbidEmbyObservationHTTP(t)
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), &snapshot, roles); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "same_file" || scenario == "directory_moved_after_prepare" {
				want = 2
			}
			if len(snapshot.DirectoryScopes) != want || *reads != 0 {
				t.Fatalf("%s scopes=%+v requests=%d", scenario, snapshot.DirectoryScopes, *reads)
			}
			if scenario == "directory_moved_after_prepare" {
				directory := SyncFile{SourceType: SourceTypeBaiduPan, AccountId: file.AccountId, SyncPathId: file.SyncPathId, FileType: v115open.TypeDir,
					FileId: "/movies/Renamed", ParentId: "/movies", Path: "/movies", FileName: "Renamed", PickCode: "12"}
				if err := db.Db.Create(&directory).Error; err != nil {
					t.Fatal(err)
				}
				if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
				evidence, _, _ := readSnapshotSources(t, "101")
				metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
				if err != nil || len(metadata.DirectoryScopes) != 0 {
					t.Fatalf("移动后的百度目录仍采用旧位置：%+v %v", metadata, err)
				}
			}
		})
	}
}

func TestEmbyBaiduScrapeMovieEvidenceRequiresSameFSID(t *testing.T) {
	_, file, _ := setupEmbyDirectoryObservation(t)
	if err := db.Db.AutoMigrate(&Media{}, &ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	file.SourceType, file.PickCode = SourceTypeBaiduPan, "2002"
	scrapePath := ScrapePath{SourceType: file.SourceType, AccountId: file.AccountId}
	if err := db.Db.Create(&scrapePath).Error; err != nil {
		t.Fatal(err)
	}
	output := Media{ScrapePathId: scrapePath.ID, MediaType: MediaTypeMovie, Status: MediaStatusRenamed, VideoFileId: file.FileId, VideoFileName: file.FileName, Path: file.Path, PathId: file.ParentId}
	if err := db.Db.Create(&output).Error; err != nil {
		t.Fatal(err)
	}
	frozen := EmbyFrozenFile{SourceType: file.SourceType, AccountID: file.AccountId, FileID: file.FileId, ParentID: file.ParentId, PickCode: file.PickCode, Path: file.Path, FileName: file.FileName, LocalFilePath: file.LocalFilePath}
	for _, fsid := range []string{"", "1001", "2002"} {
		t.Run("historical_"+fsid, func(t *testing.T) {
			if err := db.Db.Model(&output).Update("video_pick_code", fsid).Error; err != nil {
				t.Fatal(err)
			}
			directory, err := embyScrapedMovieDirectory(t.Context(), frozen, "101")
			if err != nil || (directory != nil) != (fsid == "2002") {
				t.Fatalf("fsid=%q directory=%+v err=%v", fsid, directory, err)
			}
		})
	}
}
