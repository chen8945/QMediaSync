package controllers

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestEmbyVideoDeletePreservesUnverifiedLinkedDeletion(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "local_only", true: "linked_unverified"}[enabled], func(t *testing.T) {
			previousDB, previousConfig, previousLogger := db.Db, models.GlobalEmbyConfig, helpers.AppLogger
			conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() {
				db.Db, models.GlobalEmbyConfig, helpers.AppLogger = previousDB, previousConfig, previousLogger
				_ = sqlDB.Close()
			})
			db.Db = conn
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "offline-only", SyncEnabled: 1}
			if enabled {
				models.GlobalEmbyConfig.EnableDeleteNetdisk = 1
			}
			if err := conn.AutoMigrate(&models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
				t.Fatal(err)
			}
			if err := conn.Create(&models.EmbyMediaItem{ItemId: "20", ItemIdInt: 20, Type: "Video"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.Create(&models.EmbyMediaSyncFile{EmbyItemId: 20, SyncFileId: 1}).Error; err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(`{"Event":"library.deleted","Item":{"Id":"20","Type":"Video"}}`))
			Webhook(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d", recorder.Code)
			}
			var count int64
			if err := conn.Model(&models.EmbyMediaSyncFile{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if enabled {
				want = 1
			}
			if count != want {
				t.Fatalf("Video linked deletion must retain unfinished identity: count=%d want=%d", count, want)
			}
		})
	}
}
