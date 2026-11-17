package models

import (
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"qmediasync/internal/db"
)

func TestEmbySeasonPosterPathRepresentationsReachPlan(t *testing.T) {
	for _, spelling := range []string{"native", "slash"} {
		t.Run(spelling, func(t *testing.T) {
			config, _, file := setupEmbySnapshotModelTest(t)
			config.EnableDeleteNetdisk = 1
			if err := db.Db.Save(config).Error; err != nil {
				t.Fatal(err)
			}
			var root SyncPath
			if err := db.Db.First(&root, file.SyncPathId).Error; err != nil {
				t.Fatal(err)
			}
			root.LocalPath = filepath.Join(t.TempDir(), "媒体")
			if err := db.Db.Save(&root).Error; err != nil {
				t.Fatal(err)
			}
			seriesLocal := filepath.Join(root.GetFullLocalPath(), "Show")
			seasonLocal := filepath.Join(seriesLocal, "Season 1")
			file.Path, file.ParentId, file.FileName = "/movies/Show/Season 1", "season-root", "episode.mkv"
			file.LocalFilePath = filepath.Join(seasonLocal, "episode.strm")
			if err := db.Db.Save(&file).Error; err != nil {
				t.Fatal(err)
			}
			seedEmbyLocalRoot(t, file)
			poster := file
			poster.BaseModel = BaseModel{}
			poster.FileId, poster.FileName, poster.ParentId = "poster", "season01-poster.jpg", "show-root"
			poster.Path, poster.LocalFilePath = "/movies/Show", filepath.Join(seriesLocal, poster.FileName)
			poster.PickCode, poster.IsVideo, poster.IsMeta = "poster-code", false, true
			if spelling == "slash" {
				poster.LocalFilePath = filepath.ToSlash(poster.LocalFilePath)
			}
			if err := db.Db.Create(&poster).Error; err != nil {
				t.Fatal(err)
			}
			for _, variant := range []string{"other-season", "wrong-parent", "wrong-account"} {
				other := poster
				other.BaseModel, other.FileId = BaseModel{}, variant
				switch variant {
				case "other-season":
					other.FileName, other.LocalFilePath = "season02-poster.jpg", filepath.Join(seriesLocal, "season02-poster.jpg")
				case "wrong-parent":
					other.ParentId = "another-show"
				case "wrong-account":
					other.AccountId++
				}
				if err := db.Db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			snapshot := snapshotForFile(file)
			snapshot.Item.Type, snapshot.Item.SeriesId, snapshot.Item.SeasonId, snapshot.Item.ParentIndexNumber = "Episode", "201", "202", 1
			seasonNumber := 1
			roles := []EmbyMediaDirectory{
				{ItemID: "201", MediaType: "Series", LocalPath: seriesLocal},
				{ItemID: "202", MediaType: "Season", LocalPath: seasonLocal, SeasonNumber: &seasonNumber},
			}
			collector := EmbyDirectoryEvidenceCollector{}
			if err := collector.Enrich(t.Context(), MediaServerEmby, &snapshot, roles); err != nil {
				t.Fatal(err)
			}
			token, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			var evidence EmbyItemEvidence
			if err := db.Db.First(&evidence).Error; err != nil {
				t.Fatal(err)
			}
			metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil || len(metadata.ScopedFiles) != 1 || metadata.ScopedFiles[0].File.SyncFileID != poster.ID || metadata.ScopedFiles[0].File.LocalFilePath != poster.LocalFilePath {
				t.Fatalf("季海报冻结结果错误或改写原始路径：%+v err=%v", metadata, err)
			}
			input, err := CaptureEmbyDeletionTx(db.Db, token.MediaServerType, token.ServerID, "202", "Season")
			if err != nil || len(input.ScopedFiles) != 1 || len(input.DirectoryScopes) != 1 {
				t.Fatalf("季海报收件错误：%+v err=%v", input, err)
			}
			scope, frozen := input.DirectoryScopes[0], input.Owners[0].Files[0]
			provider := &embyDirectoryTestProvider{embyDeleteTestProvider: &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{
				frozen.FileID: embyRemoteFromFrozen(frozen), poster.FileId: embyRemoteFromFrozen(input.ScopedFiles[0].File),
			}}, root: EmbyRemoteFile{FileID: scope.Root.FileID, ParentID: scope.Root.ParentID, Path: scope.Root.Path, FileName: scope.Root.FileName, IsDir: true}}
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			if len(plan.Issues) != 0 || !slices.ContainsFunc(plan.Targets, func(target EmbyDeletionTarget) bool {
				return target.Kind == "scoped_metadata" && target.File.SyncFileID == poster.ID && target.File.AccountID == poster.AccountId && target.File.LocalRoot == root.GetFullLocalPath()
			}) {
				t.Fatalf("季海报未进入正确删除计划：%+v", plan)
			}
			target := plan.Targets[slices.IndexFunc(plan.Targets, func(target EmbyDeletionTarget) bool { return target.Kind == "scoped_metadata" })]
			if err := validateEmbyScopedDeletionTarget(t.Context(), plan, target); err != nil {
				t.Fatalf("本季无保留成员时海报应可核验：%v", err)
			}
			live := EmbyMediaItem{MediaServerType: token.MediaServerType, ItemId: "988", ServerId: token.ServerID, Type: "Episode", SeasonId: "202", SeriesId: "201", SnapshotID: 999, Generation: 1}
			if err := db.Db.Create(&live).Error; err != nil {
				t.Fatal(err)
			}
			if err := validateEmbyScopedDeletionTarget(t.Context(), plan, target); err == nil {
				t.Fatal("新增存活成员后不能删除本季海报")
			}
		})
	}
}

