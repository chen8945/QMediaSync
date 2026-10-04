package models

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

// 通过真实 provider 的 List/Stat/Delete 组合，保留 115 详情与账本的路径表示差异。
type embyDeletion115PathClient struct {
	files      map[string]EmbyRemoteFile
	directory  string
	deleted    []string
	statCalls  map[string]int
	beforeStat func(string, int, *EmbyRemoteFile)
}

func (c *embyDeletion115PathClient) GetFsDetailByCid(_ context.Context, id string) (*v115open.FileDetail, error) {
	c.statCalls[id]++
	file, exists := c.files[id]
	if !exists {
		return nil, v115open.NewOpenAPIError(430004, "gone")
	}
	if c.beforeStat != nil {
		c.beforeStat(id, c.statCalls[id], &file)
	}
	return &v115open.FileDetail{
		FileId: file.FileID, FileName: file.FileName, FileCategory: v115open.TypeFile,
		Path: file.Path, Paths: []v115open.FileDetailPath{{FileId: file.ParentID}},
		PickCode: file.PickCode, Sha1: file.SHA1, FileSizeByte: file.FileSize, Utime: strconv.FormatInt(file.MTime, 10),
	}, nil
}

func (c *embyDeletion115PathClient) GetFsListWithOptions(_ context.Context, parent string, cur, dirs, show bool, offset, limit int, options v115open.FileListOptions) (*v115open.FileListResp, error) {
	if parent != "7" || !cur || !dirs || !show || offset != 0 || limit != embyDeleteListPageSize || options.Order != "file_name" {
		return nil, fmt.Errorf("unexpected list arguments: parent=%s offset=%d", parent, offset)
	}
	response := &v115open.FileListResp{PathStr: c.directory, Path: []v115open.FileParentPath{{FileId: json.Number("7")}}}
	for _, file := range c.files {
		response.Data = append(response.Data, v115open.File{
			FileId: file.FileID, Pid: file.ParentID, FileName: file.FileName, FileCategory: v115open.TypeFile,
			PickCode: file.PickCode, Sha1: file.SHA1, FileSize: file.FileSize, Utime: file.MTime,
		})
	}
	slices.SortFunc(response.Data, func(a, b v115open.File) int { return strings.Compare(a.FileName, b.FileName) })
	response.Count = len(response.Data)
	return response, nil
}

func (c *embyDeletion115PathClient) DelOnce(_ context.Context, ids []string, parent string) (bool, error) {
	if len(ids) != 1 || parent != "7" {
		return false, fmt.Errorf("unexpected delete arguments: ids=%v parent=%s", ids, parent)
	}
	if _, exists := c.files[ids[0]]; !exists {
		return false, fmt.Errorf("duplicate delete: %s", ids[0])
	}
	c.deleted = append(c.deleted, ids[0])
	delete(c.files, ids[0])
	return true, nil
}

func setupEmbyDeletion115Paths(t *testing.T, directory, kind string) (EmbyDeletionPlan, *embyDeleteProvider, *embyDeletion115PathClient) {
	t.Helper()
	matrix := setupDeletionMatrix(t, nil)
	root := matrix.roots[1]
	root.RemotePath = directory
	if err := db.Db.Save(&root).Error; err != nil {
		t.Fatal(err)
	}
	matrix.roots[1] = root
	client := &embyDeletion115PathClient{files: map[string]EmbyRemoteFile{}, directory: strings.TrimPrefix(directory, "/"), statCalls: map[string]int{}}
	var selected SyncFile
	for _, entry := range []struct{ id, name string }{
		{"10", "Movie.mkv"}, {"11", "Movie.2160p.mkv"}, {"20", "Movie.nfo"}, {"21", "Movie.2160p.nfo"}, {"22", "poster.jpg"},
	} {
		video := path.Ext(entry.name) == ".mkv"
		file := matrix.addFile(t, 1, entry.id, directory, entry.name, video, !video, false)
		file.ParentId = "7"
		if err := db.Db.Save(&file).Error; err != nil {
			t.Fatal(err)
		}
		remote := matrix.remote[deletionMatrixKey(1, entry.id)]
		remote.ParentID, remote.Path = "7", client.directory
		client.files[entry.id] = remote
		if entry.id == "10" {
			selected = file
		}
	}
	snapshot := snapshotForFile(selected)
	snapshot.Item.Type = kind
	if err := ApplyEmbySnapshots(matrix.token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var input EmbyDeletionInput
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		input, err = CaptureEmbyDeletionTx(tx, matrix.token.ServerID, "101", kind)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	file := input.Owners[0].Files[0]
	provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, v115: client}
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	return plan, provider, client
}

