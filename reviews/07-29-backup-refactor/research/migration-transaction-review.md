# Migration-transaction review evidence

- **Review range:** `dd4795a^..dd4795a`
- **Scope:** `backend/internal/migrate/*.go`, `backend/internal/models/table_catalog.go`, `backend/internal/models/migrator.go`, and `backend/main.go`; relevant callers, tests, and authority documentation
- **Authority:** `.trellis/spec/backend/database-guidelines.md`
- **Date:** 2026-07-29
- **Validation:** `(cd backend && go test ./internal/migrate ./internal/models)` passed. `git diff --check dd4795a^ dd4795a` and working-tree status were clean.

## Candidate findings

### High — required target schema upgrade can fail after the import commit, be silently accepted, and delete the retry archive

- **Location:** `backend/internal/migrate/artifact.go:114-135`, `backend/internal/migrate/artifact.go:139-153`; `backend/internal/models/migrator.go:700-705`, `backend/internal/models/migrator.go:786-790`; `backend/internal/models/dbdownload.go:46-75`.
- **Concrete trigger/state:** A PostgreSQL target does not have `idx_db_download_tasks_active_target` (or `idx_db_upload_tasks_active_target`) and lacks permission or otherwise fails while creating it. This is an expected post-import state for this protocol: these are partial unique indexes created only by `ensureActiveTransferTaskUniqueIndexes`; neither `DbDownloadTask`'s `DedupScopeHash` / `DedupLocatorHash` fields nor `DbUploadTask.RemoteFullPath` declares the partial unique index in a GORM tag (`dbdownload.go:62-63`, `dbupload.go:84`), so `migrateTargetSchema`'s `AutoMigrate` does not establish it.
- **Observed control flow:**
  1. `importMigrationArchive` commits `migrateTargetSchema`, reverse-order clearing, row import, and sequence repair in its transaction (`artifact.go:114-127`).
  2. It then calls `migrateImportedDatabase` *after* that commit (`artifact.go:128-131`).
  3. `migrateImportedDatabase` calls `models.Migrate()` (`artifact.go:139-143`). At current `MaxVersionCode`, `Migrate` calls `ensureActiveTransferTaskUniqueIndexes` (`migrator.go:700-705`).
  4. `models.Migrate()` has no error result. When this call fails it logs and returns (`migrator.go:700-704`); `migrateImportedDatabase` does not observe the failure. Its following `Migrator.VersionCode == MaxVersionCode` check remains true because no version increment was required (`artifact.go:144-149`).
  5. Assuming stale-Emby cleanup succeeds, `migrateImportedDatabase` returns nil and `importMigrationArchive` removes `migrate.zip` (`artifact.go:150-153`, `133-135`). Startup then continues into normal runtime (`main.go:950-979`).
- **Expected contract:** Database guidelines §3 and §4 require schema preparation, clearing, import, and sequence repair in one transaction; any schema failure must roll back, retain the package, block startup, and preserve pre-import target data. They also require startup failure on every schema failure. A target must not be accepted after required index setup failed.
- **Why existing tests miss it:** `artifact_test.go:91-120` verifies insert rollback only with an in-memory SQLite target. The successful round-trip test passes `postImport=nil` (`artifact_test.go:122-168`). The only post-import failure test injects a callback that returns an error and asserts only archive retention (`artifact_test.go:211-228`); it neither invokes `models.Migrate` nor asserts target rollback. No PostgreSQL test injects an error from the required partial-index creation step, checks that it propagates, or checks that the archive stays present and startup remains blocked.
- **Assessment:** Confirmed by the call graph and explicit error-swallowing branch. This violates both the transaction boundary and startup-failure contract. The concrete permission/index-creation failure is one reachable manifestation; historical version upgrade operations in `models.Migrate()` have the same post-commit, non-atomic boundary.

## Contract evidence checked

- **Archive isolation and exact contract:** The migration package is independently produced/validated; it uses `format="migration"`, version `1`, `manifest.json`, and `tables/<stable-id>.jsonl` (`artifact.go:27-35`, `183-210`, `599-601`). It does not invoke `internal/backup` restore. `BackupRecord` is deliberately included because the migration catalog selects all `AllTables` (`table_catalog.go:45-49`, `62-65`).
- **Preflight before target connection:** `main.go:936-948` runs `PreflightPendingMigration` before `startDatabase(false, false)`. Preflight opens and fully validates every table file, digest, record count, JSON line size and schema before returning (`artifact.go:66-74`, `357-478`); import repeats validation (`108-112`).
- **Exact catalog and package shape:** Catalog entries are derived in `AllTables` order with physical table name as stable ID (`table_catalog.go:52-68`); validation rejects path/name/order drift, missing files, extra files, duplicates, unknown/invalid record columns, bad digests, and record-count mismatches (`artifact.go:377-426`, `451-478`, `627-657`). Tests cover missing catalog data, physical-name drift, malformed JSONL, and missing persisted columns (`artifact_test.go:20-89`).
- **Clear/import order and rollback:** Tables are cleared in reverse catalog order (`artifact.go:490-499`) and imported in catalog order (`501-528`). The catalog places `User` before `ApiKey` (`migrator.go:31-40`); the foreign-key ordering test passes (`artifact_test.go:170-209`). Insert failure rolls back and retains the package in SQLite test coverage (`artifact_test.go:91-120`).
- **Sequence repair:** PostgreSQL repair runs before the transaction commits, for every catalog table, through `setval(?::regclass, ?, ?)` with the empty-table `is_called=false` case (`artifact.go:578-597`). SQLite is deliberately a no-op (`579-581`).
- **Package deletion and normal-runtime blocking:** The archive is deleted only after import and post-import completion (`artifact.go:128-136`), and deletion failure returns an error. Preflight/import errors return `false` from environment initialization before `configureInitialAdminSetup`, cache initialization, or `initOthers` (`main.go:940-979`), blocking normal business runtime.

## Rejected speculative concerns

- **Rejected — PostgreSQL DDL cannot be part of the GORM transaction.** The active transaction is passed directly to `AutoMigrate` (`artifact.go:114-124`). GORM’s transaction implementation commits only after the callback succeeds, and its migrator uses the database handle as its execution handle. PostgreSQL supports transactional DDL; no contrary code evidence was found.
- **Rejected — public import may execute against SQLite.** `ImportPendingMigration` rejects a nil/non-PostgreSQL global connection (`artifact.go:92-94`). SQLite appears only in the unexported test helper path (`artifact_test.go:265-274`) for deterministic archive/rollback coverage, consistent with the guideline.
- **Rejected — foreign-key clearing is definitely in the wrong order.** The catalog’s only verified physical FK relationship (`api_keys.user_id -> users.id`) imports parent first and clears child first; the dedicated test enables SQLite foreign keys and succeeds (`artifact_test.go:170-209`). No additional physical FK declarations were found in the reviewed model scope.
