package emby

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestEmbySourceLookupRequiresConfiguredRouteAndAccount(t *testing.T) {
	for _, test := range []struct {
		name   string
		url    string
		code   string
		source models.SourceType
		want   string
	}{
		{name: "115 generated", url: "https://qms.test/115/url/video.mkv?pickcode=live&userid=user-1", want: "live"},
		{name: "115 legacy", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live&path=/misleading/inside.mkv", want: "live"},
		{name: "configured prefix", url: "http://alias.test/qms/115/url/video.mkv?userid=user-1&pickcode=live", want: "live"},
		{name: "matching aliases", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live&pick_code=live", want: "live"},
		{name: "baidu", source: models.SourceTypeBaiduPan, url: "https://qms.test/baidupan/url/video.mkv?userid=user-1&pickcode=9876", want: "9876"},
		{name: "foreign host", url: "https://other.test/115/newurl?userid=user-1&pickcode=live"},
		{name: "host suffix", url: "https://qms.test.other.test/115/newurl?userid=user-1&pickcode=live"},
		{name: "different port", url: "https://qms.test:12333/115/newurl?userid=user-1&pickcode=live"},
		{name: "different scheme", url: "http://qms.test/115/newurl?userid=user-1&pickcode=live"},
		{name: "unsupported route", url: "https://qms.test/stream?userid=user-1&pickcode=live"},
		{name: "unsupported prefix", url: "https://qms.test/other/115/newurl?userid=user-1&pickcode=live"},
		{name: "generator replaces configured prefix", url: "http://alias.test/115/newurl?userid=user-1&pickcode=live", want: "live"},
		{name: "115 generator replaces base path and query", url: "http://query.test/115/url/video.mkv?userid=user-1&pickcode=live", want: "live"},
		{name: "baidu generator replaces base path and query", source: models.SourceTypeBaiduPan, url: "http://query.test/baidupan/url/video.mkv?userid=user-1&pickcode=9876", want: "9876"},
		{name: "prefix collision", url: "http://alias.test/qms2/115/newurl?userid=user-1&pickcode=live"},
		{name: "encoded route", url: "https://qms.test/%31%31%35/newurl?userid=user-1&pickcode=live"},
		{name: "route traversal", url: "https://qms.test/other/../115/newurl?userid=user-1&pickcode=live"},
		{name: "userinfo", url: "https://user:secret@qms.test/115/newurl?userid=user-1&pickcode=live"},
		{name: "missing account", url: "https://qms.test/115/newurl?pickcode=live"},
		{name: "other account", url: "https://qms.test/115/newurl?userid=user-2&pickcode=live"},
		{name: "duplicate account", url: "https://qms.test/115/newurl?userid=user-1&userid=user-1&pickcode=live"},
		{name: "duplicate object", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live&pickcode=other"},
		{name: "conflicting aliases", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live&pick_code=other"},
		{name: "unsupported parameter alias alone", url: "https://qms.test/115/newurl?userid=user-1&pick_code=live"},
		{name: "conflicting snapshot", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live", code: "other"},
		{name: "whole URL is not object ID", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live", code: "https://qms.test/115/newurl?userid=user-1&pickcode=live"},
		{name: "invalid query", url: "https://qms.test/115/newurl?userid=user-1&pickcode=live&bad=%xx"},
		{name: "path-valued object", url: "https://qms.test/115/newurl?userid=user-1&pickcode=/cloud/live.mkv"},
		{name: "baidu invalid ID", source: models.SourceTypeBaiduPan, url: "https://qms.test/baidupan/url/video.mkv?userid=user-1&pickcode=live"},
		{name: "baidu zero ID", source: models.SourceTypeBaiduPan, url: "https://qms.test/baidupan/url/video.mkv?userid=user-1&pickcode=0"},
		{name: "baidu overflowing ID", source: models.SourceTypeBaiduPan, url: "https://qms.test/baidupan/url/video.mkv?userid=user-1&pickcode=9223372036854775808"},
		{name: "baidu unknown route", source: models.SourceTypeBaiduPan, url: "https://qms.test/baidupan/newurl?userid=user-1&pickcode=9876"},
		{name: "other provider", source: models.SourceTypeBaiduPan, url: "https://qms.test/115/newurl?userid=user-1&pickcode=9876"},
		{name: "OpenList excluded", source: models.SourceTypeOpenList, url: "https://qms.test/115/newurl?userid=user-1&pickcode=live"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourceType := test.source
			if sourceType == "" {
				sourceType = models.SourceType115
			}
			account := models.Account{SourceType: sourceType, UserId: "user-1"}
			got, ok := embySourceLookupCode(models.EmbySnapshotSource{Path: test.url, PickCode: test.code}, account, []string{"https://qms.test", "http://alias.test/qms", "http://query.test/prefix?ignored=1"})
			if ok != (test.want != "") || got != test.want {
				t.Fatalf("code=%q allowed=%t, want=%q", got, ok, test.want)
			}
		})
	}
}

func TestEmby115SourceLocationRequiresMatchingObjectAndAncestry(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*v115open.DownloadUrlResult, *v115open.FileDetail)
		valid  bool
	}{
		{name: "complete source", valid: true},
		{name: "file moved but identity retained", valid: true, mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) {
			d.Paths[2].Name = "Elsewhere"
			d.Path = "cloud/Elsewhere"
		}},
		{name: "wrong ID", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.FileId = "92" }},
		{name: "wrong pickcode", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.PickCode = "replacement" }},
		{name: "wrong download pickcode", mutate: func(d *v115open.DownloadUrlResult, _ *v115open.FileDetail) { d.PickCode = "replacement" }},
		{name: "directory", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.FileCategory = v115open.TypeDir }},
		{name: "missing parents", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Paths = nil }},
		{name: "partial parent chain", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Paths = d.Paths[1:] }},
		{name: "parent cycle", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Paths[2].FileId = d.Paths[1].FileId }},
		{name: "object is parent", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Paths[2].FileId = d.FileId }},
		{name: "parent lacks ID", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Paths[2].FileId = "" }},
		{name: "parent traversal", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Paths[2].Name = ".." }},
		{name: "path contradicts parents", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.Path = "cloud/Inside" }},
		{name: "filename contains separator", mutate: func(_ *v115open.DownloadUrlResult, d *v115open.FileDetail) { d.FileName = "other/live.mkv" }},
		{name: "download name mismatch", mutate: func(d *v115open.DownloadUrlResult, _ *v115open.FileDetail) { d.FileName = "different.mkv" }},
		{name: "download hash mismatch", mutate: func(d *v115open.DownloadUrlResult, _ *v115open.FileDetail) { d.Sha1 = "replacement" }},
		{name: "download size mismatch", mutate: func(d *v115open.DownloadUrlResult, _ *v115open.FileDetail) { d.FileSize = "999" }},
		{name: "download size malformed", mutate: func(d *v115open.DownloadUrlResult, _ *v115open.FileDetail) { d.FileSize = "unknown" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			download := &v115open.DownloadUrlResult{FileID: "91", FileName: "live.mkv", PickCode: "live", Sha1: "sha1"}
			detail := &v115open.FileDetail{FileId: "91", FileName: "live.mkv", PickCode: "live", FileCategory: v115open.TypeFile, Sha1: "sha1", Path: "cloud/Other", Paths: []v115open.FileDetailPath{{FileId: "0"}, {FileId: "7", Name: "cloud"}, {FileId: "8", Name: "Other"}}}
			if test.mutate != nil {
				test.mutate(download, detail)
			}
			location, err := emby115SourceLocation("live", download, detail)
			if (err == nil) != test.valid || test.valid && location != "/"+detail.Path+"/live.mkv" {
				t.Fatalf("location=%q err=%v, want valid=%t", location, err, test.valid)
			}
		})
	}
	if _, err := emby115SourceLocation("live", nil, nil); err == nil {
		t.Fatal("nil detail accepted")
	}
}

