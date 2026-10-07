package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"
)

type deletionMatrixVideo struct {
	id, kind, name, directory, season, series, parent, version, sharedWith string
	account                                                                uint
	localID                                                                uint
}

type deletionMatrix struct {
	snapshots []EmbyItemSnapshot
	config    *EmbyConfig
	token     EmbyIndexToken
	roots     map[uint]SyncPath
	files     map[string]SyncFile
	remote    map[string]EmbyRemoteFile
	fail      map[string]bool
	called    []string
}

func setupDeletionMatrix(t *testing.T, videos []deletionMatrixVideo) *deletionMatrix {
	t.Helper()
	previousDB, previousConfig, previousLogger := db.Db, GlobalEmbyConfig, helpers.AppLogger
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Db, GlobalEmbyConfig = conn, nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		db.Db, GlobalEmbyConfig, helpers.AppLogger = previousDB, previousConfig, previousLogger
		_ = sqlDB.Close()
	})
	if err := conn.AutoMigrate(&EmbyConfig{}, &EmbyLibrarySyncPath{}, &EmbyMediaItem{}, &EmbyMediaSyncFile{}, &EmbyIndexState{}, &EmbyItemState{}, &EmbyItemEvidence{}, &EmbyItemMembership{}, &SyncFile{}, &SyncPath{}, &Account{}); err != nil {
		t.Fatal(err)
	}
	matrix := &deletionMatrix{
		config: &EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "offline-test", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1},
		roots:  map[uint]SyncPath{}, files: map[string]SyncFile{}, remote: map[string]EmbyRemoteFile{}, fail: map[string]bool{},
	}
	if err := conn.Create(matrix.config).Error; err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []uint{1, 2} {
		account := Account{BaseModel: BaseModel{ID: accountID}, SourceType: SourceType115, UserId: fmt.Sprintf("owner-%d", accountID)}
		root := SyncPath{
			BaseModel: BaseModel{ID: accountID}, SourceType: SourceType115, AccountId: accountID,
			BaseCid: fmt.Sprintf("root-%d", accountID), LocalPath: fmt.Sprintf("/strm/account-%d", accountID), RemotePath: "/movies",
		}
		if err := conn.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		if err := conn.Create(&root).Error; err != nil {
			t.Fatal(err)
		}
		matrix.roots[accountID] = root
	}
	matrix.token, err = BeginEmbyIndexRead("matrix-server", matrix.config)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []EmbyItemSnapshot
	for _, video := range videos {
		if video.account == 0 {
			video.account = 1
		}
		if video.kind == "" {
			video.kind = "Movie"
		}
		if video.directory == "" {
			video.directory = "/movies"
		}
		file, shared := matrix.files[video.sharedWith]
		if video.sharedWith != "" && !shared {
			t.Fatalf("missing shared physical file %s", video.sharedWith)
		}
		if !shared {
			file = matrix.addFile(t, video.account, "file-"+video.id, video.directory, video.name, true, false, false)
		}
		matrix.files[video.id] = file
		item := EmbyMediaItem{
			BaseModel: BaseModel{ID: video.localID}, ItemId: video.id, Type: video.kind, Name: "shared-title",
			Path: file.LocalFilePath, PickCode: file.PickCode, LibraryId: "library", SeasonId: video.season, SeriesId: video.series,
			PartOfItemID: video.parent, VersionOfItemID: video.version, DateCreated: "2026-10-01T00:00:00Z",
		}
		for _, candidate := range videos {
			if candidate.parent == video.id {
				item.PartCount++
			}
		}
		if item.PartCount != 0 {
			item.PartCount++
		}
		source := EmbySnapshotSource{ID: "source-" + video.id, ItemID: video.id, Path: "http://qms.test/stream?pickcode=" + file.PickCode, PickCode: file.PickCode}
		item.MediaSourcePath = source.Path
		snapshots = append(snapshots, EmbyItemSnapshot{Item: item, Sources: []EmbySnapshotSource{source}, MembersComplete: video.kind != "Video"})
	}
	matrix.snapshots = snapshots
	if err := ApplyEmbySnapshots(matrix.token, snapshots); err != nil {
		t.Fatal(err)
	}
	return matrix
}

// observeSidecars 明确模拟元数据已生成后的 Emby 观察；接收删除时不能回填历史。
func (matrix *deletionMatrix) observeSidecars(t *testing.T) {
	t.Helper()
	if err := ApplyEmbySnapshots(matrix.token, matrix.snapshots); err != nil {
		t.Fatal(err)
	}
}

