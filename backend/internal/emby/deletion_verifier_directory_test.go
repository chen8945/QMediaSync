package emby

import (
	"context"
	"errors"
	"path"
	"testing"

	"qmediasync/internal/models"
)

func TestEmbyDirectoryLiveRetentionUsesDirectPhysicalIdentity(t *testing.T) {
	for _, test := range []struct {
		name        string
		source      models.SourceType
		ledger      bool
		childDir    bool
		otherID     bool
		wantAllowed bool
	}{
		{name: "115 stale ledger stable ID", source: models.SourceType115, ledger: true},
		{name: "115 missing local association", source: models.SourceType115},
		{name: "Baidu stale path ledger uses fsid", source: models.SourceTypeBaiduPan, ledger: true},
		{name: "Baidu missing local association", source: models.SourceTypeBaiduPan},
		{name: "other account identity cannot match", source: models.SourceType115, ledger: true, otherID: true, wantAllowed: true},
		{name: "child directory is not recursively inspected", source: models.SourceType115, ledger: true, childDir: true, wantAllowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			scope := models.EmbyDirectoryScope{LocalPath: "/media/Show", Root: models.EmbyFrozenFile{SourceType: test.source, AccountID: 1, AccountIdentity: "owner", FileID: "2", Path: "/cloud", FileName: "Show"}}
			child := models.EmbyRemoteFile{FileID: "20", ParentID: "2", Path: "/cloud/Show", FileName: "renamed.mkv", IsDir: test.childDir}
			if !test.ledger && test.source == models.SourceType115 {
				child.PickCode = "live"
			}
			code := "live"
			if test.source == models.SourceTypeBaiduPan {
				code = "20"
			}
			if test.ledger {
				file := models.SyncFile{SourceType: test.source, AccountId: 1, FileId: "20", Path: "/cloud/Old", FileName: "old.mkv", PickCode: code}
				if test.source == models.SourceTypeBaiduPan {
					file.FileId = path.Join(file.Path, file.FileName)
				}
				if test.otherID {
					foreign := file
					foreign.AccountId = 2
					if err := f.conn.Create(&foreign).Error; err != nil {
						t.Fatal(err)
					}
					file.FileId = "30"
				}
				if err := f.conn.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			provider := &cleanupWorkerProvider{webhookTestProvider: &webhookTestProvider{files: map[string]models.EmbyRemoteFile{child.FileID: child}}}
			ctx := models.WithEmbyDirectoryProvider(t.Context(), provider)
			snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "9", Path: "/media/Other/live.strm"}, Sources: []models.EmbySnapshotSource{{Path: "http://qms/stream?pickcode=" + code, PickCode: code}}}
			err := verifyEmbyDirectoryLiveItems(ctx, scope, []models.EmbyItemSnapshot{snapshot})
			if (err == nil) != test.wantAllowed {
				t.Fatalf("allowed=%t err=%v", test.wantAllowed, err)
			}
			if provider.listCalls != 1 || len(provider.statCalls) != 0 {
				t.Fatalf("unexpected recursion or details: lists=%d stats=%v", provider.listCalls, provider.statCalls)
			}
		})
	}
}

type embyDirectoryListingFailureProvider struct {
	*webhookTestProvider
	err error
}

func (p embyDirectoryListingFailureProvider) List(context.Context, models.EmbyFrozenFile) ([]models.EmbyRemoteFile, error) {
	return nil, p.err
}

func TestEmbyDirectoryLiveRetentionRequiresSuccessfulListing(t *testing.T) {
	failure := errors.New("list unavailable")
	provider := embyDirectoryListingFailureProvider{webhookTestProvider: &webhookTestProvider{}, err: failure}
	ctx := models.WithEmbyDirectoryProvider(t.Context(), provider)
	scope := models.EmbyDirectoryScope{LocalPath: "/media/Show", Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, Path: "/cloud", FileName: "Show"}}
	if err := verifyEmbyDirectoryLiveItems(ctx, scope, nil); !errors.Is(err, failure) {
		t.Fatalf("empty live inventory hid listing failure: %v", err)
	}
}

func TestEmbyDirectoryLiveRetentionReusesSeparateDirectoryListings(t *testing.T) {
	provider := &cleanupWorkerProvider{webhookTestProvider: &webhookTestProvider{files: map[string]models.EmbyRemoteFile{}}}
	ctx, release, err := models.BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = models.WithEmbyDirectoryProvider(ctx, provider)
	for range 3 {
		for _, name := range []string{"First", "Second", "Third"} {
			scope := models.EmbyDirectoryScope{LocalPath: path.Join("/media", name), Root: models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: 1, AccountIdentity: "owner", FileID: name, Path: "/cloud", FileName: name}}
			if err := verifyEmbyDirectoryLiveItems(ctx, scope, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if provider.listCalls != 3 || len(provider.statCalls) != 0 {
		t.Fatalf("expected one direct listing per directory: lists=%d stats=%v", provider.listCalls, provider.statCalls)
	}
}