func TestEmbyBaiduSourceLocationRequiresMatchingObjectAndFullPath(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*baidupan.FileDetail)
		valid  bool
	}{
		{name: "complete source", valid: true},
		{name: "wrong fsid", mutate: func(d *baidupan.FileDetail) { d.FsID = 22 }},
		{name: "directory", mutate: func(d *baidupan.FileDetail) { d.IsDir = 1 }},
		{name: "missing path", mutate: func(d *baidupan.FileDetail) { d.Path = "" }},
		{name: "relative path", mutate: func(d *baidupan.FileDetail) { d.Path = "cloud/Other/live.mkv" }},
		{name: "path traversal", mutate: func(d *baidupan.FileDetail) { d.Path = "/cloud/../Inside/live.mkv" }},
		{name: "unclean path", mutate: func(d *baidupan.FileDetail) { d.Path = "/cloud//Other/live.mkv" }},
		{name: "name differs", mutate: func(d *baidupan.FileDetail) { d.FileName = "replacement.mkv" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail := &baidupan.FileDetail{FsID: 21, Path: "/cloud/Other/live.mkv", FileName: "live.mkv"}
			if test.mutate != nil {
				test.mutate(detail)
			}
			location, err := embyBaiduSourceLocation("21", detail)
			if (err == nil) != test.valid || test.valid && location != detail.Path {
				t.Fatalf("location=%q err=%v, want valid=%t", location, err, test.valid)
			}
		})
	}
	if _, err := embyBaiduSourceLocation("21", nil); err == nil {
		t.Fatal("nil detail accepted")
	}
}