func (matrix *deletionMatrix) addFile(t *testing.T, account uint, id, directory, name string, video, meta, isDir bool) SyncFile {
	t.Helper()
	root := matrix.roots[account]
	relative, err := filepath.Rel(root.RemotePath, directory)
	if err != nil {
		t.Fatal(err)
	}
	localName := name
	if video {
		localName = strings.TrimSuffix(name, filepath.Ext(name)) + ".strm"
	}
	file := SyncFile{
		SourceType: SourceType115, AccountId: account, SyncPathId: root.ID, FileId: id,
		ParentId: "parent-" + directory, FileName: name, FileType: v115open.TypeFile, FileSize: 100,
		PickCode: "pick-" + id, Path: directory, LocalFilePath: filepath.Join(root.GetFullLocalPath(), relative, localName),
		Sha1: "hash-" + id, MTime: 1234, IsVideo: video, IsMeta: meta,
	}
	if isDir {
		file.FileType = v115open.TypeDir
	}
	if err := db.Db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	matrix.remote[deletionMatrixKey(account, id)] = EmbyRemoteFile{
		FileID: id, ParentID: file.ParentId, FileName: name, Path: directory, PickCode: file.PickCode,
		SHA1: file.Sha1, FileSize: file.FileSize, MTime: file.MTime, FileCreatedAt: file.CreatedAt, IsDir: isDir,
	}
	return file
}

func deletionMatrixKey(account uint, id string) string {
	return fmt.Sprintf("%d:%s", account, id)
}

func (matrix *deletionMatrix) Stat(_ context.Context, file EmbyFrozenFile) (EmbyRemoteFile, error) {
	remote, exists := matrix.remote[deletionMatrixKey(file.AccountID, file.FileID)]
	if !exists {
		return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
	}
	return remote, nil
}

func (matrix *deletionMatrix) List(_ context.Context, file EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	var listing []EmbyRemoteFile
	for key, remote := range matrix.remote {
		if strings.HasPrefix(key, fmt.Sprintf("%d:", file.AccountID)) && remote.Path == file.Path {
			listing = append(listing, remote)
		}
	}
	slices.SortFunc(listing, func(a, b EmbyRemoteFile) int { return strings.Compare(a.FileID, b.FileID) })
	return listing, nil
}

func (matrix *deletionMatrix) Delete(_ context.Context, file EmbyFrozenFile, guard func() error) (bool, error) {
	if err := guard(); err != nil {
		return false, err
	}
	key := deletionMatrixKey(file.AccountID, file.FileID)
	matrix.called = append(matrix.called, key)
	if matrix.fail[key] {
		return false, nil
	}
	delete(matrix.remote, key)
	return true, nil
}

func (matrix *deletionMatrix) plan(t *testing.T, itemID, kind string) EmbyDeletionPlan {
	t.Helper()
	var input EmbyDeletionInput
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		input, err = CaptureEmbyDeletionTx(tx, "matrix-server", itemID, kind)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildEmbyDeletionPlan(context.Background(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return matrix, nil })
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func deletionMatrixActiveIDs(plan EmbyDeletionPlan) []string {
	var ids []string
	for _, target := range plan.Targets {
		if target.Reason == "" {
			ids = append(ids, deletionMatrixKey(target.File.AccountID, target.File.FileID))
		}
	}
	slices.Sort(ids)
	return ids
}

func assertDeletionMatrixIDs(t *testing.T, got, want []string) {
	t.Helper()
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("physical file set=%v, want=%v", got, want)
	}
}

