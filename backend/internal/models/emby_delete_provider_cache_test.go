package models

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
)

func TestEmbyDeleteProviderListingAttemptScope(t *testing.T) {
	for _, mode := range []string{"expiry", "release", "configuration", "failed write", "directory write", "caller mutation"} {
		t.Run(mode, func(t *testing.T) {
			files := embyProviderBatchFiles(SourceType115)
			reads := 0
			stub := &embyDelete115Stub{list: func(int) (*v115open.FileListResp, error) {
				reads++
				return embyProviderBatchList115(files), nil
			}, delete: func([]string, string) (bool, error) { return false, errors.New("ambiguous partial write") }}
			provider := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: stub}
			ctx, release, err := BeginEmbyDeletionExecution(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if release != nil {
					release()
				}
			}()
			first, err := provider.List(ctx, files[0])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.List(ctx, files[1]); err != nil || reads != 1 {
				t.Fatalf("same parent repeated: reads=%d err=%v", reads, err)
			}
			checks := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
			switch mode {
			case "expiry":
				checks.mu.Lock()
				entry := checks.listings[embyDeletionListKey(files[0])]
				entry.at = time.Now().Add(-embyDeletePreflightValidity)
				checks.listings[embyDeletionListKey(files[0])] = entry
				checks.mu.Unlock()
			case "release":
				release()
				release = nil
			case "configuration":
				end := BeginSyncPositionMutation()
				end()
			case "failed write":
				if ok, err := provider.DeleteBatch(ctx, files, func() error { return nil }); ok || err == nil {
					t.Fatal("expected ambiguous result")
				}
			case "directory write":
				root := files[0]
				root.Path, root.FileName = "/", "media"
				embyInvalidateDeletionListings(ctx, root, true)
			case "caller mutation":
				first[0].FileName = "altered"
			}
			after, err := provider.List(ctx, files[0])
			if mode == "release" {
				if !errors.Is(err, context.Canceled) || reads != 1 {
					t.Fatalf("released reads=%d err=%v", reads, err)
				}
				return
			}
			wantReads := 2
			if mode == "caller mutation" {
				wantReads = 1
			}
			if err != nil || reads != wantReads || after[0].FileName != files[0].FileName {
				t.Fatalf("reads=%d files=%+v err=%v", reads, after, err)
			}
		})
	}
}