func TestEmbyDeletion115PathRepresentations(t *testing.T) {
	for _, directory := range []string{"测试媒体库/测试电影/爱丽丝梦游仙境 (2010)", "/测试媒体库/测试电影/爱丽丝梦游仙境 (2010)", ".", "/"} {
		for _, kind := range []string{"Movie", "Episode"} {
			t.Run(directory+"/"+kind, func(t *testing.T) {
				plan, provider, client := setupEmbyDeletion115Paths(t, directory, kind)
				if len(plan.Targets) != 2 || len(plan.Issues) != 0 {
					t.Fatalf("expected video and exclusive sidecar: %+v", plan)
				}
				original := embyJSON(plan)
				for _, target := range plan.Targets {
					result := ExecuteEmbyDeletionTarget(t.Context(), plan, target, provider, allowEmbyDeleteTest)
					if result.Outcome != EmbyDeletionDeleted {
						t.Fatalf("%s was not deleted: %+v; provider calls=%v", target.Kind, result, client.deleted)
					}
				}
				for _, target := range plan.Targets {
					result := ExecuteEmbyDeletionTarget(t.Context(), plan, target, provider, allowEmbyDeleteTest)
					if result.Outcome != EmbyDeletionAlreadyAbsent {
						t.Fatalf("repeat target=%s result=%+v", target.Kind, result)
					}
				}
				if !slices.Equal(client.deleted, []string{"10", "20"}) || len(client.files) != 3 {
					t.Fatalf("wrong file set or repeated deletion: %v; retained=%v", client.deleted, client.files)
				}
				if embyJSON(plan) != original {
					t.Fatal("path comparison rewrote the frozen plan")
				}
				var evidence EmbyItemEvidence
				if err := db.Db.First(&evidence, plan.Input.Owners[0].Evidence.ID).Error; err != nil || evidence != plan.Input.Owners[0].Evidence {
					t.Fatalf("frozen evidence changed: %v", err)
				}
			})
		}
	}
}

func TestEmbyDeletion115PathIdentityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*EmbyRemoteFile)
	}{
		{"different directory", func(f *EmbyRemoteFile) { f.Path = "media-other" }},
		{"different parent", func(f *EmbyRemoteFile) { f.ParentID = "8" }},
		{"missing parent", func(f *EmbyRemoteFile) { f.ParentID = "" }},
		{"different file ID", func(f *EmbyRemoteFile) { f.FileID = "99" }},
		{"different name", func(f *EmbyRemoteFile) { f.FileName = "Other.mkv" }},
		{"different hash", func(f *EmbyRemoteFile) { f.SHA1 = "new-hash" }},
		{"different size", func(f *EmbyRemoteFile) { f.FileSize++ }},
		{"different mtime", func(f *EmbyRemoteFile) { f.MTime++ }},
		{"different pickcode", func(f *EmbyRemoteFile) { f.PickCode = "different" }},
		{"parent traversal", func(f *EmbyRemoteFile) { f.Path = "media/../media" }},
		{"root escape", func(f *EmbyRemoteFile) { f.Path = "../media" }},
		{"repeated separator", func(f *EmbyRemoteFile) { f.Path = "media//nested" }},
		{"backslash", func(f *EmbyRemoteFile) { f.Path = "media\\nested" }},
		{"NUL", func(f *EmbyRemoteFile) { f.Path = "media\x00" }},
	} {
		for _, at := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/stat%d", tc.name, at), func(t *testing.T) {
				plan, provider, client := setupEmbyDeletion115Paths(t, "media", "Movie")
				client.beforeStat = func(id string, count int, file *EmbyRemoteFile) {
					if id == "10" && count == at {
						tc.change(file)
					}
				}
				result := ExecuteEmbyDeletionTarget(t.Context(), plan, plan.Targets[0], provider, allowEmbyDeleteTest)
				if result.Outcome == EmbyDeletionDeleted || result.Outcome == EmbyDeletionAlreadyAbsent || result.Reason == "" || len(client.deleted) != 0 || client.statCalls["10"] != at {
					t.Fatalf("identity change accepted: %+v; deletes=%v stats=%v", result, client.deleted, client.statCalls)
				}
			})
		}
	}
}