func sourceLookupTestContext(t *testing.T) (context.Context, *embyDeletionVerificationState, *atomic.Int64) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixNano())
	cache := &embyDeletionVerificationState{loaded: true, now: func() time.Time { return time.Unix(0, clock.Load()) }}
	cache.completedAt = cache.now()
	return context.WithValue(t.Context(), embyDeletionVerificationKey{}, cache), cache, clock
}

func TestEmbySourceLookupCacheKeepsOriginalReadTimeAndIdentity(t *testing.T) {
	ctx, cache, clock := sourceLookupTestContext(t)
	key := embySourceLocationKey{models.SourceType115, 1, "principal", "live"}
	calls := 0
	load := func(context.Context) (string, error) { calls++; return "/cloud/Other/live.mkv", nil }
	first, err := cachedEmbySourceLocation(ctx, key, load)
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(29 * time.Second))
	second, err := cachedEmbySourceLocation(ctx, key, load)
	if err != nil || second.readAt != first.readAt || calls != 1 {
		t.Fatalf("cache renewed or reread: calls=%d err=%v", calls, err)
	}
	otherAccount := key
	otherAccount.account = 2
	if _, err := cachedEmbySourceLocation(ctx, otherAccount, load); err != nil || calls != 2 {
		t.Fatalf("accounts shared a lookup: calls=%d err=%v", calls, err)
	}
	otherIdentity := key
	otherIdentity.identity = "replacement"
	if _, err := cachedEmbySourceLocation(ctx, otherIdentity, load); err != nil || calls != 3 {
		t.Fatalf("principals shared a lookup: calls=%d err=%v", calls, err)
	}
	clock.Add(int64(time.Second))
	if _, err := cachedEmbySourceLocation(ctx, key, load); !errors.Is(err, models.ErrEmbySnapshotStale) || calls != 3 {
		t.Fatalf("expired snapshot used: calls=%d err=%v", calls, err)
	}
	cache.completedAt = cache.now()
	if _, err := cachedEmbySourceLocation(ctx, key, load); err != nil || calls != 4 {
		t.Fatalf("new snapshot failed to refresh source: calls=%d err=%v", calls, err)
	}
}

func TestEmbySourceLookupCacheRetainsFailureWithinAttempt(t *testing.T) {
	ctx, cache, clock := sourceLookupTestContext(t)
	key := embySourceLocationKey{models.SourceTypeBaiduPan, 1, "principal", "21"}
	calls := 0
	load := func(context.Context) (string, error) { calls++; return "", errors.New("request secret=do-not-cache") }
	for range 2 {
		if _, err := cachedEmbySourceLocation(ctx, key, load); !errors.Is(err, models.ErrEmbyDeleteUnverified) {
			t.Fatalf("unsafe or missing error: %v", err)
		}
		clock.Add(int64(time.Minute))
		cache.completedAt = cache.now()
	}
	if calls != 1 {
		t.Fatalf("failure retried for another directory: %d", calls)
	}
	newCtx, _, _ := sourceLookupTestContext(t)
	_, _ = cachedEmbySourceLocation(newCtx, key, load)
	if calls != 2 {
		t.Fatal("new attempt retained previous failure")
	}
}

func TestEmbySourceLookupCacheSanitizesWrappedErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		want  error
	}{
		{name: "cancelled", cause: context.Canceled, want: context.Canceled},
		{name: "timeout", cause: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "stale", cause: models.ErrEmbySnapshotStale, want: models.ErrEmbySnapshotStale},
		{name: "transport", cause: errors.New("transport failed"), want: models.ErrEmbyDeleteUnverified},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _, _ := sourceLookupTestContext(t)
			key := embySourceLocationKey{models.SourceTypeBaiduPan, 1, "principal", "21"}
			calls := 0
			load := func(context.Context) (string, error) {
				calls++
				return "", &url.Error{Op: "GET", URL: "https://cloud.test/file?access_token=private-token", Err: fmt.Errorf("request: %w", test.cause)}
			}
			for range 2 {
				read, err := cachedEmbySourceLocation(ctx, key, load)
				if err != test.want || read == nil || read.err != test.want {
					t.Fatalf("returned or cached a wrapped URL error: err=%v read=%+v", err, read)
				}
			}
			if calls != 1 {
				t.Fatalf("cached failure repeated lookup: %d", calls)
			}
		})
	}
}

