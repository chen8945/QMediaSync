//go:build integration

package backup

import (
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestRestoreEmbyIdentitySchemaCompatibilityPostgres(t *testing.T) {
	testRestoreEmbyIdentitySchemaCompatibility(t, setupHistoryRestorePostgres)
}

func TestRestoreLegacyEmbyIdentityPreflightPostgres(t *testing.T) {
	testRestoreLegacyEmbyIdentityPreflight(t, setupHistoryRestorePostgres)
}

func TestRestoreOldEmbyIdentityCrossEngine(t *testing.T) {
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for isolated cross-engine identity tests")
	}
	for _, sourceEngine := range []string{"sqlite", "postgres"} {
		for _, format := range []string{"logical", "legacy"} {
			t.Run(sourceEngine+"/"+format, func(t *testing.T) {
				setupBackupTest(t)
				source := openCrossEngineDatabase(t, sourceEngine)
				seedEmbyIdentityRestoreDatabase(t, source)
				dir := embyIdentityBackupDirectory(t, source, format, 70)
				archive := filepath.Join(t.TempDir(), "old-identities.zip")
				if err := helpers.ZipDir(dir, archive); err != nil {
					t.Fatal(err)
				}
				targetEngine := "postgres"
				if sourceEngine == "postgres" {
					targetEngine = "sqlite"
				}
				target := openCrossEngineDatabase(t, targetEngine)
				seedEmbyIdentityRestoreDatabase(t, target)
				db.Db = target
				if err := Restore(archive); err != nil {
					t.Fatal(err)
				}
				assertRestoredEmbyIdentities(t, target)
			})
		}
	}
}