func TestJellyfinManagedSTRMPathRepresentations(t *testing.T) {
	for _, route := range []string{"direct", "fallback", "mapping"} {
		for _, spelling := range []string{"native", "slash"} {
			t.Run(route+"/"+spelling, func(t *testing.T) {
				f := setupEmbyAutoSTRMTest(t)
				local := f.local
				if spelling == "slash" {
					local = filepath.ToSlash(local)
				}
				file := SyncFile{SyncPathId: f.root.ID, SourceType: f.account.SourceType, AccountId: f.account.ID, FileId: "video-id", ParentId: "season-root", PickCode: "external", IsVideo: true, MTime: 1, Path: "movies/Show/Season 1", FileName: "episode.mkv", LocalFilePath: local}
				if err := db.Db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
				snapshot := f.snapshot
				snapshot.Item.MediaServerType = MediaServerJellyfin
				collector := EmbyDirectoryEvidenceCollector{pathConfig: &EmbyConfig{MediaServerType: MediaServerJellyfin}}
				switch route {
				case "direct":
					snapshot.Item.Path = f.local
					if spelling == "native" {
						snapshot.Item.Path = filepath.ToSlash(f.local)
					}
				case "mapping":
					collector.pathConfig.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: f.root.GetFullLocalPath()}}
				}
				// direct/mapping 不能被未知路径根数限制阻断；fallback 的表示参数不消耗逻辑候选预算。
				rootCount := embyAutoSTRMMaxCandidates + 1
				if route == "fallback" {
					rootCount = embyAutoSTRMMaxCandidates / 4
				}
				for i := 1; i < rootCount; i++ {
					other := f.root
					other.BaseModel, other.RemotePath = BaseModel{}, "unused-"+strconv.Itoa(i)
					other.BaseCid = other.RemotePath
					if err := db.Db.Create(&other).Error; err != nil {
						t.Fatal(err)
					}
				}
				snapshot.Sources = slices.Clone(snapshot.Sources)
				snapshot.Item.PickCode, snapshot.Item.MediaSourcePath = "", snapshot.Item.Path
				snapshot.Sources[0].Path, snapshot.Sources[0].PickCode = snapshot.Item.Path, ""
				unresolved, err := collector.RecoverJellyfinSTRMSources(t.Context(), &snapshot)
				if err != nil || unresolved || snapshot.Sources[0].Path != f.snapshot.Sources[0].Path || snapshot.Sources[0].PickCode != file.PickCode || snapshot.Item.MediaSourcePath != f.snapshot.Sources[0].Path {
					t.Fatalf("STRM 来源恢复错误：%+v unresolved=%v err=%v", snapshot, unresolved, err)
				}
				snapshot.pathConfig = collector.pathConfig
				files, err := resolveEmbySnapshotFiles(db.Db, snapshot)
				if err != nil || len(files) != 1 || files[0].SyncFileID != file.ID || files[0].AccountID != f.account.ID || files[0].LocalRoot != f.root.GetFullLocalPath() {
					t.Fatalf("恢复来源绑定错误：%+v err=%v", files, err)
				}
			})
		}
	}
}

