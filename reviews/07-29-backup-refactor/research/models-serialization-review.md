# Models serialization review — `dd4795a^..dd4795a`

- **Scope**: Internal review of the commit’s changed `backend/internal/models` behavior, including schema/migration compatibility, GORM persistence, backup and SQLite→PostgreSQL serialization, nullable/zero values, stable stored values, and queue state invariants.
- **Date**: 2026-07-29
- **Authority reviewed**:
  - `docs/reference/database-schema.md:11-21, 47-106, 755-990`
  - `docs/operations/database.md:11-24, 25-66`
  - `docs/architecture/upload-and-strm-processing.md:48-80, 82-123`
  - `docs/architecture/emby-library-sync.md:67-147, 162-198, 461-472`

## Result

No plausible or confirmed defect candidate was found in the reviewed model/serialization scope.

## Evidence reviewed

### Schema, catalog, and migration compatibility

| File | Evidence |
|---|---|
| `backend/internal/models/migrator.go:24-41` | Advances the database version to `62`; includes `BackupConfig` and `BackupRecord` in `AllTables`; keeps the full model set as the shared schema source. |
| `backend/internal/models/migrator.go:692-740` | Version `61 → 62` first AutoMigrates the two backup models, then marks only completed records whose on-disk artifact still exists as `legacy`. A failed stat aborts without advancing the version; nonexistent records are deliberately left unchanged. |
| `backend/internal/models/migrator.go:1246-1261` | Empty-db and repair schema creation runs `AutoMigrate` over `AllTables`, then ensures the active transfer partial unique indexes. |
| `backend/internal/models/table_catalog.go:30-91` | Catalog is deterministically derived from `AllTables`; physical GORM table names are stable IDs; ordinary backup/restore excludes only instance-local `BackupRecord`, while SQLite→PostgreSQL migration includes all tables. |
| `backend/internal/models/migrator_test.go:39-98, 1016-1489` | Exercises backup-record upgrade classification, version-60/61 transfer field migration retry behavior, hidden locator serialization, and active transfer-index backfill. |
| `backend/internal/models/table_catalog_test.go:8-66` | Verifies catalog order/uniqueness, the regular-backup `BackupRecord` exclusion, migration inclusion, and pointer-receiver `TableName()` handling. |

### Backup and migration serialization

| File | Evidence |
|---|---|
| `backend/internal/backup/record_codec.go:17-108` | Artifact records serialize GORM persistence columns rather than API JSON fields, including `json:"-"` stored fields; strict decoding rejects missing and unknown columns. |
| `backend/internal/backup/export.go:100-185` | Uses one consistent read transaction, GORM model schemas, stable catalog IDs, ordered rows, `rows.Close()`, and post-iteration `rows.Err()` checking. |
| `backend/internal/backup/restore_import.go:17-103` | Imports strict decoded rows in batches with primary keys retained, clears tables in reverse catalog order, and repairs PostgreSQL sequences. |
| `backend/internal/migrate/artifact.go:101-136, 213-273, 451-575, 615-657` | SQLite→PostgreSQL archive uses the same durable columns and strict missing/unknown-column decoding; target import runs schema creation, reverse clear, ordered import, and sequence repair in one transaction. |
| `backend/internal/models/backup.go:52-116` | The scheduled password ciphertext is persisted with an explicit column but excluded from API JSON; the read DTO exposes only a boolean. |
| `backend/internal/models/backup_test.go:14-121` | Verifies ciphertext is not JSON-serialized, local encrypt/decrypt/clear behavior, retention of historical columns under GORM AutoMigrate, and unencrypted-confirmation zero/nullable semantics. |

### Queue state and task-admission invariants