func TestEmbyDeleteProviderBaiduLedgerBatchComposition(t *testing.T) {
	embyProviderTestLogger(t)
	previousDB := db.Db
	t.Cleanup(func() { db.Db = previousDB })
	config, token, video := setupEmbySnapshotModelTest(t)
	if err := db.Db.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&Account{}).Where("id = ?", video.AccountId).Update("source_type", SourceTypeBaiduPan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", video.SyncPathId).Updates(map[string]any{"source_type": SourceTypeBaiduPan, "base_cid": "/movies"}).Error; err != nil {
		t.Fatal(err)
	}
	video.SourceType, video.FileId, video.ParentId, video.PickCode = SourceTypeBaiduPan, "/movies/movie.mkv", "/movies", "10"
	if err := db.Db.Save(&video).Error; err != nil {
		t.Fatal(err)
	}
	files := []SyncFile{video}
	for i, name := range []string{"movie.nfo", "movie-poster.jpg"} {
		file := video
		file.BaseModel = BaseModel{}
		file.FileId, file.FileName, file.PickCode = path.Join(file.Path, name), name, strconv.Itoa(11+i)
		file.IsVideo, file.IsMeta = false, true
		file.LocalFilePath = path.Join(path.Dir(video.LocalFilePath), name)
		if err := db.Db.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(video)}); err != nil {
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
	input.CleanupPolicy = CurrentEmbyCleanupPolicy()
	if len(input.Owners) != 1 || len(input.Sidecars) != 2 {
		t.Fatalf("input=%+v", input)
	}
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	reads, beforeWriteReads, details, writes := 0, 0, 0, 0
	http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("method") {
		case "list":
			reads++
			if writes == 0 {
				beforeWriteReads++
			}
			entries := []*baidupan.FileInfo{}
			if writes == 0 {
				for _, f := range files {
					id, _ := strconv.ParseUint(f.PickCode, 10, 64)
					entries = append(entries, &baidupan.FileInfo{FsId: id, Path: f.FileId, ServerFilename: f.FileName, Size: uint64(f.FileSize), ServerMtime: uint64(f.MTime), Md5: f.Sha1})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries})
		case "filemanager":
			writes++
			var names []string
			if err := json.Unmarshal([]byte(r.FormValue("filelist")), &names); err != nil {
				t.Error(err)
			}
			if len(names) != 3 || r.FormValue("async") != "0" {
				t.Errorf("wrong batch %v", names)
			}
			_, _ = io.WriteString(w, `{"errno":0}`)
		case "filemetas":
			details++
			var ids []int64
			if err := json.Unmarshal([]byte(r.URL.Query().Get("fsids")), &ids); err != nil || len(ids) != 1 || ids[0] < 10 || ids[0] > 12 {
				t.Errorf("path sent instead of fsid: %v err=%v", ids, err)
			}
			_, _ = io.WriteString(w, `{"errno":0,"list":[]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})}
	frozen := input.Owners[0].Files[0]
	provider := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: frozen.AccountID, accountIdentity: frozen.AccountIdentity, baidu: baidupan.NewBaiDuPanClientWithToken("test")}
	ctx, release, err := BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	plan, err := BuildEmbyDeletionPlan(ctx, input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, allowEmbyDeleteTest)
	if err != nil || len(plan.Targets) != 3 || len(plan.Issues) != 0 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	results := ExecuteEmbyDeletionBatch(ctx, plan, plan.Targets, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
	for _, result := range results {
		if result.Outcome != EmbyDeletionDeleted {
			t.Fatalf("result=%+v", result)
		}
	}
	if len(results) != 3 || beforeWriteReads != 1 || reads != 2 || writes != 1 || details != 3 {
		t.Fatalf("results=%+v preList=%d allList=%d write=%d confirmation=%d", results, beforeWriteReads, reads, writes, details)
	}
	if plan.Targets[0].File.FileID != video.FileId || plan.Targets[0].Key != EmbyDeletionFileKey(frozen) {
		t.Fatal("persisted ledger identity rewritten")
	}
}

func TestEmbyDeleteProviderPhysicalBaiduIdentity(t *testing.T) {
	for _, tc := range []struct{ name, fileID, pickCode, want string }{
		{"real ledger", "/media/Movie.mkv", "10", "10"},
		{"legacy numeric", "10", "", "10"},
		{"contradictory numeric", "10", "11", ""},
		{"wrong path", "/elsewhere/Movie.mkv", "10", ""},
		{"missing fsid", "/media/Movie.mkv", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := EmbyFrozenFile{SourceType: SourceTypeBaiduPan, FileID: tc.fileID, PickCode: tc.pickCode, Path: "/media", FileName: "Movie.mkv"}
			if got := embyFrozenPhysicalID(file); got != tc.want {
				t.Fatalf("ID=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestEmbyDeleteProviderBaiduMissingParentChecksOriginalID(t *testing.T) {
	embyProviderTestLogger(t)
	for _, mode := range []string{"absent", "moved", "failed detail"} {
		t.Run(mode, func(t *testing.T) {
			original := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = original })
			lists, details := 0, 0
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Query().Get("method") {
				case "list":
					lists++
					_, _ = io.WriteString(w, `{"errno":-9}`)
				case "filemetas":
					details++
					if r.URL.Query().Get("fsids") != "[10]" {
						t.Error("original fsid not used")
					}
					switch mode {
					case "absent":
						_, _ = io.WriteString(w, `{"errno":0,"list":[]}`)
					case "moved":
						_, _ = io.WriteString(w, `{"errno":0,"list":[{"fs_id":10,"path":"/elsewhere/Movie.mkv","filename":"Movie.mkv","isdir":0}]}`)
					default:
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"errno":-1}`)
					}
				default:
					t.Errorf("unexpected write/request %s", r.URL.Path)
				}
			})}
			file := embyProviderTestFile(SourceTypeBaiduPan)
			file.FileName, file.FileID, file.PickCode, file.ParentID = "Movie.mkv", "/media/Movie.mkv", "10", "/media"
			provider := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: 1, accountIdentity: "owner", baidu: baidupan.NewBaiDuPanClientWithToken("test")}
			_, err := provider.Stat(t.Context(), file)
			if lists != 1 || details != 1 || err == nil || errors.Is(err, ErrEmbyRemoteFileAbsent) != (mode == "absent") {
				t.Fatalf("lists=%d details=%d err=%v", lists, details, err)
			}
		})
	}
}
