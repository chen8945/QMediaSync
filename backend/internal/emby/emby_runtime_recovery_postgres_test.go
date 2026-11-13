//go:build integration

package emby

import (
	"os"
	"testing"
)

func TestJellyfinSyncPreparationRecoveryPostgres(t *testing.T) {
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	for _, mode := range []string{"full", "incremental", "single"} {
		for _, fault := range []string{"selection", "connection", "disabled", "identity"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				conn, _ := setupEmbyPerformancePostgres(t)
				testJellyfinSyncPreparationFailure(t, conn, mode, fault)
			})
		}
	}
}