| File | Evidence |
|---|---|
| `backend/internal/models/dbdownload.go:45-75, 559-663, 880-904` | Separates hidden execution/dedup locators from public task fields; new tasks generate scoped SHA-256 dedup keys; running count is limited to `downloading`. |
| `backend/internal/models/dbupload.go:72-118, 142-149, 306-367, 1057-1085` | Introduces persisted statuses `5` (remote completed/pending local finalization) and `6` (finalizing); conditional state transitions prevent competing finalizers and startup recovery changes only `6 → 5`. |
| `backend/internal/models/download.go:71-144` and `backend/internal/models/upload.go:47-242` | Queue start/restart is gate-protected; workers and schedulers are tracked in wait groups; `StopAndWait` prevents snapshotting while dispatched work remains. |
| `backend/internal/models/strm_generation_task.go:107-245, 428-704` | STRM task creation is admission-gated and request-hash deduplicated; child state/progress updates are transactional and compare task state before transition. |
| `backend/internal/models/emby_library_refresh_task.go:1405-1482, 1596-1874` | Pending-task updates use status/time predicates to avoid stale scans overwriting fresh events; execution claim changes `pending → refreshing` before task-gate admission is released; terminal writes are conditioned on `refreshing`. |
| `backend/internal/models/queue_admission_test.go:19-294` and `backend/internal/models/emby_library_refresh_task_test.go:2204-2495` | Tests cover blocked admission, commit-before-block ordering, queued-worker quiescence, stale scan writes, renewed deadlines, extended debounce, and persisted `refreshing` registration before the barrier finishes blocking. |

## Rejected concerns

1. **Rejected — stored fields marked `json:"-"` may be omitted from backup or migration artifacts.**
   `artifactRecordCodec.recordMap` iterates GORM schema fields by `DBName`, not JSON tags (`backend/internal/backup/record_codec.go:42-51`), and the migration writer follows the same GORM-schema strategy (`backend/internal/migrate/artifact.go:615-624`). Strict readers require each persisted column and reject extras (`record_codec.go:54-88`; `artifact.go:627-657`). Thus hidden queue locators, password ciphertext, and task JSON backing columns remain in protected artifact data without being exposed by HTTP JSON.

2. **Rejected — excluding `BackupRecord` from ordinary restore leaves a broken catalog or violates the stated restore contract.**
   The exclusion is explicit in `backend/internal/models/table_catalog.go:37-43`, while `BackupRecord` remains in the SQLite→PostgreSQL catalog (`:45-50`). The database authority specifies this split because ordinary restore must preserve the target instance’s artifact index, whereas cross-engine migration includes it (`docs/reference/database-schema.md:13-17`; `docs/operations/database.md:51-55`). Restore uses the regular catalog consistently for export, clear, import, snapshot, and sequence repair (`backup/export.go:100-139`; `restore_apply.go:157-169`; `restore_snapshot.go:201-215, 284-305`).

3. **Rejected — new upload finalization statuses could be considered non-running and permit a backup while writes continue.**
   `CountRunningUploadTasks` counts both `uploading` and `remote_completed_finalizing` (`backend/internal/models/dbupload.go:1057-1068`), and the status snapshot also classifies both finalization statuses as processing (`queue_status.go:24-41`). The maintenance barrier additionally stops and waits for dispatched upload workers before considering that subsystem idle (`backend/internal/backup/tasks.go:70-75, 237-246`).

4. **Rejected — `BackupConfig` `json:"-"` ciphertext could leak through the updated backup-config endpoint.**
   The controller returns `BackupConfigReadDTO` rather than `BackupConfig` (`backend/internal/controllers/backup.go:237-245`), and the DTO only exposes `backup_encryption_enabled` (`backend/internal/models/backup.go:66-90`). `TestBackupConfigReadDTOExcludesScheduledPasswordCiphertext` independently marshals both types and asserts the field/value are absent (`backend/internal/models/backup_test.go:14-36`).

## Validation

- Passed: `(cd backend && go test ./internal/models ./internal/backup ./internal/migrate)`
- Passed: `git diff --check dd4795a^..dd4795a`

## Caveats

- This review is limited to commit `dd4795a^..dd4795a` and the requested model/serialization paths and their direct callers/tests.
- No product code, documents, tests, Git history, or task manifests were changed by this review.
