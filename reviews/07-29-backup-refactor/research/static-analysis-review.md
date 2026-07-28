# Static-analysis review: dd4795a^..dd4795a

- **Scope**: Go changes in the backup/restore and SQLite→PostgreSQL migration paths.
- **Review lenses**: compiler/type system, resource ownership, error propagation, numeric bounds, filesystem and unsafe conversion.
- **Date**: 2026-07-29

## Candidate findings

### 1. High — failed restore to an uninitialized PostgreSQL target leaves it permanently half-initialized

- **Location**: `backend/internal/backup/restore_apply.go:152-169`; rollback at `backend/internal/backup/restore_snapshot.go:277-305`; initialization decision at `backend/internal/models/migrator.go:1270-1285`.
- **Concrete trigger/state**: Restore a valid v1 PostgreSQL artifact to the supported fresh/uninitialized PostgreSQL target, then have any later submission step fail after `ensureRestoreSchema` succeeds (for example, a transient write/quota failure while importing a table, or a schema/data constraint error). The preflight accepts such a target because it only probes a read-only transaction (`restore_target.go:216-245`).
- **Observed control flow**:
  1. `applyRestorePostgres` calls `ensureRestoreSchema` *before* its import transaction.
  2. `ensureRestoreSchema` observes a missing catalog table and calls `createRestoreSchema`, which `AutoMigrate`s all tables outside the transaction.
  3. A later `clearCatalogTables` / `importArtifactTables` / sequence step fails and the import transaction rolls back.
  4. `rollbackAfterFailedRestore` calls `RestoreSnapshot.Rollback`. For the initially empty target, `capturePostgres` recorded no table data. `rollbackPostgres` clears catalog data and imports only the tables that existed in the snapshot; it never drops tables created by `ensureRestoreSchema`.
  5. On the next normal startup, `InitDB` sees the now-present `migrator` table and skips first-time initialization (`migrator.go:1272-1277`), despite that table being empty. Defaults and the migrator row are therefore never created.
- **Expected contract**: The operation documentation promises that any post-snapshot failure automatically rolls back database and files, and explicitly supports an uninitialized PostgreSQL restore target (`docs/operations/database.md:45-48`; `restore_apply.go:152-153`). After a failed restore, that target must remain equivalent to its pre-restore empty state so the next startup can perform its normal first-run initialization.
- **Why existing tests miss it**: `TestApplyRestoreArtifactSwitchesTargetAndMirrorsWhitelist` (`backend/internal/backup/restore_flow_test.go:152-235`) exercises SQLite only and only a successful apply. `restore_target_test.go` validates PostgreSQL target parsing but supplies no PostgreSQL instance or failure-after-schema scenario. The documented verification process also identifies real PostgreSQL transactional behavior as manual-only (`docs/engineering/verification.md:108-110`).

## Rejected concerns

- **Rejected — migration JSONL export uses default transaction isolation with offset pagination** (`backend/internal/migrate/artifact.go:188-200`, `233-254`). This can miss or duplicate rows if another process changes the embedded PostgreSQL source while export is in progress because PostgreSQL `READ COMMITTED` statements can observe different snapshots. In the normal migration-server lifecycle, however, the main application does not start its business runtime, so this needs an unsupported concurrent writer (or a separately established multi-instance deployment) to trigger. It is not presented as a finding.

## Commands run

- Passed: `(cd backend && go test ./internal/backup ./internal/migrate ./internal/models ./internal/controllers ./internal/taskgate)`.
- Passed: `(cd backend && go vet ./internal/backup ./internal/migrate ./internal/models ./internal/controllers ./internal/taskgate)`.
- `go test -race ./internal/backup ./internal/controllers ./internal/models ./internal/taskgate` did not complete cleanly because existing `internal/controllers` credential-session timing tests timed out; the project verification document already excludes that package from broad race runs. No race report is used as evidence for the finding above.