func TestEmbyDeleteProvider115RequiresFrozenParent(t *testing.T) {
	plan, provider, client := setupEmbyDeletion115Paths(t, "media", "Movie")
	file := plan.Targets[0].File
	file.ParentID = ""
	ok, err := provider.Delete(t.Context(), file, func() error { return nil })
	if ok || err == nil || len(client.deleted) != 0 || len(client.statCalls) != 0 {
		t.Fatalf("missing original parent accepted: ok=%v err=%v deletes=%v stats=%v", ok, err, client.deleted, client.statCalls)
	}
}

func TestEmbyDeletion115PathRootAndSharedProtection(t *testing.T) {
	for _, directory := range []string{"media", "/media"} {
		for _, boundary := range []string{"same root", "ancestor of root", "root ID", "other account", "shared file"} {
			t.Run(directory+"/"+boundary, func(t *testing.T) {
				plan, provider, client := setupEmbyDeletion115Paths(t, directory, "Movie")
				file := plan.Targets[0].File
				otherPath := "media/Movie.mkv"
				if directory == "media" {
					otherPath = "/" + otherPath
				}
				root := SyncPath{SourceType: SourceType115, AccountId: file.AccountID, BaseCid: "other-root", LocalPath: "/unrelated", RemotePath: otherPath}
				switch boundary {
				case "ancestor of root":
					root.RemotePath += "/nested"
				case "root ID":
					root.RemotePath, root.BaseCid = "/unrelated", file.FileID
				case "other account":
					if err := db.Db.Model(&Account{}).Where("id = ?", file.AccountID).Update("user_id", "replacement").Error; err != nil {
						t.Fatal(err)
					}
				case "shared file":
					item := EmbyMediaItem{ItemId: "102", ItemIdInt: 102, ServerId: plan.Input.ServerID}
					link := EmbyMediaSyncFile{EmbyItemId: 102, SyncFileId: file.SyncFileID, SyncPathId: file.SyncPathID}
					if err := db.Db.Create(&item).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Db.Create(&link).Error; err != nil {
						t.Fatal(err)
					}
				}
				if boundary != "other account" && boundary != "shared file" {
					if err := db.Db.Create(&root).Error; err != nil {
						t.Fatal(err)
					}
				}
				result := ExecuteEmbyDeletionTarget(t.Context(), plan, plan.Targets[0], provider, allowEmbyDeleteTest)
				if result.Outcome != EmbyDeletionUnresolved || len(client.deleted) != 0 || len(client.statCalls) != 0 {
					t.Fatalf("protection bypassed: %+v; deletes=%v stats=%v", result, client.deleted, client.statCalls)
				}
			})
		}
	}
}

type embyDeletionOtherPathClient struct {
	file    EmbyFrozenFile
	deleted int
}

func (c *embyDeletionOtherPathClient) GetFileDetail(context.Context, string, int32) (*baidupan.FileDetail, error) {
	return nil, fmt.Errorf("unexpected Baidu fallback lookup")
}

func (c *embyDeletionOtherPathClient) GetFileListWithOptions(_ context.Context, directory string, folder int, empty, offset, limit int32, _ baidupan.FileListOptions) ([]*baidupan.FileInfo, error) {
	if directory != c.file.Path || folder != 0 || empty != 1 || offset != 0 || limit != embyDeleteListPageSize {
		return nil, fmt.Errorf("Baidu list arguments changed")
	}
	return []*baidupan.FileInfo{{FsId: 10, Path: path.Join(c.file.Path, c.file.FileName), ServerFilename: c.file.FileName, Size: uint64(c.file.FileSize), ServerMtime: uint64(c.file.MTime), Md5: c.file.SHA1}}, nil
}

