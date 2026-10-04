package emby

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBuildMinDateLastSaved回退Overlap且不产生负时间(t *testing.T) {
	tests := []struct {
		name   string
		cursor int64
		want   string
	}{
		{name: "正常游标回退十分钟", cursor: 1782698400, want: "2026-06-29T01:50:00Z"},
		{name: "初始游标不产生负时间", cursor: 0, want: "1970-01-01T00:00:00Z"},
		{name: "小于 overlap 的游标归零", cursor: 300, want: "1970-01-01T00:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildMinDateLastSaved(tt.cursor, 600)
			if got != tt.want {
				t.Fatalf("buildMinDateLastSaved(%d, 600) = %q, want %q", tt.cursor, got, tt.want)
			}
		})
	}
}

func TestPerformEmbyIncrementalSync使用MinDateLastSaved并推进游标(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)
	setupSnapshotTestTables(t)

	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyLibrary{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	var capturedMinDateLastSaved string
	var capturedSortBy string
	var capturedSortOrder string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			fmt.Fprint(w, `{ "Id": "server-a" }`)
		case "/emby/Library/MediaFolders":
			fmt.Fprint(w, `{"Items":[{"Id":"lib-a","Name":"电影"}]}`)
		case "/emby/Items":
			capturedMinDateLastSaved = r.URL.Query().Get("MinDateLastSaved")
			capturedSortBy = r.URL.Query().Get("SortBy")
			capturedSortOrder = r.URL.Query().Get("SortOrder")
			fmt.Fprint(w, `{"TotalRecordCount":0,"Items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := db.Db.Create(&models.EmbyConfig{
		EmbyUrl:           server.URL,
		EmbyApiKey:        "test-key",
		SyncEnabled:       1,
		SyncCron:          "0 * * * *",
		SyncAllLibraries:  1,
		LastSavedCursorAt: 1782698400,
	}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	result, err := PerformEmbyIncrementalSync()
	if err != nil {
		t.Fatalf("PerformEmbyIncrementalSync() error = %v", err)
	}
	if result != 0 {
		t.Fatalf("result = %d, want 0", result)
	}
	if capturedMinDateLastSaved != "2026-06-29T01:50:00Z" {
		t.Fatalf("MinDateLastSaved = %q, want 2026-06-29T01:50:00Z", capturedMinDateLastSaved)
	}
	if capturedSortBy != "DateLastSaved" || capturedSortOrder != "Descending" {
		t.Fatalf("SortBy/SortOrder = %q/%q, want DateLastSaved/Descending", capturedSortBy, capturedSortOrder)
	}

	fresh, err := models.GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() error = %v", err)
	}
	if fresh.LastSavedCursorAt <= 1782698400 {
		t.Fatalf("LastSavedCursorAt = %d, want greater than 1782698400", fresh.LastSavedCursorAt)
	}
	if fresh.LastIncrementalSyncAt == 0 || fresh.LastSyncTime == 0 {
		t.Fatalf("增量同步完成时间未更新: %+v", fresh)
	}
}
