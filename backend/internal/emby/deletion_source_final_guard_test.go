package emby

import (
	"context"
	"errors"
	"testing"
	"time"

	"qmediasync/internal/models"
)

type withoutEmbyVerificationContext struct{ context.Context }

func (ctx withoutEmbyVerificationContext) Value(key any) any {
	if _, ok := key.(embyDeletionVerificationKey); ok {
		return nil
	}
	return ctx.Context.Value(key)
}

// 用真实 worker→models→provider callback 取得最终阶段，避免测试自行伪造阶段标记。
func TestEmbySourceFinalGuardUsesCompletedReadsOnly(t *testing.T) {
	for _, state := range []string{"complete", "failure", "missing", "expired", "other snapshot", "in flight", "no source context", "expired full snapshot", "missing full snapshot", "no full context"} {
		t.Run(state, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", true)
			finalCalls, providerReads := 0, 0
			f.worker.verify = func(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
				if !models.IsEmbyDeletionFinalGuard(ctx) {
					return verifyEmbyDeletion(ctx, input, target)
				}
				finalCalls++
				cache := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
				switch state {
				case "expired full snapshot":
					cache.completedAt = cache.now().Add(-embyDeletionVerificationWindow)
				case "missing full snapshot":
					cache.loaded = false
				case "no full context":
					ctx = withoutEmbyVerificationContext{ctx}
				}
				localBefore := f.localReads.Load()
				fullBefore := f.fullScans.Load()
				err := verifyEmbyDeletion(ctx, input, target)
				if f.fullScans.Load() != fullBefore {
					t.Error("final guard reacquired the complete Emby inventory")
				}
				if f.localReads.Load() != localBefore+1 {
					t.Error("final guard omitted original item check")
				}
				if state == "expired full snapshot" || state == "missing full snapshot" || state == "no full context" {
					if !errors.Is(err, models.ErrEmbySnapshotStale) {
						t.Errorf("invalid full snapshot accepted or refreshed: %v", err)
					}
					return err
				}
				if err != nil {
					t.Errorf("current full snapshot failed: %v", err)
					return err
				}
				key := embySourceLocationKey{models.SourceType115, 1, "principal", "live"}
				read := &embySourceLocationRead{ready: make(chan struct{}), readAt: cache.now(), snapshotAt: cache.completedAt, path: "/cloud/Other/live.mkv"}
				switch state {
				case "failure":
					read.err = models.ErrEmbyDeleteUnverified
				case "expired":
					read.readAt = cache.now().Add(-embyDeletionVerificationWindow)
				case "other snapshot":
					read.snapshotAt = cache.completedAt.Add(-time.Nanosecond)
				case "no source context":
					ctx = withoutEmbyVerificationContext{ctx}
				}
				if state != "in flight" {
					close(read.ready)
				}
				if state != "missing" {
					cache.sourceReads = map[embySourceLocationKey]*embySourceLocationRead{key: read}
				}
				_, err = cachedEmbySourceLocation(ctx, key, func(context.Context) (string, error) {
					providerReads++
					return "/cloud/Other/live.mkv", nil
				})
				want := models.ErrEmbySnapshotStale
				if state == "complete" {
					want = nil
				} else if state == "failure" {
					want = models.ErrEmbyDeleteUnverified
				}
				if !errors.Is(err, want) {
					t.Errorf("final source check=%v, want=%v", err, want)
				}
				return err
			}
			f.receive(t)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			f.worker.process(ctx, claimWebhookTest(t))
			wantWrites := 0
			if state == "complete" {
				wantWrites = 1
			}
			wantScans := int32(1)
			if state == "expired full snapshot" || state == "missing full snapshot" {
				// 拒绝目录后，文件回退的准备阶段仍能重建上下文。
				wantScans = 2
			}
			if finalCalls != 1 || providerReads != 0 || f.fullScans.Load() != wantScans || f.cloud.directoryCalls != wantWrites {
				t.Fatalf("final=%d source reads=%d full scans=%d directory writes=%d, want writes=%d", finalCalls, providerReads, f.fullScans.Load(), f.cloud.directoryCalls, wantWrites)
			}
		})
	}
}
