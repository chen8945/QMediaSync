package emby

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestWebhookCleanupPreservesLiveCrossFolderSource(t *testing.T) {
	for _, test := range []struct {
		name        string
		indexed     bool
		staleLedger bool
	}{
		{name: "indexed_source_control", indexed: true},
		{name: "stale_ledger_outside_directory", indexed: true, staleLedger: true},
		{name: "unindexed_source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", true)
			var root models.SyncPath
			if err := db.Db.First(&root, f.file.SyncPathId).Error; err != nil {
				t.Fatal(err)
			}
			// 存活 STRM 位于兄弟目录，实际播放源仍在待删作品目录。
			local := filepath.Join(root.GetFullLocalPath(), "Other", "live.strm")
			if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
				t.Fatal(err)
			}
			const playback = "http://qms/stream?pickcode=p-live"
			if err := os.WriteFile(local, []byte(playback), 0600); err != nil {
				t.Fatal(err)
			}
			remote := models.EmbyRemoteFile{FileID: "live-remote", ParentID: f.file.ParentId, Path: f.file.Path, FileName: "live.mkv", PickCode: "p-live", SHA1: "live-sha", FileSize: 250, MTime: 123}
			f.cloud.files[remote.FileID] = remote
			if test.indexed {
				file := f.file
				file.BaseModel = models.BaseModel{}
				file.FileId, file.FileName, file.PickCode, file.Sha1, file.FileSize, file.LocalFilePath = remote.FileID, remote.FileName, remote.PickCode, remote.SHA1, remote.FileSize, local
				if test.staleLedger {
					file.Path, file.ParentId = path.Join(root.RemotePath, "OldLocation"), "old-parent"
				}
				if err := db.Db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "202", Type: "Movie", Path: local, MediaSources: []embyclientrestgo.MediaSource{{ID: "live-source", ItemID: "202", Path: playback}}}}
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			if _, exists := f.cloud.files[remote.FileID]; !exists {
				t.Fatalf("surviving Emby item lost its real cloud source: directoryWrites=%d status=%s, actualRemotePath=%s, survivingSTRM=%s", f.cloud.directoryCalls, readWebhookTest(t, record.ID).Status, remote.Path, local)
			}
			assertCleanupWorkerDone(t, record.ID)
			if f.cloud.directoryCalls != 0 || len(f.cloud.batchCalls) != 1 || len(f.cloud.scalarCalls) != 0 {
				t.Fatalf("precise fallback not used: directories=%d batches=%v scalar=%v", f.cloud.directoryCalls, f.cloud.batchCalls, f.cloud.scalarCalls)
			}
			ids := slices.Clone(f.cloud.batchCalls[0])
			slices.Sort(ids)
			if !slices.Equal(ids, []string{"f1", "image", "nfo"}) {
				t.Fatalf("selected video and exclusive sidecars did not progress together: %v", ids)
			}
			for _, id := range ids {
				if _, exists := f.cloud.files[id]; exists {
					t.Errorf("selected file %q retained", id)
				}
				row := cleanupWorkerRows(t, record.ID)[id]
				if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
					t.Errorf("selected file %q result=%+v", id, row)
				}
			}
			if f.fullScans.Load() != 1 {
				t.Fatalf("directory fallback repeated the full Emby scan: %d", f.fullScans.Load())
			}
			t.Logf("provider 方法调用：目录删除=%d，文件批次=%d，List=%d，Stat=%d；Emby HTTP=%d，完整列表=%d", f.cloud.directoryCalls, len(f.cloud.batchCalls), f.cloud.listCalls, len(f.cloud.statCalls), f.totalHTTP.Load(), f.fullScans.Load())
		})
	}
}