func TestEmbySourceLookupRejectsLongReadAndReleasedContext(t *testing.T) {
	ctx, _, clock := sourceLookupTestContext(t)
	key := embySourceLocationKey{models.SourceType115, 1, "principal", "live"}
	if _, err := cachedEmbySourceLocation(ctx, key, func(context.Context) (string, error) {
		clock.Add(int64(embyDeletionVerificationWindow))
		return "/cloud/Other/live.mkv", nil
	}); !errors.Is(err, models.ErrEmbySnapshotStale) {
		t.Fatalf("long lookup authorized an expired snapshot: %v", err)
	}
	released, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cachedEmbySourceLocation(released, key, func(context.Context) (string, error) {
		t.Fatal("released execution performed I/O")
		return "", nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("released context accepted: %v", err)
	}
}

func TestEmbySourceLookupDeduplicatesInFlightReads(t *testing.T) {
	ctx, _, _ := sourceLookupTestContext(t)
	key := embySourceLocationKey{models.SourceType115, 1, "principal", "live"}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := cachedEmbySourceLocation(ctx, key, func(readCtx context.Context) (string, error) {
			close(started)
			select {
			case <-release:
				return "/cloud/Other/live.mkv", nil
			case <-readCtx.Done():
				return "", readCtx.Err()
			}
		})
		done <- err
	}()
	<-started
	sharedDone := make(chan error, 1)
	go func() {
		_, err := cachedEmbySourceLocation(ctx, key, func(context.Context) (string, error) {
			t.Error("in-flight lookup repeated")
			return "", nil
		})
		sharedDone <- err
	}()
	waiter, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cachedEmbySourceLocation(waiter, key, func(context.Context) (string, error) { t.Error("duplicate lookup"); return "", nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter failed: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-sharedDone; err != nil {
		t.Fatal(err)
	}
	if _, err := cachedEmbySourceLocation(ctx, key, func(context.Context) (string, error) { t.Error("completed lookup repeated"); return "", nil }); err != nil {
		t.Fatal(err)
	}
}

func TestEmbySourceLookupRechecksAccountAndConfiguration(t *testing.T) {
	for _, change := range []string{"account", "base URL", "expiry"} {
		t.Run(change, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			if err := f.conn.AutoMigrate(&models.Account{}, &models.Settings{}, &models.SyncPath{}); err != nil {
				t.Fatal(err)
			}
			account := models.Account{SourceType: models.SourceType115, UserId: "user-1"}
			if err := f.conn.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.conn.Create(&models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: "https://qms.test"}}).Error; err != nil {
				t.Fatal(err)
			}
			root := models.EmbyFrozenFile{SourceType: account.SourceType, AccountID: account.ID, AccountIdentity: models.EmbyAccountIdentity(account)}
			ctx, cache, clock := sourceLookupTestContext(t)
			read := &embySourceLocationRead{readAt: cache.now(), snapshotAt: cache.completedAt, path: "/cloud/Other/live.mkv"}
			uses := []embySourceLocationUse{{read: read, source: models.EmbySnapshotSource{Path: "https://qms.test/115/newurl?pickcode=live&userid=user-1", PickCode: "live"}}}
			if err := verifyEmbySourceLocationReads(ctx, root, uses); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "account":
				if err := f.conn.Model(&account).UpdateColumn("user_id", "replacement").Error; err != nil {
					t.Fatal(err)
				}
			case "base URL":
				if err := f.conn.Model(&models.Settings{}).Where("id > 0").UpdateColumn("strm_base_url", "https://replacement.test").Error; err != nil {
					t.Fatal(err)
				}
			case "expiry":
				clock.Add(int64(embyDeletionVerificationWindow))
			}
			if err := verifyEmbySourceLocationReads(ctx, root, uses); !errors.Is(err, models.ErrEmbySnapshotStale) {
				t.Fatalf("changed %s still authorized directory: %v", change, err)
			}
		})
	}
}
