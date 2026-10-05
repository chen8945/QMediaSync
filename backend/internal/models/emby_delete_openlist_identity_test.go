package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/openlist"
)

func TestEmbyOpenListDeletionUsesIndependentIdentity(t *testing.T) {
	file := embyProviderTestFile(SourceTypeOpenList)
	file.OpenlistMD5 = "md5"
	remote, err := embyRemoteOpenList(file.Path, openlist.FileListItemInfo{
		Name: file.FileName, ID: file.OpenlistObjectID, Size: file.FileSize,
		Modified:    time.Unix(file.MTime, 0).UTC().Format(time.RFC3339),
		HashInfoMap: map[string]string{"sha1": file.OpenlistSHA1, "md5": file.OpenlistMD5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(file.PickCode, "https://") || remote.PickCode != "" {
		t.Fatal("fixture must reproduce the stored download URL and empty provider PickCode")
	}
	for _, tc := range []struct {
		name   string
		change func(*EmbyRemoteFile)
		match  bool
	}{
		{name: "download URL is not object identity", match: true},
		{name: "changed object", change: func(f *EmbyRemoteFile) { f.OpenlistObjectID = "replacement" }},
		{name: "changed SHA1", change: func(f *EmbyRemoteFile) { f.OpenlistSHA1 = "replacement" }},
		{name: "changed MD5", change: func(f *EmbyRemoteFile) { f.OpenlistMD5 = "replacement" }},
		{name: "changed size", change: func(f *EmbyRemoteFile) { f.FileSize++ }},
		{name: "changed time", change: func(f *EmbyRemoteFile) { f.MTime++ }},
		{name: "changed parent", change: func(f *EmbyRemoteFile) { f.ParentID = "/elsewhere" }},
		{name: "changed path", change: func(f *EmbyRemoteFile) { f.Path = "/elsewhere" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := remote
			if tc.change != nil {
				tc.change(&current)
			}
			if got := embyRemoteMatches(file, current); got != tc.match {
				t.Errorf("plan identity match=%v, want %v", got, tc.match)
			}
			if got := embyProviderFileMatches(file, current); got != tc.match {
				t.Errorf("provider identity match=%v, want %v", got, tc.match)
			}
		})
	}
	owner := EmbyDeletionOwnerRef{ItemID: "100"}
	target := EmbyDeletionTarget{Kind: "video", File: file, Owners: []EmbyDeletionOwnerRef{owner}}
	owners, ok := embySidecarOwners("Movie,\nPart1.nfo", []EmbyRemoteFile{remote}, []EmbyDeletionTarget{target})
	if !ok || len(owners) != 1 || owners[0] != owner {
		t.Fatalf("download URL prevented exclusive sidecar ownership: owners=%v ok=%v", owners, ok)
	}
}

func TestEmby115DeletionStillChecksPickCode(t *testing.T) {
	file := embyProviderTestFile(SourceType115)
	file.PickCode = "original-code"
	remote := embyRemoteFromFrozen(file)
	remote.PickCode = "replacement-code"
	if embyRemoteMatches(file, remote) || embyProviderFileMatches(file, remote) {
		t.Fatal("changed 115 PickCode was accepted")
	}
}

func TestEmbyOpenListPlanDeletesVideoAndSidecarWithDownloadURLs(t *testing.T) {
	embyProviderTestLogger(t)
	config, token, video := setupEmbySnapshotModelTest(t)
	entries := map[string]openlist.FileListItemInfo{}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			content := make([]openlist.FileListItemInfo, 0, len(entries))
			for _, file := range entries {
				content = append(content, file)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": content, "total": len(content)}})
		case "/api/fs/get":
			var body struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			file, found := entries[path.Base(body.Path)]
			if !found {
				t.Errorf("unexpected detail of absent file %q", body.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": file})
		case "/api/fs/remove":
			var body struct {
				Dir   string   `json:"dir"`
				Names []string `json:"names"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			slices.Sort(body.Names)
			if body.Dir != "/movies" || !slices.Equal(body.Names, []string{"movie.mkv", "movie.nfo"}) {
				t.Errorf("wrong deletion scope: %+v", body)
			}
			writes++
			for _, name := range body.Names {
				delete(entries, name)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success"})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	var account Account
	if err := db.Db.First(&account, video.AccountId).Error; err != nil {
		t.Fatal(err)
	}
	account.SourceType, account.BaseUrl, account.Username = SourceTypeOpenList, server.URL, "user"
	if err := db.Db.Save(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", video.SyncPathId).Updates(map[string]any{"source_type": SourceTypeOpenList, "base_cid": "/movies"}).Error; err != nil {
		t.Fatal(err)
	}
	video.SourceType, video.FileId, video.ParentId = SourceTypeOpenList, "/movies/movie.mkv", "/movies"
	video.Sha1, video.OpenlistObjectId, video.OpenlistSHA1 = "", "video-object", "video-hash"
	video.PickCode = helpers.MakeOpenListUrl(server.URL, "test-sign", video.FileId)
	if err := db.Db.Save(&video).Error; err != nil {
		t.Fatal(err)
	}
	sidecar := video
	sidecar.BaseModel = BaseModel{}
	sidecar.FileId, sidecar.FileName = "/movies/movie.nfo", "movie.nfo"
	sidecar.LocalFilePath = path.Join(path.Dir(video.LocalFilePath), "movie.nfo")
	sidecar.OpenlistObjectId, sidecar.OpenlistSHA1 = "nfo-object", "nfo-hash"
	sidecar.PickCode = helpers.MakeOpenListUrl(server.URL, "test-sign", sidecar.FileId)
	sidecar.IsVideo, sidecar.IsMeta = false, true
	if err := db.Db.Create(&sidecar).Error; err != nil {
		t.Fatal(err)
	}
	for _, file := range []SyncFile{video, sidecar} {
		entries[file.FileName] = openlist.FileListItemInfo{Name: file.FileName, ID: file.OpenlistObjectId, Size: file.FileSize, Modified: time.Unix(file.MTime, 0).UTC().Format(time.RFC3339), HashInfoMap: map[string]string{"sha1": file.OpenlistSHA1}}
	}
	entries["notes.txt"] = openlist.FileListItemInfo{Name: "notes.txt", ID: "unknown-object"}
	if err := db.Db.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(video)
	snapshot.Item.MediaSourcePath, snapshot.Sources[0].Path = video.PickCode, video.PickCode
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var input EmbyDeletionInput
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		input, err = CaptureEmbyDeletionTx(tx, token.ServerID, "101", "Movie")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	client := openlist.NewTemporaryClient(server.URL, "user", "password", "token")
	t.Cleanup(func() { _ = client.Close() })
	provider := &embyDeleteProvider{source: SourceTypeOpenList, accountID: account.ID, accountIdentity: EmbyAccountIdentity(account), openlist: client}
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, allowEmbyDeleteTest)
	if err != nil || len(plan.Issues) != 0 || len(plan.Targets) != 2 {
		t.Fatalf("targets=%d issues=%v error=%v", len(plan.Targets), plan.Issues, err)
	}
	for _, target := range plan.Targets {
		if target.File.PickCode == "" || strings.Contains(target.File.PickCode, "test-sign") || target.Key != EmbyDeletionFileKey(target.File) {
			t.Fatalf("frozen URL or key changed: %+v", target)
		}
	}
	results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
	if writes != 1 || len(results) != 2 || len(entries) != 1 || entries["notes.txt"].ID == "" {
		t.Fatalf("writes=%d results=%+v remaining=%v", writes, results, entries)
	}
	for _, result := range results {
		if result.Outcome != EmbyDeletionDeleted {
			t.Fatalf("unfinished target: %+v", result)
		}
	}
}