func TestWebhookCleanupSourceVerificationCounts115HTTP(t *testing.T) {
	for _, test := range []struct {
		name          string
		indexed       bool
		staleLedger   bool
		outside       bool
		duplicateLive bool
		lookupFailure bool
		playback      string
		baseURL       string
		wantDirectory bool
		wantLookup    bool
	}{
		{name: "known_qms_source_uses_ledger", indexed: true, outside: true, wantDirectory: true},
		{name: "stale_ledger_direct_child", indexed: true, staleLedger: true},
		{name: "unindexed_disjoint_source_directory", outside: true, wantDirectory: true, wantLookup: true},
		{name: "configured_base_path_query_uses_generated_origin", outside: true, baseURL: "http://qms/prefix?token=fixture", wantDirectory: true, wantLookup: true},
		{name: "duplicate_source_reuses_both_lookup_steps", outside: true, duplicateLive: true, wantDirectory: true, wantLookup: true},
		{name: "unindexed_internal_source_fallback"},
		{name: "lookup_failure_fallback", outside: true, lookupFailure: true, wantLookup: true},
		{name: "missing_account_fallback", playback: "http://qms/115/url/video.mkv?pickcode=p-live"},
		{name: "unsupported_url_fallback", playback: "http://qms/stream?pickcode=p-live&userid=source-fixture"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", true)
			if err := db.Db.AutoMigrate(&models.Settings{}); err != nil {
				t.Fatal(err)
			}
			baseURL := test.baseURL
			if baseURL == "" {
				baseURL = "http://qms"
			}
			if err := db.Db.Create(&models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: baseURL}}).Error; err != nil {
				t.Fatal(err)
			}
			previous115Logger := helpers.V115Log
			helpers.V115Log = helpers.AppLogger
			t.Cleanup(func() { helpers.V115Log = previous115Logger })
			// 账号缓存和浏览版本均按 ID 隔离，只在该实例首次请求前设置本地传输层。
			account := models.Account{BaseModel: models.BaseModel{ID: uint(8_000_000 + cleanupSourceAccountID.Add(1))}, SourceType: models.SourceType115, UserId: "source-fixture", Token: "fixture"}
			if err := db.Db.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			var root models.SyncPath
			if err := db.Db.First(&root, f.file.SyncPathId).Error; err != nil {
				t.Fatal(err)
			}
			root.AccountId, root.BaseCid = account.ID, "1"
			if err := db.Db.Save(&root).Error; err != nil {
				t.Fatal(err)
			}
			cloud := map[string]models.EmbyRemoteFile{
				"1":  {FileID: "1", ParentID: "0", Path: "/", FileName: "movies", IsDir: true},
				"2":  {FileID: "2", ParentID: "1", Path: "/movies", FileName: "Feature", IsDir: true},
				"3":  {FileID: "3", ParentID: "1", Path: "/movies", FileName: "Other", IsDir: true},
				"30": {FileID: "30", ParentID: "2", Path: "/movies/Feature", FileName: "notes.txt", FileSize: 5, MTime: 123},
				"31": {FileID: "31", ParentID: "2", Path: "/movies/Feature", FileName: "unknown.jpg", FileSize: 5, MTime: 123},
				"40": {FileID: "40", ParentID: "2", Path: "/movies/Feature", FileName: "Extras", IsDir: true},
				"41": {FileID: "41", ParentID: "40", Path: "/movies/Feature/Extras", FileName: "unknown.mkv", FileSize: 5, MTime: 123},
			}
			var files []models.SyncFile
			if err := db.Db.Order("id").Find(&files).Error; err != nil {
				t.Fatal(err)
			}
			for i := range files {
				file := &files[i]
				file.AccountId, file.FileId, file.ParentId = account.ID, strconv.Itoa(10+i), "2"
				if err := db.Db.Save(file).Error; err != nil {
					t.Fatal(err)
				}
				if file.ID == f.file.ID {
					f.file = *file
				}
				cloud[file.FileId] = models.EmbyRemoteFile{FileID: file.FileId, ParentID: file.ParentId, Path: file.Path, FileName: file.FileName, PickCode: file.PickCode, SHA1: file.Sha1, FileSize: file.FileSize, MTime: file.MTime}
			}
			snapshot := workerSnapshot(f.file)
			if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			f.directory.Root.AccountID, f.directory.Root.AccountIdentity = account.ID, models.EmbyAccountIdentity(account)
			f.directory.Root.RootFileID, f.directory.Root.FileID, f.directory.Root.ParentID = "1", "2", "1"
			f.directory.Ancestors = []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "1", ParentID: "0", Path: "/movies"}}
			var evidence models.EmbyItemEvidence
			if err := db.Db.Where("item_id = ?", "101").Order("id DESC").First(&evidence).Error; err != nil {
				t.Fatal(err)
			}
			metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil {
				t.Fatal(err)
			}
			metadata.DirectoryScopes = []models.EmbyDirectoryScope{f.directory}
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&evidence).Update("sidecars_json", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}
			live := models.EmbyRemoteFile{FileID: "20", ParentID: "2", Path: "/movies/Feature", FileName: "live.mkv", PickCode: "p-live", SHA1: "live-sha1", FileSize: 250, MTime: 123}
			if test.outside {
				live.ParentID, live.Path = "3", "/movies/Other"
			}
			cloud[live.FileID] = live
			liveLocal := filepath.Join(root.GetFullLocalPath(), "Other", "live.strm")
			if err := os.MkdirAll(filepath.Dir(liveLocal), 0700); err != nil {
				t.Fatal(err)
			}
			playback := test.playback
			if playback == "" {
				playback = "http://qms/115/url/video.mkv?pickcode=p-live&userid=source-fixture"
			}
			if err := os.WriteFile(liveLocal, []byte(playback), 0600); err != nil {
				t.Fatal(err)
			}
			if test.indexed {
				file := models.SyncFile{SourceType: root.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileId: live.FileID, ParentId: live.ParentID, Path: live.Path, FileName: live.FileName, PickCode: live.PickCode, IsVideo: true, LocalFilePath: liveLocal}
				if test.staleLedger {
					file.ParentId, file.Path = "3", "/movies/Other"
				}
				if err := db.Db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "202", Type: "Movie", Path: liveLocal, MediaSources: []embyclientrestgo.MediaSource{{ID: "live-source", ItemID: "202", Path: playback}}}}
			if test.duplicateLive {
				duplicateLocal := filepath.Join(root.GetFullLocalPath(), "Other", "duplicate.strm")
				duplicateURL := playback + "&path=duplicate.mkv&sign=second"
				if err := os.WriteFile(duplicateLocal, []byte(duplicateURL), 0600); err != nil {
					t.Fatal(err)
				}
				f.live = append(f.live, embyclientrestgo.BaseItemDtoV2{Id: "203", Type: "Movie", Path: duplicateLocal, MediaSources: []embyclientrestgo.MediaSource{{ID: "duplicate-source", ItemID: "203", Path: duplicateURL}}})
			}
			lists, details, lookupIDs, lookupPositions, writes := 0, 0, 0, 0, 0
			var detailIDs, deleted []string
			account.Get115Client().SetTransport(cleanupSourceHTTPTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Host != "proapi.115.com" {
					t.Errorf("unexpected 115 HTTP host: %s", r.URL.Host)
				}
				writeJSON := func(value any) {
					if err := json.NewEncoder(w).Encode(value); err != nil {
						t.Error(err)
					}
				}
				switch r.URL.Path {
				case "/open/ufile/downurl":
					lookupIDs++
					if r.Method != http.MethodPost || r.FormValue("pick_code") != live.PickCode {
						t.Errorf("unexpected source lookup: %s %s", r.Method, r.FormValue("pick_code"))
					}
					if test.lookupFailure {
						fmt.Fprint(w, `{"state":true,"data":{}}`)
						return
					}
					writeJSON(map[string]any{"state": true, "data": map[string]any{live.FileID: map[string]any{"file_name": live.FileName, "file_size": live.FileSize, "pick_code": live.PickCode, "sha1": live.SHA1, "url": map[string]string{"url": "https://fixture.invalid/video"}}}})
				case "/open/folder/get_info":
					id := r.URL.Query().Get("file_id")
					if id == live.FileID {
						lookupPositions++
					} else {
						details++
						detailIDs = append(detailIDs, id)
					}
					file, exists := cloud[id]
					if !exists {
						fmt.Fprint(w, `{"state":false,"code":430004}`)
						return
					}
					ancestors := []map[string]string{}
					for parentID := file.ParentID; parentID != "0"; {
						parent, ok := cloud[parentID]
						if !ok {
							t.Errorf("fixture parent %s missing for %s", parentID, id)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						ancestors = append(ancestors, map[string]string{"file_id": parentID, "file_name": parent.FileName})
						parentID = parent.ParentID
					}
					ancestors = append(ancestors, map[string]string{"file_id": "0", "file_name": ""})
					slices.Reverse(ancestors)
					category := "1"
					if file.IsDir {
						category = "0"
					}
					writeJSON(map[string]any{"state": true, "data": map[string]any{"file_id": id, "file_name": file.FileName, "file_category": category, "size_byte": file.FileSize, "pick_code": file.PickCode, "sha1": file.SHA1, "utime": strconv.FormatInt(file.MTime, 10), "paths": ancestors}})
				case "/open/ufile/files":
					lists++
					offset := r.URL.Query().Get("offset")
					if r.URL.Query().Get("cid") != "2" || offset != "" && offset != "0" {
						t.Errorf("unexpected 115 cloud listing: %s", r.URL.RawQuery)
					}
					entries := []map[string]any{}
					for _, file := range cloud {
						if file.ParentID != "2" {
							continue
						}
						category := "1"
						if file.IsDir {
							category = "0"
						}
						entries = append(entries, map[string]any{"fid": file.FileID, "pid": file.ParentID, "fc": category, "fn": file.FileName, "pc": file.PickCode, "sha1": file.SHA1, "fs": file.FileSize, "upt": file.MTime, "aid": "1"})
					}
					writeJSON(map[string]any{"state": true, "count": len(entries), "offset": 0, "path": []map[string]string{{"cid": "0", "name": ""}, {"cid": "1", "name": "movies"}, {"cid": "2", "name": "Feature"}}, "data": entries})
				case "/open/ufile/delete":
					writes++
					if r.Method != http.MethodPost {
						t.Errorf("unexpected 115 delete method: %s", r.Method)
					}
					ids := strings.Split(r.FormValue("file_ids"), ",")
					deleted = append(deleted, ids...)
					for _, id := range ids {
						file, exists := cloud[id]
						if !exists || file.ParentID != r.FormValue("parent_id") {
							t.Errorf("115 delete used wrong identity or parent: id=%s parent=%s", id, r.FormValue("parent_id"))
						}
						fullPath := path.Join(file.Path, file.FileName)
						for childID, child := range cloud {
							if childID == id || strings.HasPrefix(path.Join(child.Path, child.FileName), fullPath+"/") {
								delete(cloud, childID)
							}
						}
					}
					fmt.Fprint(w, `{"state":true,"code":0,"errno":0}`)
				default:
					t.Errorf("unexpected 115 HTTP request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			f.worker.provider = models.NewEmbyDeleteProvider
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			for _, id := range []string{"1", "3", "20"} {
				if _, exists := cloud[id]; !exists {
					t.Errorf("protected cloud file/directory %s was deleted", id)
				}
			}
			for _, id := range []string{"10", "11", "12"} {
				if _, exists := cloud[id]; exists {
					t.Errorf("selected cloud file %s was retained", id)
				}
			}
			for _, id := range []string{"2", "30", "31", "40", "41"} {
				if _, exists := cloud[id]; exists == test.wantDirectory {
					t.Errorf("directory or unknown content %s retention=%t, directoryAllowed=%t", id, exists, test.wantDirectory)
				}
			}
			wantDeleted := []string{"10", "11", "12"}
			wantDetails := []string{"2", "10", "11", "12"}
			wantLists, wantLookupIDs, wantLookupPositions := 1, 0, 0
			if test.wantDirectory {
				wantDeleted, wantDetails = []string{"2"}, []string{"2", "2", "2"}
			}
			if test.wantLookup {
				wantLookupIDs = 1
				if !test.lookupFailure {
					wantLookupPositions = 1
				}
			}
			slices.Sort(deleted)
			slices.Sort(detailIDs)
			slices.Sort(wantDetails)
			if !slices.Equal(deleted, wantDeleted) || !slices.Equal(detailIDs, wantDetails) {
				t.Errorf("115 writes=%v want=%v; ordinary detail IDs=%v want=%v", deleted, wantDeleted, detailIDs, wantDetails)
			}
			if lists != wantLists || details != len(wantDetails) || lookupIDs != wantLookupIDs || lookupPositions != wantLookupPositions || writes != 1 || f.fullScans.Load() != 1 {
				t.Errorf("115 HTTP: lists=%d ordinaryDetails=%d sourceIDs=%d sourcePositions=%d writes=%d; Emby fullScans=%d", lists, details, lookupIDs, lookupPositions, writes, f.fullScans.Load())
			}
			t.Logf("115 HTTP：列表=%d，普通详情=%d，来源 ID 反查=%d，来源位置反查=%d，删除 POST=%d，总数=%d；Emby HTTP=%d，完整列表=%d", lists, details, lookupIDs, lookupPositions, writes, lists+details+lookupIDs+lookupPositions+writes, f.totalHTTP.Load(), f.fullScans.Load())
		})
	}
}

