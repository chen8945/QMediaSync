package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

func TestEmbyDeleteProviderConcurrentReads(t *testing.T) {
	for _, operation := range []string{"stat", "list"} {
		for _, outcome := range []string{"success", "failed read", "cancel waiter", "write during read", "release", "final guard"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, release := beginEmbyDirectoryChecks(t.Context())
					defer release()
					file := embyProviderTestFile(SourceType115)
					blocked := make(chan struct{})
					var reads atomic.Int64
					read := func() error {
						reads.Add(1)
						<-blocked
						if outcome == "failed read" {
							return errors.New("unavailable")
						}
						return nil
					}
					client := &embyDeleteStatCache115Client{embyDelete115Stub: &embyDelete115Stub{list: func(int) (*v115open.FileListResp, error) {
						return embyProviderBatchList115([]EmbyFrozenFile{file}), read()
					}}, read: func(context.Context, string) (*v115open.FileDetail, error) { return embyStatCacheDetail(file), read() }}
					provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, v115: client}
					call := func(ctx context.Context) error {
						if operation == "stat" {
							_, err := provider.Stat(ctx, file)
							return err
						}
						_, err := provider.List(ctx, file)
						return err
					}
					first := make(chan error, 1)
					go func() { first <- call(ctx) }()
					synctest.Wait()
					waiting, cancel := context.WithCancel(ctx)
					defer cancel()
					results := make(chan error, 8)
					for range cap(results) {
						go func() { results <- call(waiting) }()
					}
					synctest.Wait()
					if reads.Load() != 1 {
						t.Fatalf("same object issued %d concurrent reads", reads.Load())
					}
					switch outcome {
					case "cancel waiter":
						cancel()
					case "write during read":
						embyInvalidateDeletionListings(ctx, file, false)
					case "release":
						release()
					case "final guard":
						if err := call(context.WithValue(ctx, embyDeletionFinalGuardKey{}, true)); !errors.Is(err, ErrEmbyDeleteUnverified) {
							t.Fatalf("pending read used during final send: %v", err)
						}
					}
					synctest.Wait()
					close(blocked)
					wantFailure := outcome == "failed read" || outcome == "write during read" || outcome == "release"
					if err := <-first; (err != nil) != wantFailure {
						t.Fatalf("first read=%v", err)
					}
					for range cap(results) {
						err := <-results
						if (err != nil) != (wantFailure || outcome == "cancel waiter") {
							t.Fatalf("shared read=%v", err)
						}
						if outcome == "cancel waiter" && !errors.Is(err, context.Canceled) {
							t.Fatalf("waiter did not preserve cancellation: %v", err)
						}
					}
					if reads.Load() != 1 {
						t.Fatalf("waiters repeated read: %d", reads.Load())
					}
				})
			})
		}
	}
}

