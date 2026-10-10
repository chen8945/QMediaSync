//go:build integration

package emby

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestEmbySnapshotPipelineDeletionDuringParallelPreparationPreservesOldStatePostgres(t *testing.T) {
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for an isolated PostgreSQL test database")
	}
	testEmbySnapshotPipelineDeletionDuringParallelPreparation(t, func(t *testing.T) *gorm.DB {
		conn, _ := setupEmbyPerformancePostgres(t)
		return conn
	})
}

// TestEmbySnapshotPipelineThreeCommitWorkersPostgresDML 在真实 INSERT 后暂停，核对生产入口的三个并行批事务。
func TestEmbySnapshotPipelineThreeCommitWorkersPostgresDML(t *testing.T) {
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for an isolated PostgreSQL test database")
	}
	conn, _ := setupEmbyPerformancePostgres(t)
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Items" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Items":[`)
		for i := range 12 {
			if i != 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"Id":"%d","Name":"movie","Type":"Movie","Path":"/media/%d.mkv","MediaSources":[]}`, 801+i, 801+i)
		}
		fmt.Fprint(w, `],"TotalRecordCount":12}`)
	}))
	t.Cleanup(server.Close)
	config := &models.EmbyConfig{
		EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1,
	}
	if err := conn.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	token, err := models.BeginEmbyIndexRead("worker-proof", config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 3), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var writes, active atomic.Int32
	const callback = "pipeline:hold_completed_evidence"
	if err := conn.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Error != nil || tx.RowsAffected == 0 || tx.Statement.Table != "emby_item_evidences" {
			return
		}
		// 首次三个 INSERT 必须来自不同的活跃事务；放行后只统计后续写入。
		if writes.Add(1) > 3 {
			return
		}
		active.Add(1)
		defer active.Add(-1)
		select {
		case entered <- struct{}{}:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
			return
		}
		select {
		case <-release:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Callback().Create().Remove(callback) })
	handled := map[string]bool{}
	done, finished := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(finished)
		count, err := syncEmbyLibrary(
			ctx,
			embyclientrestgo.NewClient(server.URL, "fixture"),
			embyclientrestgo.EmbyItemsQuery{LibraryID: "lib"},
			"library",
			"worker-proof",
			123,
			token,
			handled,
		)
		if err == nil && count != 12 {
			err = fmt.Errorf("processed=%d, want 12", count)
		}
		done <- err
	}()
	defer func() {
		unblock()
		cancel()
		<-finished
	}()
	for range 3 {
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("pipeline exited before holding three real writes: %v", err)
		case <-ctx.Done():
			t.Fatalf("did not reach three simultaneous real writes: %v", ctx.Err())
		}
	}
	if active.Load() != 3 {
		t.Fatalf("held real write transactions=%d, want 3", active.Load())
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 12 || active.Load() != 0 || len(handled) != 12 {
		t.Fatalf("transaction results were lost: writes=%d active=%d handled=%v", writes.Load(), active.Load(), handled)
	}
	for _, model := range []any{
		&models.EmbyMediaItem{}, &models.EmbyItemState{}, &models.EmbyItemEvidence{}, &models.EmbyItemMembership{},
	} {
		var count int64
		if err := conn.Model(model).Count(&count).Error; err != nil || count != 12 {
			t.Fatalf("committed rows %T count=%d err=%v", model, count, err)
		}
	}
}
