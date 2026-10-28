package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/playback"
)

func restoreTestPreloader(t *testing.T) *playback.Preloader {
	t.Helper()
	previous := playback.DefaultPreloader
	preloader := playback.NewPreloader()
	playback.DefaultPreloader = preloader
	preloader.Apply(true, 1)
	preloader.Start(t.Context())
	t.Cleanup(func() {
		preloader.Stop()
		playback.DefaultPreloader = previous
	})
	return preloader
}

func TestRestoreStopsPreloadAfterHTTPAdmissionBeforeSQLDrain(t *testing.T) {
	database, _ := setupRestoreMaintenanceTest(t)
	preloader := restoreTestPreloader(t)
	started := make(chan struct{})
	observed := make(chan bool, 1)
	if !preloader.Submit(playback.PreloadCandidate{
		Key: "restore-old-source", Scope: "restore-device", Kind: playback.PreloadNext,
		Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "test-user"}, "restore-old-source") {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}
			close(started)
			<-ctx.Done()
			release, admitted := AcquireRuntimeRequest()
			if admitted {
				release()
			}
			observed <- !admitted && !db.IsMaintenance(database) && !task.Publish(func() {})
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		},
	}) {
		t.Fatal("恢复测试作业未接纳")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("恢复测试作业未启动")
	}
	maintenance, err := beginRestoreMaintenanceRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Finish(false, false); err != nil {
		t.Fatal(err)
	}
	select {
	case correctOrder := <-observed:
		if !correctOrder {
			t.Fatal("预加载必须在关闭 HTTP 准入后、SQL 排空前撤销，且禁止旧发布")
		}
	case <-time.After(time.Second):
		t.Fatal("预加载未退出")
	}
	if preloader.Enabled() || !db.IsMaintenance(database) {
		t.Fatal("恢复结束重开了旧运行态")
	}
}

func TestRestoreInvalidArchiveKeepsPreloadRunning(t *testing.T) {
	setupRestoreMaintenanceTest(t)
	preloader := restoreTestPreloader(t)
	archive := filepath.Join(t.TempDir(), "invalid.zip")
	if err := os.WriteFile(archive, []byte("invalid zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive); err == nil {
		t.Fatal("无效归档被接受")
	}
	if !preloader.Enabled() {
		t.Fatal("预检失败关闭了正常预加载")
	}
	called := make(chan struct{}, 1)
	if !preloader.Submit(playback.PreloadCandidate{
		Key: "after-invalid-archive", Scope: "active-device", Kind: playback.PreloadNext,
		Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
			called <- struct{}{}
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		},
	}) {
		t.Fatal("预检失败后无法接纳正常作业")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("预检失败后预加载未继续工作")
	}
}