func TestEmbyDeleteProviderExternal115StableID(t *testing.T) {
	for _, mode := range []string{"complete", "mtime without sha1", "zero size at root", "wrong ID", "wrong pickcode", "missing root", "duplicate ancestor", "self ancestor", "invalid ancestor name", "conflicting path", "missing fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			ctx, release := beginEmbyDirectoryChecks(t.Context())
			defer release()
			file := embyProviderTestFile(SourceType115)
			file.PickCode = "selected-pickcode"
			detail := embyStatCacheDetail(file)
			detail.PickCode, detail.Ptime = file.PickCode, "1600000000"
			detail.Paths = []v115open.FileDetailPath{{FileId: "0"}, {FileId: file.ParentID, Name: "media"}}
			switch mode {
			case "mtime without sha1":
				detail.Sha1 = ""
			case "zero size at root":
				detail.Path, detail.FileSizeByte, detail.Paths = "", 0, detail.Paths[:1]
			case "wrong ID":
				detail.FileId = "11"
			case "wrong pickcode":
				detail.PickCode = "replacement"
			case "missing root":
				detail.Paths = detail.Paths[1:]
			case "duplicate ancestor":
				detail.Paths = append(detail.Paths, detail.Paths[1])
			case "self ancestor":
				detail.Paths[1].FileId = file.FileID
			case "invalid ancestor name":
				detail.Paths[1].Name = "../media"
			case "conflicting path":
				detail.Path = "other"
			case "missing fingerprint":
				detail.Sha1, detail.Utime, detail.Ptime = "", "", ""
			}
			reads := 0
			provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity,
				v115: &embyDeleteStatCache115Client{read: func(_ context.Context, id string) (*v115open.FileDetail, error) {
					reads++
					if id != file.FileID {
						t.Fatalf("detail requested %q", id)
					}
					return detail, nil
				}}}
			minimal := EmbyFrozenFile{SourceType: file.SourceType, AccountID: file.AccountID, AccountIdentity: file.AccountIdentity, FileID: file.FileID, PickCode: file.PickCode, ExternalSource: true}
			before := time.Now()
			remote, err := provider.Stat(ctx, minimal)
			valid := mode == "complete" || mode == "mtime without sha1" || mode == "zero size at root"
			if (err == nil) != valid || reads != 1 {
				t.Fatalf("remote=%+v reads=%d err=%v", remote, reads, err)
			}
			if !valid {
				return
			}
			source := EmbySnapshotSource{SourceType: file.SourceType, AccountID: file.AccountID, AccountIdentity: file.AccountIdentity, ObjectKind: "pickcode", ObjectID: file.PickCode}
			cached, at, found, err := LookupEmbyDeletionSource(ctx, source)
			if err != nil || !found || cached != remote || at.Before(before) || at.After(time.Now()) || cached.FileCreatedAt != 1600000000 {
				t.Fatalf("cached=%+v at=%v found=%v err=%v", cached, at, found, err)
			}
			if _, err := provider.Stat(ctx, minimal); err != nil || reads != 1 {
				t.Fatalf("identity queried again: reads=%d err=%v", reads, err)
			}
		})
	}
}

func TestEmbyDeleteProviderSourceListingUniqueIdentity(t *testing.T) {
	for _, mode := range []string{"complete", "different principal", "duplicate pickcode", "incomplete", "write", "expired"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, release := beginEmbyDirectoryChecks(t.Context())
				defer release()
				files := embyProviderBatchFiles(SourceType115)
				for i := range files {
					files[i].PickCode = "pc" + strconv.Itoa(i)
				}
				lists := 0
				client := &embyDelete115Stub{list: func(int) (*v115open.FileListResp, error) {
					lists++
					response := embyProviderBatchList115(files)
					for i := range files {
						response.Data[i].PickCode = files[i].PickCode
					}
					if mode == "duplicate pickcode" {
						response.Data[2].PickCode = files[1].PickCode
					}
					if mode == "incomplete" {
						response.Data[1].Sha1, response.Data[1].Utime = "", 0
					}
					return response, nil
				}}
				file := files[0]
				provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, v115: client}
				at := time.Now()
				if _, err := provider.List(ctx, file); err != nil {
					t.Fatal(err)
				}
				source := EmbySnapshotSource{SourceType: file.SourceType, AccountID: file.AccountID, AccountIdentity: file.AccountIdentity, ObjectKind: "pickcode", ObjectID: files[1].PickCode}
				switch mode {
				case "different principal":
					source.AccountIdentity = "other"
				case "write":
					embyInvalidateDeletionListings(ctx, file, false)
				case "expired":
					time.Sleep(embyDeletePreflightValidity)
				}
				remote, readAt, found, err := LookupEmbyDeletionSource(ctx, source)
				if found != (mode == "complete") || lists != 1 {
					t.Fatalf("remote=%+v found=%v lists=%d err=%v", remote, found, lists, err)
				}
				if mode == "duplicate pickcode" && !errors.Is(err, ErrEmbyIdentityAmbiguous) {
					t.Fatalf("ambiguous pickcode accepted: %v", err)
				}
				if found && (remote.FileID != files[1].FileID || !readAt.Equal(at)) {
					t.Fatalf("wrong identity/read time: remote=%+v at=%v", remote, readAt)
				}
			})
		})
	}
}

