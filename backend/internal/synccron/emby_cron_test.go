package synccron

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"qmediasync/internal/db"

	"qmediasync/internal/models"
)

func TestMediaLibraryCronUpdatesOnlyRequestedEndpoint(t *testing.T) {
	oldDB, oldCron, oldEntries, oldPerform := db.Db, GlobalCron, mediaLibraryCronEntries, performScheduledMediaLibrarySync
	t.Cleanup(func() {
		db.Db = oldDB
		GlobalCron = oldCron
		mediaLibraryCronEntries = oldEntries
		performScheduledMediaLibrarySync = oldPerform
	})
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Db = testDB
	if err := testDB.AutoMigrate(&models.EmbyConfig{}, &models.JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
	config := models.EmbyConfig{Enabled: true, SyncEnabled: 1, EmbyUrl: "http://media.local", EmbyApiKey: "test-key", SyncCron: "0 * * * *", EnableDailyFirstFullSync: 1}
	if err := testDB.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	jellyfin := models.JellyfinConfig(config)
	jellyfin.ID = 0
	if err := testDB.Create(&jellyfin).Error; err != nil {
		t.Fatal(err)
	}
	GlobalCron = cron.New()
	mediaLibraryCronEntries = make(map[models.MediaServerType]cron.EntryID)
	unrelated, err := GlobalCron.AddFunc("*/5 * * * *", func() {})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		if err := UpdateMediaLibraryCron(kind); err != nil {
			t.Fatal(err)
		}
	}
	jellyfinEntry := mediaLibraryCronEntries[models.MediaServerJellyfin]
	var calledKind models.MediaServerType
	var calledMode string
	performScheduledMediaLibrarySync = func(kind models.MediaServerType, mode string) (int, error) {
		calledKind, calledMode = kind, mode
		return 0, nil
	}
	GlobalCron.Entry(mediaLibraryCronEntries[models.MediaServerEmby]).Job.Run()
	if calledKind != models.MediaServerEmby || calledMode != models.EmbySyncModeFull {
		t.Fatalf("scheduled=%s/%s", calledKind, calledMode)
	}
	if err := testDB.Model(&models.EmbyConfig{}).Where("id > 0").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := UpdateMediaLibraryCron(models.MediaServerEmby); err != nil {
		t.Fatal(err)
	}
	if _, exists := mediaLibraryCronEntries[models.MediaServerEmby]; exists {
		t.Fatal("disabled endpoint retained schedule")
	}
	if mediaLibraryCronEntries[models.MediaServerJellyfin] != jellyfinEntry || GlobalCron.Entry(unrelated).ID != unrelated {
		t.Fatal("other schedules replaced")
	}
	GlobalCron.Entry(jellyfinEntry).Job.Run()
	if calledKind != models.MediaServerJellyfin {
		t.Fatal("Jellyfin schedule lost its endpoint")
	}
}

func TestSelectScheduledEmbySyncMode(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 6, 30, 1, 30, 0, 0, loc)
	todayFullSync := time.Date(2026, 6, 30, 0, 5, 0, 0, loc).Unix()
	yesterdayFullSync := time.Date(2026, 6, 29, 23, 50, 0, 0, loc).Unix()

	tests := []struct {
		name   string
		config *models.EmbyConfig
		want   string
	}{
		{
			name: "关闭每日首次全量同步时执行增量",
			config: &models.EmbyConfig{
				EnableDailyFirstFullSync: 0,
				LastFullSyncAt:           0,
			},
			want: models.EmbySyncModeIncremental,
		},
		{
			name: "启用且从未全量同步时执行全量",
			config: &models.EmbyConfig{
				EnableDailyFirstFullSync: 1,
				LastFullSyncAt:           0,
			},
			want: models.EmbySyncModeFull,
		},
		{
			name: "启用且今天尚未全量成功时执行全量",
			config: &models.EmbyConfig{
				EnableDailyFirstFullSync: 1,
				LastFullSyncAt:           yesterdayFullSync,
			},
			want: models.EmbySyncModeFull,
		},
		{
			name: "启用且今天已有全量成功时执行增量",
			config: &models.EmbyConfig{
				EnableDailyFirstFullSync: 1,
				LastFullSyncAt:           todayFullSync,
			},
			want: models.EmbySyncModeIncremental,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectScheduledEmbySyncMode(tt.config, now)
			if got != tt.want {
				t.Fatalf("selectScheduledEmbySyncMode() = %q, want %q", got, tt.want)
			}
		})
	}
}