var cleanupSourceAccountID atomic.Uint32

// cleanupSourceHTTPTransport 执行真实 115／百度客户端的 HTTP 请求，只读写本地云端夹具。
type cleanupSourceHTTPTransport http.HandlerFunc

func (transport cleanupSourceHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	recorder := httptest.NewRecorder()
	transport(recorder, req)
	response := recorder.Result()
	response.Request = req
	return response, nil
}

func TestWebhookCleanupSourceVerificationCountsBaiduHTTP(t *testing.T) {
	for _, test := range []struct {
		name          string
		knownDisjoint bool
		staleLedger   bool
		remoteOutside bool
		playback      string
		baseURL       string
		duplicateLive bool
		lookupFailure bool
		wantDirectory bool
		wantLookups   int
		wantLists     int
	}{
		{name: "known_disjoint_source_directory", knownDisjoint: true, wantDirectory: true, wantLists: 1},
		{name: "stale_ledger_direct_child", knownDisjoint: true, staleLedger: true, wantLists: 2},
		{name: "unindexed_cross_folder_source_fallback", wantLists: 2},
		{name: "known_qms_source_uses_ledger", knownDisjoint: true, playback: "http://qms/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", wantDirectory: true, wantLists: 1},
		{name: "unindexed_disjoint_qms_source_directory", remoteOutside: true, playback: "http://qms/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", wantDirectory: true, wantLookups: 1, wantLists: 1},
		{name: "configured_base_path_query_uses_generated_origin", remoteOutside: true, baseURL: "http://qms/prefix?token=fixture", playback: "http://qms/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", wantDirectory: true, wantLookups: 1, wantLists: 1},
		{name: "duplicate_qms_source_reuses_lookup", remoteOutside: true, playback: "http://qms/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", duplicateLive: true, wantDirectory: true, wantLookups: 1, wantLists: 1},
		{name: "unindexed_internal_qms_source_fallback", playback: "http://qms/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", wantLists: 2},
		{name: "qms_source_lookup_failure_fallback", playback: "http://qms/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", remoteOutside: true, lookupFailure: true, wantLookups: 1, wantLists: 2},
		{name: "qms_source_without_account_fallback", playback: "http://qms/baidupan/url/video.mkv?pickcode=20", wantLists: 2},
		{name: "unsupported_url_fallback", playback: "http://qms/stream?pickcode=20&userid=source-fixture", wantLists: 2},
		{name: "foreign_qms_url_fallback", playback: "http://untrusted.invalid/baidupan/url/video.mkv?pickcode=20&userid=source-fixture", wantLists: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", true)
			if err := db.Db.AutoMigrate(&models.Settings{}); err != nil {
				t.Fatal(err)
			}
			baseURL := test.baseURL
			if baseURL == "" {
				baseURL = "http://qms"
			}
			settings := models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: baseURL}}
			if err := db.Db.Create(&settings).Error; err != nil {
				t.Fatal(err)
			}
			// 百度客户端按账号缓存；独立 ID 保证每次执行绑定当前 HTTP 替身。
			account := models.Account{BaseModel: models.BaseModel{ID: uint(8_000_000 + cleanupSourceAccountID.Add(1))}, SourceType: models.SourceTypeBaiduPan, UserId: "source-fixture", Token: "fixture"}
			if err := db.Db.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			var root models.SyncPath
			if err := db.Db.First(&root, f.file.SyncPathId).Error; err != nil {
				t.Fatal(err)
			}
			root.SourceType, root.AccountId, root.BaseCid = models.SourceTypeBaiduPan, account.ID, "/movies"
			if err := db.Db.Save(&root).Error; err != nil {
				t.Fatal(err)
			}
			var files []models.SyncFile
			if err := db.Db.Order("id").Find(&files).Error; err != nil {
				t.Fatal(err)
			}
			cloud := map[uint64]*baidupan.FileInfo{
				1:  {FsId: 1, Path: "/movies", ServerFilename: "movies", IsDir: 1},
				2:  {FsId: 2, Path: "/movies/Feature", ServerFilename: "Feature", IsDir: 1},
				30: {FsId: 30, Path: "/movies/Feature/notes.txt", ServerFilename: "notes.txt", Size: 5, ServerMtime: 123},
				31: {FsId: 31, Path: "/movies/Feature/unknown.jpg", ServerFilename: "unknown.jpg", Size: 5, ServerMtime: 123},
			}
			var selected []string
			for i := range files {
				file := &files[i]
				id := uint64(10 + i)
				file.SourceType, file.AccountId = root.SourceType, root.AccountId
				file.FileId, file.ParentId, file.PickCode = path.Join(file.Path, file.FileName), file.Path, strconv.FormatUint(id, 10)
				if err := db.Db.Save(file).Error; err != nil {
					t.Fatal(err)
				}
				if file.ID == f.file.ID {
					f.file = *file
				}
				selected = append(selected, file.FileId)
				cloud[id] = &baidupan.FileInfo{FsId: id, Path: file.FileId, ServerFilename: file.FileName, Size: uint64(file.FileSize), ServerMtime: uint64(file.MTime), Md5: file.Sha1}
			}
			snapshot := workerSnapshot(f.file)
			snapshot.Sources[0].Path = "http://qms/stream?pickcode=" + f.file.PickCode
			if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			f.directory.Root.SourceType, f.directory.Root.AccountID, f.directory.Root.AccountIdentity = root.SourceType, account.ID, models.EmbyAccountIdentity(account)
			f.directory.Root.RootFileID, f.directory.Root.FileID, f.directory.Root.ParentID = root.BaseCid, "2", "1"
			f.directory.Ancestors = []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "1", ParentID: "0", Path: "/movies"}}
			var evidence models.EmbyItemEvidence
			if err := db.Db.Where("item_id = ?", "101").Order("id DESC").First(&evidence).Error; err != nil {
				t.Fatal(err)
			}
			metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil {
				t.Fatal(err)
			}
			metadata.DirectoryScopes = []models.EmbyDirectoryScope{f.directory}
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&evidence).Update("sidecars_json", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}

			liveLocal := filepath.Join(root.GetFullLocalPath(), "Other", "live.strm")
			if err := os.MkdirAll(filepath.Dir(liveLocal), 0700); err != nil {
				t.Fatal(err)
			}
			playback := test.playback
			if playback == "" {
				playback = "http://qms/stream?pickcode=20"
			}
			if err := os.WriteFile(liveLocal, []byte(playback), 0600); err != nil {
				t.Fatal(err)
			}
			livePath := "/movies/Feature/live.mkv"
			if test.knownDisjoint || test.remoteOutside {
				livePath = "/movies/Other/live.mkv"
			}
			if test.knownDisjoint {
				live := models.SyncFile{SourceType: root.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileId: livePath, ParentId: path.Dir(livePath), Path: path.Dir(livePath), FileName: path.Base(livePath), PickCode: "20", IsVideo: true, LocalFilePath: liveLocal}
				if err := db.Db.Create(&live).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.staleLedger {
				livePath = "/movies/Feature/live.mkv"
			}
			cloud[20] = &baidupan.FileInfo{FsId: 20, Path: livePath, ServerFilename: "live.mkv", Size: 250, ServerMtime: 123, Md5: "live-md5"}
			f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "202", Type: "Movie", Path: liveLocal, MediaSources: []embyclientrestgo.MediaSource{{ID: "live-source", ItemID: "202", Path: playback}}}}
			if test.duplicateLive {
				duplicateLocal := filepath.Join(root.GetFullLocalPath(), "Other", "duplicate.strm")
				duplicateURL := playback + "&path=duplicate.mkv&sign=second"
				if err := os.WriteFile(duplicateLocal, []byte(duplicateURL), 0600); err != nil {
					t.Fatal(err)
				}
				f.live = append(f.live, embyclientrestgo.BaseItemDtoV2{Id: "203", Type: "Movie", Path: duplicateLocal, MediaSources: []embyclientrestgo.MediaSource{{ID: "duplicate-source", ItemID: "203", Path: duplicateURL}}})
			}

			originalHTTP := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = originalHTTP })
			lists, details, lookups, writes := 0, 0, 0, 0
			var detailIDs []uint64
			var deleted []string
			http.DefaultClient = &http.Client{Transport: cleanupSourceHTTPTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Query().Get("method") {
				case "list":
					lists++
					if r.URL.Query().Get("dir") != "/movies/Feature" {
						t.Errorf("unexpected cloud listing: %s", r.URL.Query().Get("dir"))
					}
					entries := []*baidupan.FileInfo{}
					for _, file := range cloud {
						if path.Dir(file.Path) == r.URL.Query().Get("dir") {
							entries = append(entries, file)
						}
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries}); err != nil {
						t.Error(err)
					}
				case "filemetas":
					var ids []uint64
					if err := json.Unmarshal([]byte(r.URL.Query().Get("fsids")), &ids); err != nil || len(ids) != 1 {
						t.Errorf("invalid detail IDs: %v, err=%v", ids, err)
					}
					if len(ids) == 1 && ids[0] == 20 {
						lookups++
						if test.lookupFailure {
							fmt.Fprint(w, `{"errno":2}`)
							return
						}
					} else {
						details++
						detailIDs = append(detailIDs, ids...)
					}
					entries := []*baidupan.FileDetail{}
					for _, id := range ids {
						if file, exists := cloud[id]; exists {
							entries = append(entries, &baidupan.FileDetail{FsID: id, Path: file.Path, FileName: file.ServerFilename, IsDir: file.IsDir, Size: file.Size, ServerMtime: file.ServerMtime})
						}
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries}); err != nil {
						t.Error(err)
					}
				case "filemanager":
					writes++
					if r.Method != http.MethodPost || r.FormValue("opera") != "delete" || r.FormValue("async") != "0" {
						t.Errorf("unexpected cloud write: %s %s", r.Method, r.URL.Path)
					}
					var names []string
					if err := json.Unmarshal([]byte(r.FormValue("filelist")), &names); err != nil {
						t.Error(err)
					}
					deleted = append(deleted, names...)
					for _, name := range names {
						for id, file := range cloud {
							if file.Path == name || strings.HasPrefix(file.Path, name+"/") {
								delete(cloud, id)
							}
						}
					}
					fmt.Fprint(w, `{"errno":0}`)
				default:
					t.Errorf("unexpected cloud HTTP request: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			})}
			f.worker.provider = models.NewEmbyDeleteProvider
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			if _, exists := cloud[20]; !exists {
				t.Fatal("live playback source was deleted")
			}
			for _, id := range []uint64{10, 11, 12} {
				if _, exists := cloud[id]; exists {
					t.Errorf("selected file %d was retained", id)
				}
			}
			for _, id := range []uint64{2, 30, 31} {
				if _, exists := cloud[id]; exists == test.wantDirectory {
					t.Errorf("directory or unknown content %d retention=%t, directoryAllowed=%t", id, exists, test.wantDirectory)
				}
			}
			wantDeleted := selected
			wantDetailIDs := []uint64{1, 2, 10, 11, 12}
			if test.wantDirectory {
				wantDeleted = []string{"/movies/Feature"}
				wantDetailIDs = []uint64{1, 1, 2, 2, 2}
			}
			slices.Sort(deleted)
			slices.Sort(wantDeleted)
			slices.Sort(detailIDs)
			if !slices.Equal(deleted, wantDeleted) || !slices.Equal(detailIDs, wantDetailIDs) {
				t.Fatalf("cloud writes=%v want=%v; detail IDs=%v want=%v", deleted, wantDeleted, detailIDs, wantDetailIDs)
			}
			if lists != test.wantLists || details != 5 || lookups != test.wantLookups || writes != 1 || f.fullScans.Load() != 1 {
				t.Fatalf("cloud HTTP: lists=%d ordinaryDetails=%d sourceLookups=%d writes=%d; Emby fullScans=%d", lists, details, lookups, writes, f.fullScans.Load())
			}
			t.Logf("百度 HTTP：列表=%d，普通详情=%d，来源反查=%d，删除 POST=%d，总数=%d；Emby HTTP=%d，完整列表=%d", lists, details, lookups, writes, lists+details+lookups+writes, f.totalHTTP.Load(), f.fullScans.Load())
		})
	}
}