func TestEmbyDeletionMatrixPhysicalMembership(t *testing.T) {
	for _, tc := range []struct {
		name, itemID, kind string
		videos             []deletionMatrixVideo
		want               []string
	}{
		{
			name: "movie confirmed parts exclude alternate version", itemID: "101", kind: "Movie",
			videos: []deletionMatrixVideo{
				{id: "101", name: "Movie - part1.mkv"},
				{id: "102", kind: "Video", name: "Movie - part2.mkv", parent: "101"},
				{id: "103", name: "Movie - Extended.mkv", version: "101"},
				{id: "104", name: "Neighbor.mkv"},
			},
			want: []string{"1:file-101", "1:file-102"},
		},
		{
			name: "episode selects only part1", itemID: "201", kind: "Episode",
			videos: []deletionMatrixVideo{
				{id: "201", kind: "Episode", name: "Show S01E01 - part1.mkv", season: "900", series: "800"},
				{id: "202", kind: "Video", name: "Show S01E01 - part2.mkv", parent: "201", season: "900", series: "800"},
			},
			want: []string{"1:file-201"},
		},
		{
			name: "video selects itself", itemID: "202", kind: "Video",
			videos: []deletionMatrixVideo{
				{id: "201", kind: "Episode", name: "Show - part1.mkv", season: "900", series: "800"},
				{id: "202", kind: "Video", name: "Show - part2.mkv", parent: "201", season: "900", series: "800"},
			},
			want: []string{"1:file-202"},
		},
		{
			name: "season spans accounts and directories without local row ID collision", itemID: "900", kind: "Season",
			videos: []deletionMatrixVideo{
				{id: "201", localID: 9, kind: "Episode", name: "S01E01.mkv", directory: "/movies/Show/Season 01", season: "900", series: "800"},
				{id: "202", kind: "Episode", name: "S01E02.mkv", directory: "/movies/Show flat", season: "900", series: "800", account: 2},
				{id: "9", localID: 201, kind: "Episode", name: "S02E01.mkv", directory: "/movies/Show/Season 01", season: "901", series: "800"},
				{id: "204", kind: "Episode", name: "S00E01.mkv", directory: "/movies/Show/Specials", season: "902", series: "800"},
				{id: "205", kind: "Video", name: "S01E01 - part2.mkv", directory: "/movies/Show/Season 01", season: "900", series: "800", parent: "201"},
			},
			want: []string{"1:file-201", "2:file-202", "1:file-205"},
		},
		{
			name: "series follows IDs through flat and named seasons", itemID: "800", kind: "Series",
			videos: []deletionMatrixVideo{
				{id: "201", kind: "Episode", name: "S01E01.mkv", directory: "/movies/Show/Season 01", season: "900", series: "800"},
				{id: "202", kind: "Episode", name: "S02E01.mkv", directory: "/movies/Flat", season: "901", series: "800", account: 2},
				{id: "203", kind: "Episode", name: "S00E01.mkv", directory: "/movies/Show/Season Specials", season: "902", series: "800"},
				{id: "204", kind: "Episode", name: "Other S01E01.mkv", directory: "/movies/Show/Season 01", season: "903", series: "801"},
				{id: "205", kind: "Video", name: "S02E01 - part2.mkv", directory: "/movies/Flat", season: "901", series: "800", parent: "202", account: 2},
			},
			want: []string{"1:file-201", "2:file-202", "1:file-203", "2:file-205"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := setupDeletionMatrix(t, tc.videos)
			plan := matrix.plan(t, tc.itemID, tc.kind)
			assertDeletionMatrixIDs(t, deletionMatrixActiveIDs(plan), tc.want)
			if len(matrix.called) != 0 {
				t.Fatal("planning performed a destructive provider call")
			}
			results := matrix.execute(t, plan)
			assertDeletionMatrixIDs(t, matrix.called, tc.want)
			for _, result := range results {
				if result.Outcome != EmbyDeletionDeleted {
					t.Fatalf("confirmed member deletion failed: %+v", results)
				}
			}
			if err := FinalizeEmbyDeletionPlan(context.Background(), plan, results); err != nil {
				t.Fatal(err)
			}
			for _, video := range tc.videos {
				file := matrix.files[video.id]
				selected := slices.Contains(tc.want, deletionMatrixKey(file.AccountId, file.FileId))
				assertDeletionMatrixOwner(t, video.id, !selected)
			}
		})
	}
}

func TestEmbyDeletionMatrixSidecarOwners(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mainName  string
		retained  string
		metadata  []string
		wantNames []string
	}{
		{
			name: "extended version does not lose prefixed metadata", retained: "Movie - Extended.mkv",
			metadata:  []string{"Movie.nfo", "Movie-poster.jpg", "Movie.en.srt", "Movie - Extended.nfo", "Movie - Extended-fanart.jpg", "poster.jpg", "tvshow.nfo"},
			wantNames: []string{"Movie.mkv", "Movie.nfo", "Movie-poster.jpg", "Movie.en.srt"},
		},
		{
			name: "dot stem is an independent retained video", retained: "Movie.zh.mkv",
			metadata:  []string{"Movie.nfo", "Movie.zh.nfo", "Movie.zh-poster.jpg", "Movie.zh.srt"},
			wantNames: []string{"Movie.mkv", "Movie.nfo"},
		},
		{
			name: "same stem containers share sidecars", retained: "Movie.mp4",
			metadata:  []string{"Movie.nfo", "Movie-poster.jpg", "Movie.en.srt"},
			wantNames: []string{"Movie.mkv"},
		},
		{
			name: "poster named movie cannot claim generic directory artwork", mainName: "poster.mkv",
			metadata: []string{"poster.jpg", "folder.jpg", "fanart.jpg"}, wantNames: []string{"poster.mkv"},
		},
		{
			name: "tvshow named movie cannot claim series metadata", mainName: "tvshow.mkv",
			metadata: []string{"tvshow.nfo"}, wantNames: []string{"tvshow.mkv"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mainName := tc.mainName
			if mainName == "" {
				mainName = "Movie.mkv"
			}
			videos := []deletionMatrixVideo{{id: "101", name: mainName}}
			if tc.retained != "" {
				videos = append(videos, deletionMatrixVideo{id: "102", name: tc.retained})
			}
			matrix := setupDeletionMatrix(t, videos)
			for i, name := range tc.metadata {
				matrix.addFile(t, 1, fmt.Sprintf("meta-%d", i), "/movies", name, false, true, false)
			}
			// 列表完整也不授权删除未知内容或目录。
			matrix.addFile(t, 1, "unknown-file", "/movies", "unrelated.txt", false, false, false)
			matrix.addFile(t, 1, "unknown-folder", "/movies", "Extras", false, false, true)
			matrix.observeSidecars(t)
			plan := matrix.plan(t, "101", "Movie")
			var got []string
			for _, target := range plan.Targets {
				if target.Reason == "" {
					got = append(got, target.File.FileName)
				}
			}
			assertDeletionMatrixIDs(t, got, tc.wantNames)
			names := map[string]string{}
			for key, file := range matrix.remote {
				names[key] = file.FileName
			}
			matrix.execute(t, plan)
			var deletedNames []string
			for _, key := range matrix.called {
				deletedNames = append(deletedNames, names[key])
			}
			assertDeletionMatrixIDs(t, deletedNames, tc.wantNames)
		})
	}
}

