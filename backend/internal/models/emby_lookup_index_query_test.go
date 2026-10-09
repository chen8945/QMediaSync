package models

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
)

func TestSQLiteEmbyLookupQueryPlans(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := db.Db.AutoMigrate(&SyncFile{}, &EmbyMediaItem{}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSyncFileLookupIndexes(db.Db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureEmbySyncFileLookupIndexes(db.Db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureEmbyMediaLookupIndexes(db.Db); err != nil {
		t.Fatal(err)
	}
	files := []SyncFile{
		{BaseModel: BaseModel{ID: 1}, SourceType: SourceType115, AccountId: 7, SyncPathId: 11, Path: "/movies", IsMeta: true},
		{BaseModel: BaseModel{ID: 2}, SourceType: SourceType115, AccountId: 7, SyncPathId: 12, Path: "/movies", IsMeta: true},
		{BaseModel: BaseModel{ID: 3}, SourceType: SourceType115, AccountId: 8, SyncPathId: 11, Path: "/movies", IsMeta: true},
		{BaseModel: BaseModel{ID: 4}, SourceType: SourceTypeBaiduPan, AccountId: 7, SyncPathId: 11, Path: "/movies", IsMeta: true},
		{BaseModel: BaseModel{ID: 5}, SourceType: SourceType115, AccountId: 7, SyncPathId: 11, Path: "/movies"},
		{BaseModel: BaseModel{ID: 6}, SourceType: SourceType115, AccountId: 7, SyncPathId: 11, Path: "/movies", IsMeta: true, IsVideo: true},
	}
	name := strings.Repeat("完整目录名称", 1024)
	for id := uint(11); id <= 18; id++ {
		file := SyncFile{BaseModel: BaseModel{ID: id}, SourceType: SourceType115, AccountId: 7, SyncPathId: 11,
			Path: "/movies", FileName: name, PickCode: "directory-code", FileType: v115open.TypeDir}
		switch id {
		case 13:
			file.SyncPathId = 12
		case 14:
			file.SourceType = SourceTypeBaiduPan
		case 15:
			file.AccountId = 8
		case 16:
			file.Path = "/elsewhere"
		case 17:
			file.FileName += "-suffix"
		case 18:
			file.Path = "movies"
		}
		files = append(files, file)
	}
	items := []EmbyMediaItem{
		{BaseModel: BaseModel{ID: 1}, ItemId: "one", ServerId: "server", PartOfItemID: "parent", VersionOfItemID: "version"},
		{BaseModel: BaseModel{ID: 2}, ItemId: "two", ServerId: "server", PartOfItemID: "parent", VersionOfItemID: "version"},
		{BaseModel: BaseModel{ID: 3}, ItemId: "other-server", ServerId: "other", PartOfItemID: "parent", VersionOfItemID: "version"},
		{BaseModel: BaseModel{ID: 4}, ItemId: "other-parent", ServerId: "server", PartOfItemID: "other", VersionOfItemID: "other"},
	}
	for i := range 512 {
		files = append(files, SyncFile{BaseModel: BaseModel{ID: uint(i + 100)}, SyncPathId: 11,
			SourceType: SourceType115, AccountId: 7, Path: fmt.Sprintf("/other/%d", i), IsMeta: true})
		items = append(items, EmbyMediaItem{BaseModel: BaseModel{ID: uint(i + 100)},
			ItemId: fmt.Sprintf("filler-%d", i), ServerId: "server", PartOfItemID: "unrelated", VersionOfItemID: "unrelated"})
	}
	if err := db.Db.CreateInBatches(files, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.CreateInBatches(items, 100).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		query   string
		args    []any
		indexes []string
		key     string
		ids     []uint
	}{
		{"sidecars_across_roots", "SELECT * FROM sync_files WHERE source_type = ? AND account_id = ? AND path IN (?, ?) AND is_meta = ? AND is_video = ? ORDER BY id",
			[]any{SourceType115, 7, "/movies", "movies", true, false}, []string{syncFileSidecarLookupIndexName}, "is_video=?", []uint{1, 2}},
		{"complete_directory_name", "SELECT * FROM sync_files WHERE source_type = ? AND account_id = ? AND sync_path_id = ? AND file_type = ? AND path IN (?, ?) AND file_name = ? ORDER BY id",
			[]any{SourceType115, 7, 11, v115open.TypeDir, "/movies", "movies", name}, []string{syncFileDirectoryNameIndexName, syncFileNameIndexName}, "file_name=?", []uint{11, 12, 18}},
		{"baidu_directory_identity", "SELECT * FROM sync_files WHERE source_type = ? AND account_id = ? AND sync_path_id = ? AND file_type = ? AND pick_code = ? ORDER BY id",
			[]any{SourceTypeBaiduPan, 7, 11, v115open.TypeDir, "directory-code"}, []string{syncFileDirectoryPickCodeIndexName}, "pick_code=?", []uint{14}},
		{"part_members", "SELECT * FROM emby_media_items WHERE server_id = ? AND part_of_item_id IN (?) ORDER BY id",
			[]any{"server", "parent"}, []string{embyMediaPartLookupIndexName}, "part_of_item_id=?", []uint{1, 2}},
		{"version_members", "SELECT * FROM emby_media_items WHERE server_id = ? AND version_of_item_id IN (?) ORDER BY id",
			[]any{"server", "version"}, []string{embyMediaVersionLookupIndexName}, "version_of_item_id=?", []uint{1, 2}},
		{"multiple_part_members", "SELECT * FROM emby_media_items WHERE server_id = ? AND part_of_item_id IN (?, ?) ORDER BY id",
			[]any{"server", "parent", "missing"}, []string{embyMediaPartLookupIndexName, "idx_emby_server_id"}, "server_id=?", []uint{1, 2}},
		{"multiple_version_members", "SELECT * FROM emby_media_items WHERE server_id = ? AND version_of_item_id IN (?, ?) ORDER BY id",
			[]any{"server", "version", "missing"}, []string{embyMediaVersionLookupIndexName, "idx_emby_server_id"}, "server_id=?", []uint{1, 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rows []struct{ ID uint }
			if err := db.Db.Raw(test.query, test.args...).Scan(&rows).Error; err != nil {
				t.Fatal(err)
			}
			ids := make([]uint, len(rows))
			for i, row := range rows {
				ids[i] = row.ID
			}
			if !slices.Equal(ids, test.ids) {
				t.Fatalf("lookup scope/results changed: got=%v want=%v", ids, test.ids)
			}
			var plan []struct{ Detail string }
			if err := db.Db.Raw("EXPLAIN QUERY PLAN "+test.query, test.args...).Scan(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(plan, func(step struct{ Detail string }) bool {
				return strings.Contains(step.Detail, "SEARCH ") && strings.Contains(step.Detail, test.key) && slices.ContainsFunc(test.indexes, func(index string) bool {
					return strings.Contains(step.Detail, index)
				})
			}) {
				t.Fatalf("lookup did not use its scoped index: %+v", plan)
			}
			t.Logf("plan=%+v", plan)
		})
	}
}