func TestEmbyDeleteProviderExternalBaiduSharesHTTPDetail(t *testing.T) {
	embyProviderTestLogger(t)
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	details := 0
	http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("method") != "filemetas" || r.URL.Query().Get("fsids") != "[10]" {
			t.Errorf("unexpected request method=%s fsids=%s", r.URL.Query().Get("method"), r.URL.Query().Get("fsids"))
		}
		details++
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []baidupan.FileDetail{{FsID: 10, FileName: "Movie.mkv", Path: "/media/Movie.mkv", ServerMtime: 1700000000}}})
	})}
	ctx, release := beginEmbyDirectoryChecks(t.Context())
	defer release()
	file := EmbyFrozenFile{SourceType: SourceTypeBaiduPan, AccountID: 1, AccountIdentity: "owner", FileID: "10", ExternalSource: true}
	provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, baidu: baidupan.NewBaiDuPanClientWithToken("test")}
	for range 2 {
		remote, err := provider.Stat(ctx, file)
		if err != nil || remote.FileID != "10" || remote.ParentID != "/media" || remote.MTime != 1700000000 || remote.FileSize != 0 {
			t.Fatalf("remote=%+v err=%v", remote, err)
		}
	}
	if details != 1 {
		t.Fatalf("details=%d", details)
	}
}

func TestEmbyDeleteProviderOpenListSharesVerifiedHTTPReads(t *testing.T) {
	embyProviderTestLogger(t)
	for _, conflict := range []bool{false, true} {
		t.Run(strconv.FormatBool(conflict), func(t *testing.T) {
			file := embyProviderTestFile(SourceTypeOpenList)
			var lists, details, writes atomic.Int64
			entry := map[string]any{"name": file.FileName, "size": file.FileSize, "id": file.OpenlistObjectID, "modified": time.Unix(file.MTime, 0).UTC().Format(time.RFC3339), "hash_info": map[string]string{"sha1": file.OpenlistSHA1}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/fs/list":
					lists.Add(1)
					entries := []any{entry}
					if writes.Load() != 0 {
						entries = nil
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": entries, "total": len(entries)}})
				case "/api/fs/get":
					details.Add(1)
					observed := entry
					if conflict {
						observed = map[string]any{"name": file.FileName, "size": file.FileSize, "id": "replacement"}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": observed})
				case "/api/fs/remove":
					writes.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client := openlist.NewTemporaryClient(server.URL, "user", "password", "token")
			t.Cleanup(func() { _ = client.Close() })
			provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, openlist: client}
			ctx, release := beginEmbyDirectoryChecks(t.Context())
			defer release()
			if _, err := provider.Stat(ctx, file); (err != nil) != conflict {
				t.Fatalf("conflict=%v err=%v", conflict, err)
			}
			if conflict {
				if writes.Load() != 0 || details.Load() != 1 || lists.Load() != 1 {
					t.Fatal("failed get/list validation was not retained")
				}
				return
			}
			if ok, err := provider.DeleteBatch(ctx, []EmbyFrozenFile{file}, func() error { return nil }); !ok || err != nil {
				t.Fatalf("delete=%v err=%v", ok, err)
			}
			if lists.Load() != 1 || details.Load() != 1 || writes.Load() != 1 {
				t.Fatalf("lists=%d details=%d writes=%d", lists.Load(), details.Load(), writes.Load())
			}
			if _, err := provider.Stat(ctx, file); !errors.Is(err, ErrEmbyRemoteFileAbsent) || lists.Load() != 2 || details.Load() != 1 {
				t.Fatalf("write confirmation reused old observation: lists=%d details=%d err=%v", lists.Load(), details.Load(), err)
			}
		})
	}
}