func TestEmbyDeletionMatrixSubtitleOwnershipUsesLongestStem(t *testing.T) {
	for _, tc := range []struct {
		name           string
		main, retained string
		metadata       []string
		wantNames      []string
	}{
		{
			name:      "labelled and empty label subtitles follow the only video",
			metadata:  []string{"Movie.zh-CN.简体中文.Default.ass", "Movie..ass", "Movie - Extended.ass"},
			wantNames: []string{"Movie.mkv", "Movie.zh-CN.简体中文.Default.ass"},
		},
		{
			name: "deleting the extended version takes its labelled subtitle", main: "Movie.Extended.mkv", retained: "Movie.mkv",
			metadata:  []string{"Movie.Extended.zh.ass", "Movie.zh.ass"},
			wantNames: []string{"Movie.Extended.mkv", "Movie.Extended.zh.ass"},
		},
		{
			name: "deleting the plain version keeps the extended subtitle", main: "Movie.mkv", retained: "Movie.Extended.mkv",
			metadata:  []string{"Movie.Extended.zh.ass", "Movie.zh.ass"},
			wantNames: []string{"Movie.mkv", "Movie.zh.ass"},
		},
		{
			name: "exact name owns the subtitle over a shorter prefix", main: "Movie.mkv", retained: "Movie.en.mkv",
			metadata:  []string{"Movie.en.srt"},
			wantNames: []string{"Movie.mkv"},
		},
		{
			name: "deleting the exact name owner takes the shared-stem subtitle", main: "Movie.en.mkv", retained: "Movie.mkv",
			metadata:  []string{"Movie.en.srt"},
			wantNames: []string{"Movie.en.mkv", "Movie.en.srt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main := tc.main
			if main == "" {
				main = "Movie.mkv"
			}
			videos := []deletionMatrixVideo{{id: "101", name: main}}
			if tc.retained != "" {
				videos = append(videos, deletionMatrixVideo{id: "102", name: tc.retained})
			}
			matrix := setupDeletionMatrix(t, videos)
			for i, name := range tc.metadata {
				matrix.addFile(t, 1, fmt.Sprintf("meta-%d", i), "/movies", name, false, true, false)
			}
			matrix.observeSidecars(t)
			plan := matrix.plan(t, "101", "Movie")
			var got []string
			for _, target := range plan.Targets {
				if target.Reason == "" {
					got = append(got, target.File.FileName)
				}
			}
			assertDeletionMatrixIDs(t, got, tc.wantNames)
			names := map[string]string{}
			for key, file := range matrix.remote {
				names[key] = file.FileName
			}
			matrix.execute(t, plan)
			var deletedNames []string
			for _, key := range matrix.called {
				deletedNames = append(deletedNames, names[key])
			}
			assertDeletionMatrixIDs(t, deletedNames, tc.wantNames)
		})
	}
}

func TestEmbyDeletionMatrixFreezesLabelledSubtitleEvidence(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{{id: "101", name: "Movie.mkv"}})
	matrix.addFile(t, 1, "meta-labelled", "/movies", "Movie.zh-CN.简体中文.Default.ass", false, true, false)
	matrix.observeSidecars(t)
	var state EmbyItemState
	if err := db.Db.Where("server_id = ? AND item_id = ?", matrix.token.ServerID, "101").First(&state).Error; err != nil {
		t.Fatal(err)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence, state.SnapshotID).Error; err != nil {
		t.Fatal(err)
	}
	metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(metadata.ExclusiveFiles, func(file EmbyFrozenFile) bool { return file.FileName == "Movie.zh-CN.简体中文.Default.ass" }) {
		t.Fatalf("冻结证据缺少中文标签字幕：%#v", metadata.ExclusiveFiles)
	}
}