func (c *embyDeletionOtherPathClient) Del(_ context.Context, paths []string) error {
	if !slices.Equal(paths, []string{path.Join(c.file.Path, c.file.FileName)}) {
		return fmt.Errorf("Baidu full delete path changed: %v", paths)
	}
	c.deleted++
	return nil
}

func (c *embyDeletionOtherPathClient) FileDetailContext(_ context.Context, fullPath string) (*openlist.FileDetail, error) {
	if fullPath != path.Join(c.file.Path, c.file.FileName) {
		return nil, fmt.Errorf("OpenList detail path changed")
	}
	return &openlist.FileDetail{Name: c.file.FileName, ID: c.file.OpenlistObjectID, Size: c.file.FileSize, Modified: time.Unix(c.file.MTime, 0).UTC().Format(time.RFC3339)}, nil
}

func (c *embyDeletionOtherPathClient) FileListWithRefresh(_ context.Context, directory string, page, limit int, refresh bool) (*openlist.FileListResp, error) {
	if directory != c.file.Path || page != 1 || limit != embyDeleteListPageSize || !refresh {
		return nil, fmt.Errorf("OpenList list arguments changed")
	}
	return &openlist.FileListResp{Total: 1, Content: []openlist.FileListItemInfo{{Name: c.file.FileName, ID: c.file.OpenlistObjectID, Size: c.file.FileSize, Modified: time.Unix(c.file.MTime, 0).UTC().Format(time.RFC3339)}}}, nil
}

func (c *embyDeletionOtherPathClient) DelContext(_ context.Context, directory string, names []string) error {
	if directory != c.file.Path || !slices.Equal(names, []string{c.file.FileName}) {
		return fmt.Errorf("OpenList directory/name delete arguments changed")
	}
	c.deleted++
	return nil
}

func TestEmbyDeletionOtherProviderRootRepresentations(t *testing.T) {
	for _, source := range []SourceType{SourceTypeBaiduPan, SourceTypeOpenList} {
		for _, remoteRoot := range []string{"media", "/media", ".", "/", "media-other", "media/../media"} {
			t.Run(string(source)+"/"+remoteRoot, func(t *testing.T) {
				matrix := setupDeletionMatrix(t, nil)
				var account Account
				if err := db.Db.First(&account, 1).Error; err != nil {
					t.Fatal(err)
				}
				account.SourceType, account.BaseUrl, account.Username = source, "https://openlist.invalid", "user"
				if err := db.Db.Save(&account).Error; err != nil {
					t.Fatal(err)
				}
				root := matrix.roots[1]
				root.SourceType, root.RemotePath = source, remoteRoot
				if err := db.Db.Save(&root).Error; err != nil {
					t.Fatal(err)
				}
				file := SyncFile{SourceType: source, AccountId: 1, SyncPathId: root.ID, FileId: "10", ParentId: "7", Path: "/media", FileName: "Movie.mkv", LocalFilePath: root.GetFullLocalPath() + "/Movie.strm", IsVideo: true, FileSize: 100, Sha1: "hash", MTime: 123}
				if source == SourceTypeOpenList {
					file.FileId, file.ParentId, file.OpenlistObjectId, file.Sha1 = "/media/Movie.mkv", "/media", "object-A", ""
				}
				if err := db.Db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
				frozen, err := freezeEmbyFile(db.Db, file, "source-101", file.LocalFilePath)
				if err != nil {
					t.Fatal(err)
				}
				client := &embyDeletionOtherPathClient{file: frozen}
				provider := &embyDeleteProvider{source: source, accountID: frozen.AccountID, accountIdentity: frozen.AccountIdentity, baidu: client, openlist: client}
				wantDelete := remoteRoot != "media-other" && remoteRoot != "media/../media"
				if (frozen.Reason == "") != wantDelete || frozen.RemoteRoot != remoteRoot || frozen.Path != file.Path || frozen.FileID != file.FileId {
					t.Fatalf("unexpected frozen scope or rewritten identity: %+v", frozen)
				}
				ok, err := provider.Delete(t.Context(), frozen, func() error { return nil })
				if ok != wantDelete || (err == nil) != wantDelete || (client.deleted == 1) != wantDelete {
					t.Fatalf("delete=%v calls=%d error=%v", ok, client.deleted, err)
				}
			})
		}
	}
}
