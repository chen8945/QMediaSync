package emby

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestEmbySnapshotPipelineDeletionDuringParallelPreparationPreservesOldState(t *testing.T) {
	testEmbySnapshotPipelineDeletionDuringParallelPreparation(t, func(t *testing.T) *gorm.DB {
		t.Helper()
		conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		db.Db = conn
		setupSnapshotTestTables(t)
		return conn
	})
}

func testEmbySnapshotPipelineDeletionDuringParallelPreparation(t *testing.T, setupDatabase func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, mode := range []string{"full", "incremental"} {
		t.Run(mode, func(t *testing.T) {
			previousDB, previousLogger, previousConfig := db.Db, helpers.AppLogger, models.GlobalEmbyConfig
			t.Cleanup(func() {
				db.Db, helpers.AppLogger, models.GlobalEmbyConfig = previousDB, previousLogger, previousConfig
				SetEmbySyncRunning(false)
			})
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			models.GlobalEmbyConfig = nil
			SetEmbySyncRunning(false)
			conn := setupDatabase(t)
			if err := conn.Create(&models.EmbyMediaItem{ItemId: "999", ItemIdInt: 999, LibraryId: "lib", LastSeenSyncRun: "previous"}).Error; err != nil {
				t.Fatal(err)
			}
			ready, release := make(chan struct{}, 2), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var active, maximum atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/emby/System/Info/Public":
					fmt.Fprint(w, `{"Id":"server-a"}`)
				case "/emby/Users":
					fmt.Fprint(w, `[{"Id":"u","Policy":{"EnableAllFolders":true}}]`)
				case "/emby/Library/MediaFolders":
					fmt.Fprint(w, `{"Items":[{"Id":"lib","Name":"library"}]}`)
				case "/emby/Items":
					id := r.URL.Query().Get("Ids")
					if id == "" {
						fmt.Fprint(w, `{"Items":[{"Id":"101","Type":"Video"},{"Id":"102","Type":"Video"}],"TotalRecordCount":2}`)
						return
					}
					current := active.Add(1)
					defer active.Add(-1)
					for old := maximum.Load(); current > old; old = maximum.Load() {
						if maximum.CompareAndSwap(old, current) {
							break
						}
					}
					ready <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					fmt.Fprintf(w, `{"Items":[{"Id":%q,"Type":"Video","Path":%q,"MediaSources":[]}],"TotalRecordCount":1}`, id, "/media/"+id+".strm")
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(unblock)
			config := models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, LastSavedCursorAt: 100, LastFullSyncAt: 50}
			if err := conn.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var runErr error
			go func() {
				defer close(done)
				if mode == "full" {
					_, runErr = PerformEmbySync()
				} else {
					_, runErr = PerformEmbyIncrementalSync()
				}
			}()
			t.Cleanup(func() {
				unblock()
				<-done
			})
			for range 2 {
				select {
				case <-ready:
				case <-time.After(5 * time.Second):
					t.Fatal("两个详情请求未能并行准备")
				}
			}
			if err := conn.Transaction(func(tx *gorm.DB) error {
				_, err := models.RegisterEmbyDeletionTx(tx, "server-a", []string{"101"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			unblock()
			<-done
			if !errors.Is(runErr, models.ErrEmbySnapshotStale) {
				t.Fatalf("删除后的旧响应没有被拒绝：%v", runErr)
			}
			if maximum.Load() != 2 || active.Load() != 0 || IsEmbySyncRunning() {
				t.Fatalf("并发准备或运行状态未结束：maximum=%d active=%d", maximum.Load(), active.Load())
			}
			var fresh models.EmbyConfig
			if err := conn.First(&fresh).Error; err != nil {
				t.Fatal(err)
			}
			if fresh.LastSavedCursorAt != 100 || fresh.LastFullSyncAt != 50 || fresh.LastError == "" {
				t.Fatalf("删除竞争推进了成功水位：%+v", fresh)
			}
			var items []models.EmbyMediaItem
			if err := conn.Find(&items).Error; err != nil || len(items) != 1 || items[0].ItemId != "999" {
				t.Fatalf("清掉旧项或提交了过期组：items=%+v err=%v", items, err)
			}
		})
	}
}