func TestEmbyDeletionMatrixSameDirectoryAcrossAccounts(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{
		{id: "101", name: "Movie.mkv"},
		{id: "102", name: "Movie.mkv", account: 2},
	})
	matrix.addFile(t, 1, "metadata", "/movies", "Movie.nfo", false, true, false)
	matrix.addFile(t, 2, "metadata", "/movies", "Movie.nfo", false, true, false)
	matrix.observeSidecars(t)
	plan := matrix.plan(t, "101", "Movie")
	assertDeletionMatrixIDs(t, deletionMatrixActiveIDs(plan), []string{"1:file-101", "1:metadata"})
}

func TestEmbyDeletionMatrixProtectsEverySavedRoot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		registerRoot bool
		want         []string
	}{
		{name: "root-level video remains an explicit file", want: []string{"1:file-101"}},
		{name: "a second sync root identity is protected", registerRoot: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := setupDeletionMatrix(t, []deletionMatrixVideo{{id: "101", name: "Movie.mkv"}})
			matrix.addFile(t, 1, "unknown-directory", "/movies", "Unindexed", false, false, true)
			if tc.registerRoot {
				root := SyncPath{SourceType: SourceType115, AccountId: 1, BaseCid: "file-101", LocalPath: "/other-local", RemotePath: "/other-root"}
				if err := db.Db.Create(&root).Error; err != nil {
					t.Fatal(err)
				}
			}
			plan := matrix.plan(t, "101", "Movie")
			results := matrix.execute(t, plan)
			assertDeletionMatrixIDs(t, matrix.called, tc.want)
			if tc.registerRoot && (len(results) != 1 || results[0].Outcome != EmbyDeletionUnresolved) {
				t.Fatalf("protected root must remain unresolved: %+v", results)
			}
			for _, target := range plan.Targets {
				if target.File.FileID == "unknown-directory" || target.File.FileID == "root-1" {
					t.Fatal("file planning expanded to a directory or saved root")
				}
			}
		})
	}
}

func (matrix *deletionMatrix) execute(t *testing.T, plan EmbyDeletionPlan) []EmbyDeletionResult {
	t.Helper()
	provider := &embyJointTestProvider{deletionMatrix: matrix}
	groups, err := GroupEmbyDeletionTargets(plan.Targets, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var results []EmbyDeletionResult
	for _, group := range groups {
		results = append(results, ExecuteEmbyDeletionBatch(ctx, plan, group, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })...)
	}
	return results
}

func TestEmbyDeletionMatrixSharedSourceDoesNotDelete(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{
		{id: "301", name: "Shared.mkv"},
		{id: "302", name: "Shared.mkv", sharedWith: "301"},
	})
	plan := matrix.plan(t, "301", "Movie")
	results := matrix.execute(t, plan)
	if len(results) != 1 || results[0].Outcome != EmbyDeletionUnresolved || results[0].Reason == "" {
		t.Fatalf("shared target must retain a reason: %+v", results)
	}
	assertDeletionMatrixIDs(t, matrix.called, nil)
	if err := FinalizeEmbyDeletionPlan(context.Background(), plan, results); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"301", "302"} {
		assertDeletionMatrixOwner(t, id, true)
	}
}

func assertDeletionMatrixOwner(t *testing.T, id string, exists bool) {
	t.Helper()
	var items, links int64
	if err := db.Db.Model(&EmbyMediaItem{}).Where("item_id = ?", id).Count(&items).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&EmbyMediaSyncFile{}).Where("emby_item_id = ?", id).Count(&links).Error; err != nil {
		t.Fatal(err)
	}
	want := int64(0)
	if exists {
		want = 1
	}
	if items != want || links != want {
		t.Fatalf("item %s: items=%d links=%d, want=%d", id, items, links, want)
	}
}

