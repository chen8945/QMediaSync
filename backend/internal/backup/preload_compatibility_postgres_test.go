//go:build integration

package backup

import "testing"

func TestRestorePreloadSchemaCompatibilityPostgres(t *testing.T) {
	testRestorePreloadSchemaCompatibility(t, setupHistoryRestorePostgres)
}