func TestEmbySnapshotLocalPathDisambiguation(t *testing.T) {
	for _, mode := range []string{"direct", "mapping", "ambiguous", "dot-component", "parent-component", "trailing-separator", "case-only"} {
		t.Run(mode, func(t *testing.T) {
			f := setupEmbyAutoSTRMTest(t)
			otherRoot := f.root
			otherRoot.BaseModel, otherRoot.LocalPath = BaseModel{}, t.TempDir()
			otherRoot.BaseCid = "other-root"
			if err := db.Db.Create(&otherRoot).Error; err != nil {
				t.Fatal(err)
			}
			file := SyncFile{SyncPathId: f.root.ID, SourceType: f.account.SourceType, AccountId: f.account.ID, FileId: "video-id", ParentId: "season-root", PickCode: "external", IsVideo: true, MTime: 1, Path: "movies/Show/Season 1", FileName: "episode.mkv", LocalFilePath: filepath.ToSlash(f.local)}
			other := file
			other.SyncPathId, other.LocalFilePath = otherRoot.ID, filepath.Join(otherRoot.GetFullLocalPath(), "Show", "Season 1", "episode.strm")
			if mode == "ambiguous" {
				other.LocalFilePath = file.LocalFilePath
			}
			if err := db.Db.Create(&[]*SyncFile{&file, &other}).Error; err != nil {
				t.Fatal(err)
			}
			snapshot := f.snapshot
			snapshot.Item.Path = f.local
			snapshot.pathConfig = &EmbyConfig{}
			switch mode {
			case "mapping":
				snapshot.Item.Path = "/emby/Show/Season 1/episode.strm"
				snapshot.pathConfig.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: f.root.GetFullLocalPath()}}
			case "dot-component":
				snapshot.Item.Path = filepath.Dir(f.local) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(f.local)
			case "parent-component":
				snapshot.Item.Path = filepath.Dir(f.local) + string(filepath.Separator) + "unused" + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(f.local)
			case "trailing-separator":
				snapshot.Item.Path += string(filepath.Separator)
			case "case-only":
				snapshot.Item.Path = strings.TrimSuffix(f.local, "episode.strm") + "EPISODE.strm"
			}
			files, err := resolveEmbySnapshotFiles(db.Db, snapshot)
			wantMatch := mode == "direct" || mode == "mapping"
			if wantMatch {
				if err != nil || len(files) != 1 || files[0].SyncFileID != file.ID || files[0].AccountID != f.account.ID || files[0].LocalRoot != f.root.GetFullLocalPath() || files[0].LocalFilePath != file.LocalFilePath {
					t.Fatalf("未选中正确输出根或改写原始路径：%+v err=%v", files, err)
				}
			} else if !errors.Is(err, ErrEmbyIdentityAmbiguous) {
				t.Fatalf("歧义或非法路径被自动绑定：%+v err=%v", files, err)
			}
		})
	}
}

func TestEmbySnapshotWindowsMixedSeparatorDisambiguation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("需要原生 Windows 路径语义")
	}
	// 混合输入通过实际多根定位验证；存储仍只承诺原生和正斜杠两种表示。
	f := setupEmbyAutoSTRMTest(t)
	file := SyncFile{SyncPathId: f.root.ID, SourceType: f.account.SourceType, AccountId: f.account.ID, FileId: "video-id", PickCode: "external", IsVideo: true, MTime: 1, Path: "movies/Show/Season 1", FileName: "episode.mkv", LocalFilePath: filepath.ToSlash(f.local)}
	other := file
	other.LocalFilePath = filepath.Join(f.root.GetFullLocalPath(), "other.strm")
	if err := db.Db.Create(&[]*SyncFile{&file, &other}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := f.snapshot
	snapshot.Item.Path = filepath.Dir(f.local) + "/episode.strm"
	files, err := resolveEmbySnapshotFiles(db.Db, snapshot)
	if err != nil || len(files) != 1 || files[0].SyncFileID != file.ID {
		t.Fatalf("混合分隔符未定位正确对象：%+v err=%v", files, err)
	}
}