func TestEmbyDeletionMatrixEpisodePart2CanReindex(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{
		{id: "201", kind: "Episode", name: "Show - part1.mkv", season: "900", series: "800"},
		{id: "202", kind: "Video", name: "Show - part2.mkv", parent: "201", season: "900", series: "800"},
	})
	matrix.addFile(t, 1, "meta-part1", "/movies", "Show - part1.nfo", false, true, false)
	matrix.addFile(t, 1, "meta-part2", "/movies", "Show - part2.nfo", false, true, false)
	matrix.observeSidecars(t)
	plan := matrix.plan(t, "201", "Episode")
	results := matrix.execute(t, plan)
	assertDeletionMatrixIDs(t, matrix.called, []string{"1:file-201", "1:meta-part1"})
	for _, result := range results {
		if result.Outcome != EmbyDeletionDeleted {
			t.Fatalf("expected successful part1-only deletion: %+v", results)
		}
	}
	if err := FinalizeEmbyDeletionPlan(context.Background(), plan, results); err != nil {
		t.Fatal(err)
	}
	assertDeletionMatrixOwner(t, "201", false)
	assertDeletionMatrixOwner(t, "202", true)
	if _, ok := matrix.remote["1:file-202"]; !ok {
		t.Fatal("remaining part2 disappeared")
	}
	if _, ok := matrix.remote["1:meta-part2"]; !ok {
		t.Fatal("remaining part2 metadata disappeared")
	}
	file := matrix.files["202"]
	fresh, err := BeginEmbyIndexRead("matrix-server", matrix.config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := EmbyItemSnapshot{
		Item:            EmbyMediaItem{ItemId: "203", Type: "Episode", Path: file.LocalFilePath, LibraryId: "library", SeasonId: "900", SeriesId: "800"},
		Sources:         []EmbySnapshotSource{{ID: "source-203", ItemID: "203", Path: "http://qms.test/stream?pickcode=" + file.PickCode, PickCode: file.PickCode}},
		MembersComplete: true,
	}
	if err := ApplyEmbySnapshots(fresh, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatalf("retained part2 could not reindex under its new Emby ID: %v", err)
	}
	assertDeletionMatrixOwner(t, "203", true)
	var link EmbyMediaSyncFile
	if err := db.Db.Where("emby_item_id = ?", 203).First(&link).Error; err != nil || link.SyncFileId != file.ID {
		t.Fatalf("new part2 identity did not retain its physical file: %+v error=%v", link, err)
	}
}

func TestEmbyDeletionMatrixPartialBatchRetainsEvidenceAndConfirmsSidecars(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{
		{id: "101", name: "Movie - part1.mkv"},
		{id: "102", kind: "Video", name: "Movie - part2.mkv", parent: "101"},
		{id: "103", name: "Movie - Extended.mkv", version: "101"},
	})
	matrix.addFile(t, 1, "meta-part1", "/movies", "Movie - part1.nfo", false, true, false)
	matrix.addFile(t, 1, "meta-part2", "/movies", "Movie - part2.nfo", false, true, false)
	matrix.addFile(t, 1, "meta-version", "/movies", "Movie - Extended.nfo", false, true, false)
	matrix.fail["1:file-102"] = true
	matrix.observeSidecars(t)
	plan := matrix.plan(t, "101", "Movie")
	results := matrix.execute(t, plan)
	assertDeletionMatrixIDs(t, matrix.called, []string{"1:file-101", "1:file-102", "1:meta-part1", "1:meta-part2"})
	outcomes := map[string]EmbyDeletionOutcome{}
	for _, target := range plan.Targets {
		for _, result := range results {
			if result.Key == target.Key {
				outcomes[target.File.FileID] = result.Outcome
			}
		}
	}
	if outcomes["file-101"] != EmbyDeletionDeleted || outcomes["file-102"] != EmbyDeletionFailed || outcomes["meta-part1"] != EmbyDeletionDeleted || outcomes["meta-part2"] != EmbyDeletionDeleted {
		t.Fatalf("partial result lost target outcomes: %v", outcomes)
	}
	if err := FinalizeEmbyDeletionPlan(context.Background(), plan, results); err != nil {
		t.Fatal(err)
	}
	assertDeletionMatrixOwner(t, "101", false)
	assertDeletionMatrixOwner(t, "102", true)
	assertDeletionMatrixOwner(t, "103", true)
	for _, key := range []string{"1:file-102", "1:file-103", "1:meta-version"} {
		if _, exists := matrix.remote[key]; !exists {
			t.Fatalf("unfinished or retained physical target %s lost", key)
		}
	}
	for _, owner := range plan.Input.Owners {
		var evidence EmbyItemEvidence
		if err := db.Db.First(&evidence, owner.Evidence.ID).Error; err != nil {
			t.Fatalf("immutable retry evidence removed: %v", err)
		}
		var files []EmbyFrozenFile
		if err := json.Unmarshal([]byte(evidence.FilesJSON), &files); err != nil || len(files) != 1 || files[0].FileID != owner.Files[0].FileID {
			t.Fatalf("retry identity changed: %+v error=%v", files, err)
		}
	}
	var ledgerCount int64
	if err := db.Db.Model(&SyncFile{}).Count(&ledgerCount).Error; err != nil || ledgerCount != 6 {
		t.Fatalf("linked deletion unexpectedly removed STRM reconciliation ledger: count=%d error=%v", ledgerCount, err)
	}
}

