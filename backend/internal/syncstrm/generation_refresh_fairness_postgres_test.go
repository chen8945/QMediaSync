//go:build integration

package syncstrm

import (
	"testing"

	"qmediasync/internal/models"
)

func TestStrmRefreshRetryHealthyEndpointPostgres(t *testing.T) {
	for _, stage := range []string{"generate", "finalizing", "parent"} {
		for _, healthy := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
			for _, targetType := range []models.EmbyRefreshTargetType{models.EmbyRefreshTargetTypeLibrary, models.EmbyRefreshTargetTypeItem} {
				t.Run(stage+"/"+string(healthy)+"/"+string(targetType), func(t *testing.T) {
					account, sp := setupGenerationRetryPostgresTestDB(t)
					testStrmRefreshRetryHealthyEndpoint(t, account, sp, stage, healthy, targetType)
				})
			}
		}
	}
}
