//go:build integration

package syncstrm

import (
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestStrmRefreshRecoveryPostgres(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		account, sp := setupGenerationRetryPostgresTestDB(t)
		if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.JellyfinConfig{}); err != nil {
			t.Fatal(err)
		}
		testLegacyStrmParentRefreshFinalizes(t, account, sp)
	})
	for _, stage := range []string{"resolve", "submit", "batch-submit"} {
		t.Run(stage, func(t *testing.T) {
			account, sp := setupGenerationRetryPostgresTestDB(t)
			testStrmRefreshIdentityFailureRemainsRecoverable(t, account, sp, stage)
		})
	}
}

func TestOrdinaryStrmRefreshRecoveryPostgres(t *testing.T) {
	for _, source := range []models.StrmGenerationSource{models.StrmGenerationSourceUploadCompleted, models.StrmGenerationSourceRemoteExists} {
		for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
			for _, stage := range []string{"resolve", "submit", "local-offline"} {
				t.Run(string(source)+"/"+string(kind)+"/"+stage, func(t *testing.T) {
					account, sp := setupGenerationRetryPostgresTestDB(t)
					testStrmRefreshFailureForSource(t, account, sp, stage, source, kind)
				})
			}
		}
	}
}