func TestEmbyDeletionMatrixSidecarPlanningIssuesFinalizePerDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, issue string
		historical  bool
	}{
		{name: "sidecar identity changed", issue: "sidecar_identity_changed:", historical: true},
		{name: "sidecar history unconfirmed", issue: "sidecar_history_unconfirmed:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := setupDeletionMatrix(t, []deletionMatrixVideo{
				{id: "201", kind: "Episode", name: "S01E01.mkv", season: "900", series: "800"},
				{id: "202", kind: "Episode", name: "S01E02.mkv", directory: "/movies/other", season: "900", series: "800"},
				{id: "203", kind: "Episode", name: "S01E01.mkv", account: 2, season: "900", series: "800"},
			})
			matrix.addFile(t, 1, "metadata", "/movies", "S01E01.nfo", false, true, false)
			if tc.historical {
				matrix.observeSidecars(t)
				remote := matrix.remote["1:metadata"]
				remote.SHA1 = "replacement-hash"
				matrix.remote["1:metadata"] = remote
			}
			plan := matrix.plan(t, "900", "Season")
			if len(plan.Issues) != 1 || !strings.HasPrefix(plan.Issues[0], tc.issue) {
				t.Fatalf("unexpected sidecar planning issues: %v", plan.Issues)
			}
			results := matrix.execute(t, plan)
			assertDeletionMatrixIDs(t, matrix.called, []string{"1:file-201", "1:file-202", "2:file-203"})
			for _, result := range results {
				if result.Outcome != EmbyDeletionDeleted {
					t.Fatalf("confirmed video deletion failed: %+v", results)
				}
			}
			if err := FinalizeEmbyDeletionPlan(t.Context(), plan, results); err != nil {
				t.Fatal(err)
			}
			assertDeletionMatrixOwner(t, "201", true)
			assertDeletionMatrixOwner(t, "202", false)
			assertDeletionMatrixOwner(t, "203", false)
			if _, exists := matrix.remote["1:metadata"]; !exists {
				t.Fatal("unconfirmed sidecar was removed")
			}
			for _, owner := range plan.Input.Owners {
				var evidence EmbyItemEvidence
				if err := db.Db.First(&evidence, owner.Evidence.ID).Error; err != nil || evidence != owner.Evidence {
					t.Fatalf("immutable owner evidence changed: item=%s error=%v", owner.Item.ItemId, err)
				}
			}
			var ledgerCount int64
			if err := db.Db.Model(&SyncFile{}).Count(&ledgerCount).Error; err != nil || ledgerCount != 4 {
				t.Fatalf("STRM reconciliation ledger changed: count=%d error=%v", ledgerCount, err)
			}
		})
	}
}

func TestEmbyDeletionMatrixFinalizePlanningIssueSourceScope(t *testing.T) {
	for _, tc := range []struct {
		name, issue string
		retained    bool
	}{
		{name: "inventory unavailable in another source", issue: "sidecar_inventory_unavailable:"},
		{name: "identity changed in another source", issue: "sidecar_identity_changed:"},
		{name: "history unconfirmed in another source", issue: "sidecar_history_unconfirmed:"},
		{name: "unknown issue remains conservative", issue: "sidecar_unknown:", retained: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := setupDeletionMatrix(t, []deletionMatrixVideo{{id: "101", name: "Movie.mkv"}})
			plan := matrix.plan(t, "101", "Movie")
			results := matrix.execute(t, plan)
			if len(results) != 1 || results[0].Outcome != EmbyDeletionDeleted {
				t.Fatalf("confirmed video deletion failed: %+v", results)
			}
			// 同账号 ID 和路径也不能将其他来源的问题关联到本 owner；未知问题仍须保留。
			file := plan.Input.Owners[0].Files[0]
			plan.Issues = append(plan.Issues, tc.issue+embyDigest([]any{SourceTypeBaiduPan, file.AccountID, file.Path}))
			if err := FinalizeEmbyDeletionPlan(t.Context(), plan, results); err != nil {
				t.Fatal(err)
			}
			assertDeletionMatrixOwner(t, "101", tc.retained)
		})
	}
}

func TestEmbyDeletionMatrixSharedSidecarAllOwnersSelected(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{
		{id: "101", kind: "Episode", name: "Movie.mkv", season: "900", series: "800"},
		{id: "102", kind: "Episode", name: "Movie.mp4", season: "900", series: "800"},
	})
	matrix.addFile(t, 1, "shared-metadata", "/movies", "Movie.nfo", false, true, false)
	matrix.observeSidecars(t)
	plan := matrix.plan(t, "900", "Season")
	want := []string{"1:file-101", "1:file-102", "1:shared-metadata"}
	assertDeletionMatrixIDs(t, deletionMatrixActiveIDs(plan), want)
	results := matrix.execute(t, plan)
	assertDeletionMatrixIDs(t, matrix.called, want)
	for _, result := range results {
		if result.Outcome != EmbyDeletionDeleted {
			t.Fatalf("all selected users should allow shared metadata deletion: %+v", results)
		}
	}
	if err := FinalizeEmbyDeletionPlan(context.Background(), plan, results); err != nil {
		t.Fatal(err)
	}
	assertDeletionMatrixOwner(t, "101", false)
	assertDeletionMatrixOwner(t, "102", false)
}

