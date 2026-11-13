package backup

import (
	"context"
	"errors"
	"sync"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestRestoreSettingsDrainTimeoutKeepsMaintenanceAndData(t *testing.T) {
	database, observer := setupRestoreMaintenanceTest(t)
	if err := database.AutoMigrate(&models.EmbyConfig{}, &models.JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
	models.StartMediaLibrarySettingsRuntime()
	defer models.StopMediaLibrarySettingsRuntime(context.Background())
	applying, release := make(chan struct{}), make(chan struct{})
	finishApply := sync.OnceFunc(func() { close(release) })
	finished := make(chan error, 1)
	var saves sync.WaitGroup
	defer func() { finishApply(); saves.Wait() }()
	saves.Go(func() {
		_, err := models.SaveMediaLibrarySettings(context.Background(), models.MediaServerEmby,
			models.MediaLibrarySettingsChange{Values: models.EmbyConfig{EmbyUrl: "http://saved.invalid", EmbyApiKey: "key"}}, nil,
			func(context.Context, *models.EmbyConfig) []string {
				close(applying)
				<-release
				return nil
			})
		finished <- err
	})
	<-applying
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	maintenance, err := beginRestoreMaintenanceRuntime(ctx)
	if maintenance != nil || !errors.Is(err, context.Canceled) || !db.IsMaintenance(database) {
		t.Fatalf("settings drain failure reopened maintenance: permit=%v err=%v", maintenance, err)
	}
	if release, accepted := AcquireRuntimeRequest(); accepted {
		release()
		t.Fatal("settings drain failure reopened HTTP admission")
	}
	var item backupTestItem
	if err := observer.First(&item, 1).Error; err != nil || item.Name != "before" {
		t.Fatalf("settings drain failure changed restore data: %+v %v", item, err)
	}
	finishApply()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
