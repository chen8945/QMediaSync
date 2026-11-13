package app

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/synccron"

	"github.com/robfig/cron/v3"
)

func TestAppStopWaitsForActiveCronCallback(t *testing.T) {
	const childEnv = "QMS_TEST_STOP_ACTIVE_CRON"
	if os.Getenv(childEnv) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAppStopWaitsForActiveCronCallback$", "-test.timeout=15s")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("shutdown cron integration failed: %v\n%s", err, output)
		}
		return
	}
	setupFnosStartupTest(t)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finishCallback := sync.OnceFunc(func() { close(release) })
	var callbacks sync.Once
	scheduler := cron.New()
	scheduler.Schedule(cron.Every(time.Second), cron.FuncJob(func() {
		callbacks.Do(func() {
			close(started)
			<-release
			close(finished)
		})
	}))
	synccron.TokenCron = scheduler
	scheduler.Start()
	defer func() { finishCallback(); <-scheduler.Stop().Done() }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("cron callback did not start")
	}
	stopped := make(chan struct{})
	go func() { (&App{}).Stop(); close(stopped) }()
	defer func() { finishCallback(); <-stopped }()
	select {
	case <-stopped:
		t.Fatal("application stop returned while a cron callback was active")
	case <-time.After(100 * time.Millisecond):
	}
	finishCallback()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("application stop did not finish after cron callback exited")
	}
	select {
	case <-finished:
	default:
		t.Fatal("application resources closed before cron callback completed")
	}
}