func TestEmbyDeletionMatrixNewVideoProtectsPlannedSubtitle(t *testing.T) {
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{{id: "101", name: "Movie.mkv"}})
	matrix.addFile(t, 1, "subtitle", "/movies", "Movie.zh.srt", false, true, false)
	matrix.observeSidecars(t)
	plan := matrix.plan(t, "101", "Movie")
	assertDeletionMatrixIDs(t, deletionMatrixActiveIDs(plan), []string{"1:file-101", "1:subtitle"})
	// 新使用者尚未进入 QMS 台账；执行时的新鲜远端清单仍必须保护其同 stem 字幕。
	matrix.remote["1:new-video"] = EmbyRemoteFile{
		FileID: "new-video", ParentID: "parent-/movies", FileName: "Movie.zh.mkv", Path: "/movies",
		PickCode: "new-pick", SHA1: "new-sha1", FileSize: 100, MTime: 5678,
	}
	results := matrix.execute(t, plan)
	assertDeletionMatrixIDs(t, matrix.called, []string{"1:file-101"})
	for _, target := range plan.Targets {
		if target.File.FileID != "subtitle" {
			continue
		}
		found := false
		for _, result := range results {
			if result.Key == target.Key {
				found = true
				if result.Outcome != EmbyDeletionUnresolved || result.Reason == "" {
					t.Fatalf("new subtitle user was not protected: %+v", result)
				}
			}
		}
		if !found {
			t.Fatal("planned subtitle lost its unresolved result")
		}
	}
	if err := FinalizeEmbyDeletionPlan(context.Background(), plan, results); err != nil {
		t.Fatal(err)
	}
	assertDeletionMatrixOwner(t, "101", true)
	for _, key := range []string{"1:new-video", "1:subtitle"} {
		if _, exists := matrix.remote[key]; !exists {
			t.Fatalf("new user or shared subtitle %s was removed", key)
		}
	}
}

// 重见拒绝是确定性终态：共同批次与目录执行器都必须返回 unresolved 并保留原因，不能落 failed 触发重试。
type embyReappearedDeleteProvider struct{ *deletionMatrix }

func (embyReappearedDeleteProvider) Delete(context.Context, EmbyFrozenFile, func() error) (bool, error) {
	return false, fmt.Errorf("%w，保留核验", ErrEmbyDeletionReappeared)
}

type embyReappearedDirectoryProvider struct {
	*embyDirectoryTestProvider
	calls int
}

func (p *embyReappearedDirectoryProvider) DeleteDirectory(context.Context, EmbyDirectoryScope, func() error) (bool, error) {
	p.calls++
	return false, fmt.Errorf("%w，保留重新出现的对象", ErrEmbyDeletionReappeared)
}

func assertEmbyReappearedUnresolved(t *testing.T, results []EmbyDeletionResult) {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("results=%+v", results)
	}
	if results[0].Outcome != EmbyDeletionUnresolved || !strings.Contains(results[0].Reason, ErrEmbyDeletionReappeared.Error()) {
		t.Fatalf("重见拒绝未落 unresolved 终态并保留原因: %+v", results[0])
	}
}

func TestEmbyDeletionReappearedRefusalIsUnresolvedInEveryExecutor(t *testing.T) {
	t.Run("joint batch", func(t *testing.T) {
		matrix := setupDeletionMatrix(t, []deletionMatrixVideo{{id: "101", name: "Movie.mkv"}})
		plan := matrix.plan(t, "101", "Movie")
		results := ExecuteEmbyDeletionBatch(context.Background(), plan, plan.Targets, embyReappearedDeleteProvider{matrix}, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
		assertEmbyReappearedUnresolved(t, results)
		if len(matrix.called) != 0 {
			t.Fatalf("重见拒绝仍触发原始删除: %v", matrix.called)
		}
	})
	t.Run("directory", func(t *testing.T) {
		input, provider := setupEmbyDirectoryTest(t, "Movie")
		plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
		targets := embyDirectoryTargetsOnly(plan)
		if len(targets) != 1 {
			t.Fatalf("directory targets=%+v", targets)
		}
		ctx, release, err := BeginEmbyDeletionExecution(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		providerWrapper := &embyReappearedDirectoryProvider{embyDirectoryTestProvider: provider}
		result := ExecuteEmbyDeletionDirectory(ctx, plan, targets[0], providerWrapper, allowEmbyDeleteTest, func() error { return nil })
		if result.Outcome != EmbyDeletionUnresolved || !strings.Contains(result.Reason, ErrEmbyDeletionReappeared.Error()) {
			t.Fatalf("目录执行器未按重见拒绝保留 unresolved: %+v", result)
		}
		if providerWrapper.calls != 1 || provider.rootWrites != 0 {
			t.Fatalf("重见拒绝的目录发送次数或写入异常: calls=%d writes=%d", providerWrapper.calls, provider.rootWrites)
		}
	})
}
