//go:build integration

package syncstrm

import (
	"testing"

	"qmediasync/internal/models"
)

func TestStandaloneFinalizationPostgres(t *testing.T) {
	for _, stage := range []string{"scope read", "completion save", "failure save"} {
		t.Run(stage, func(t *testing.T) {
			account, sp := setupGenerationRetryPostgresTestDB(t)
			testStandaloneFinalizationFailure(t, account, sp, stage)
		})
	}
	t.Run("completed state preserved", func(t *testing.T) {
		account, sp := setupGenerationRetryPostgresTestDB(t)
		testStandaloneFinalizationChangedState(t, account, sp, models.StrmGenerationStatusCompleted)
	})
}
