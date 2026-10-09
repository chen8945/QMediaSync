//go:build integration

package backup

import "testing"

func TestRestoreEmbySyncSchemaCompatibilityPostgres(t *testing.T) {
	testRestoreEmbySyncSchemaCompatibility(t, setupHistoryRestorePostgres)
}

func TestRestoreLegacyRootEvidenceDependenciesPostgres(t *testing.T) {
	testRestoreLegacyRootEvidenceDependencies(t, setupHistoryRestorePostgres)
}
