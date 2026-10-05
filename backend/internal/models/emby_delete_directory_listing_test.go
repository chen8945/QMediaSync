package models

import (
	"context"
	"errors"
	"testing"
	"time"

	"qmediasync/internal/v115open"
)

func TestEmbyDirectoryListingSharesProviderCacheAndFinalGuard(t *testing.T) {
	for _, change := range []string{"missing", "expiry", "configuration", "write", "release"} {
		t.Run(change, func(t *testing.T) {
			files := embyProviderBatchFiles(SourceType115)
			reads := 0
			provider := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: &embyDelete115Stub{list: func(int) (*v115open.FileListResp, error) {
				reads++
				return embyProviderBatchList115(files), nil
			}}}
			root := files[0]
			root.FileID, root.ParentID, root.Path, root.FileName = "7", "0", "/", "media"
			scope := EmbyDirectoryScope{Root: root}
			ctx, release, err := BeginEmbyDeletionExecution(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx = WithEmbyDirectoryProvider(ctx, provider)
			finalCtx := context.WithValue(ctx, embyDeletionFinalGuardKey{}, true)
			if change == "missing" {
				if _, err := ListEmbyDeletionDirectory(finalCtx, scope); err == nil || reads != 0 {
					t.Fatalf("final guard populated a missing listing: reads=%d err=%v", reads, err)
				}
				return
			}
			if _, err := provider.List(ctx, files[0]); err != nil {
				t.Fatal(err)
			}
			for _, checkCtx := range []context.Context{ctx, ctx, finalCtx} {
				if listed, err := ListEmbyDeletionDirectory(checkCtx, scope); err != nil || len(listed) != len(files) || reads != 1 {
					t.Fatalf("directory did not reuse file listing: reads=%d files=%v err=%v", reads, listed, err)
				}
			}
			switch change {
			case "expiry":
				checks := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
				checks.mu.Lock()
				key := embyDeletionListKey(files[0])
				cached := checks.listings[key]
				cached.at = time.Now().Add(-embyDeletePreflightValidity)
				checks.listings[key] = cached
				checks.mu.Unlock()
			case "configuration":
				finish := BeginSyncPositionMutation()
				finish()
			case "write":
				embyInvalidateDeletionListings(ctx, root, true)
			case "release":
				release()
			}
			if _, err := ListEmbyDeletionDirectory(finalCtx, scope); err == nil || reads != 1 {
				t.Fatalf("final guard reused invalid data or issued a read: reads=%d err=%v", reads, err)
			}
			if change != "release" {
				if _, err := ListEmbyDeletionDirectory(ctx, scope); err != nil || reads != 2 {
					t.Fatalf("preparation failed to refresh invalid listing: reads=%d err=%v", reads, err)
				}
			}
		})
	}
}

func TestEmbyDirectoryListingFailureNeverBecomesEmptyInventory(t *testing.T) {
	files := embyProviderBatchFiles(SourceType115)
	reads := 0
	failure := errors.New("list unavailable")
	provider := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: &embyDelete115Stub{list: func(int) (*v115open.FileListResp, error) {
		reads++
		return nil, failure
	}}}
	root := files[0]
	root.FileID, root.ParentID, root.Path, root.FileName = "7", "0", "/", "media"
	ctx, release, err := BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = WithEmbyDirectoryProvider(ctx, provider)
	if _, err := ListEmbyDeletionDirectory(ctx, EmbyDirectoryScope{Root: root}); err == nil {
		t.Fatal("failed listing accepted as empty")
	}
	ctx = context.WithValue(ctx, embyDeletionFinalGuardKey{}, true)
	if _, err := ListEmbyDeletionDirectory(ctx, EmbyDirectoryScope{Root: root}); err == nil || reads != 1 {
		t.Fatalf("final guard retried failed listing: reads=%d err=%v", reads, err)
	}
}
