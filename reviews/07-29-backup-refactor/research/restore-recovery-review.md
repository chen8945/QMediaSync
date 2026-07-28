# Restore/recovery review — `dd4795a^..dd4795a`

## Scope and authority

- **Reviewed range:** `dd4795a^..dd4795a` only.
- **Authority:** `docs/operations/database.md:43-49,57-66` defines two-phase recovery, snapshot-before-write, automatic rollback, diagnostic-only startup after rollback failure, external token/status state, and orderly exit after restore terminal state.
- **Reviewed implementation/callers:** `backend/internal/backup/{preflight.go,restore_preflight.go,restore_operation.go,restore_apply.go,restore_snapshot.go,restore_recovery.go,restore_target.go,runtime.go,operation.go,operation_backup.go,tasks.go}`, `backend/internal/controllers/{backup.go,maintenance.go}`, and `backend/main.go`.
- **Validation performed:** `git diff --check dd4795a^ dd4795a`; `cd backend && go test ./internal/backup ./internal/controllers` (both passed, cached).

## Confirmed / plausible candidates

### 1. [P1] Restore reopens business traffic before the required process exit, allowing stale-runtime writes or request failures

- **Location:** `backend/internal/backup/restore_operation.go:189-195`; state transition behavior in `backend/internal/backup/operation.go:457-468`; orderly-exit hook in `backend/main.go:929-934`.
- **Concrete trigger/state:** A restore reaches `logs_switched`, then `applyRestoreArtifact` succeeds. This is especially visible when the artifact’s target is not the database currently used by the still-running process, a supported case (`restore_target.go:53-65`). A client sends a normal authenticated API request after the completed status becomes visible but before `QMSApp.Stop()` has shut the HTTP servers down.
- **Observed control flow:** `runRestoreOperation` first calls `finishOperation(..., completed, ...)` at line 189. `OperationCoordinator.Transition` makes any terminal state set `record.Maintenance = false`, persists it, and clears the in-memory maintenance barrier (`operation.go:457-468`). Only after this does line 190 attempt to record `terminal`, and line 194 calls `requestOrderlyExit`. The real hook calls `QMSApp.Stop()` only after the maintenance flag is already false. Thus the middleware starts passing ordinary APIs (`maintenance.go:19-31`) while the old process is still live. If the target was the current SQLite database, it has already been closed at `restore_apply.go:117-122`; if the target differs, the old connection is still open and accepts writes to the pre-restore database.
- **Expected contract:** The authority document says that during recovery business APIs return `503`, only status is allowed, and that after success the process exits because it is holding replaced database/configuration state (`database.md:47-49`). The barrier must remain effective until the old runtime has stopped accepting business work; terminal status is still available because the status route is explicitly exempt.
- **Why existing tests miss it:** `restore_flow_test.go:152-235` calls `applyRestoreArtifact` directly and does not run the operation completion/exit sequence. `operation_test.go:53-100` explicitly asserts that a terminal state leaves maintenance, but does not exercise a live Gin server, queued concurrent request, or the shutdown hook. Controller maintenance tests verify only that the flag blocks traffic, not the terminal-to-exit ordering.

### 2. [P2] PostgreSQL rollback does not restore the prior schema when recovery had to create missing tables

- **Location:** snapshot capture at `backend/internal/backup/restore_snapshot.go:201-212`; PostgreSQL restore schema creation at `backend/internal/backup/restore_apply.go:152-169,194-201`; rollback at `backend/internal/backup/restore_snapshot.go:277-310`.
- **Concrete trigger/state:** Restore a PostgreSQL artifact into a recoverable but not fully initialized target database where at least one table in `models.MasterTableCatalog()` is absent. This is explicitly supported by `ensureRestoreSchema`’s comment and behavior. A later import/config/log step then fails, so automatic rollback runs.
- **Observed control flow:** The snapshot captures JSONL only for tables that existed at snapshot time and records their IDs (`capturePostgres`, lines 201-212). During apply, `ensureRestoreSchema` invokes `createRestoreSchema(connection)` as soon as any master table is absent, and `AutoMigrate(models.AllTables...)` creates the missing schema (`restore_apply.go:152-155,194-200`). On failure, `rollbackPostgres` clears all catalog tables, re-imports only snapshot JSONL files that exist, then repairs sequences (`restore_snapshot.go:284-305`). It never drops tables absent from the snapshot, and `meta.PostgresTables` is not consulted. The target consequently retains newly created empty tables after a purported rollback.
- **Expected contract:** `database.md:47` requires any post-snapshot failure to automatically roll back database and files; `database.md:61-64` describes restoring the pre-recovery target database before resuming service. A pre-restore target missing tables is not restored to its prior database state if rollback leaves added schema behind.
- **Why existing tests miss it:** Snapshot/rollback tests exercise SQLite only (`restore_snapshot_test.go:37-142`). The only PostgreSQL-related snapshot test intentionally uses an unreachable PostgreSQL target and asserts that snapshot creation fails before capture (`restore_snapshot_test.go:160-200`). No test creates a partial PostgreSQL schema, applies `ensureRestoreSchema`, and verifies rollback removes the newly added tables.

### 3. [P2] The confirm handoff can consume a valid one-time preflight credential without accepting a recovery operation

- **Location:** `backend/internal/backup/restore_operation.go:63-115`, specifically preflight consumption at lines 93-101 before coordinator acquisition at lines 103-106; corroborating stated contract at lines 52-54.
- **Concrete trigger/state:** A valid preflight is submitted for confirm while another goroutine starts an operation after the confirm’s initial `coordinator.Active() == nil` check (line 67) but before its `coordinator.Begin` call. For example, a manual backup is accepted during the confirm request’s second artifact verification/target probe.
- **Observed control flow:** Confirm revalidates the artifact, calls `ConsumePreflight`, and durably marks the credential `Used=true` before trying `coordinator.Begin`. If the other operation wins `Begin`, this confirm receives `ErrOperationInProgress`, but the preflight ID has already been consumed. The caller cannot retry after the conflicting operation finishes; it must repeat the expensive preflight and password submission.
- **Expected contract:** The implementation comment promises that all conditions must agree before it “consumes that ID, atomically creates operation, and transfers” the work (`restore_operation.go:52-54`). The authority document also frames `preflight_id` as the confirm-stage handoff (`database.md:45`). The current preflight-store mutex and coordinator mutex are independent, so consumption and acceptance are not atomic as stated.
- **Why existing tests miss it:** `preflight_test.go:13-54` tests one-time/source-bound consumption in isolation. `operation_test.go:11-51` tests coordinator serialization in isolation. `restore_flow_test.go:80-124` consumes a preflight directly rather than racing confirm with another coordinator acquisition. None schedules concurrent confirm/backup requests across the check–consume–begin window.

## Checked concerns rejected as findings

### Rejected: interruption after snapshot creation but before `snapshot_ready` is recorded

`CreateRestoreSnapshot` completes and writes/fsyncs its metadata before `prepareRestore` records `snapshot_ready` (`restore_snapshot.go:80-92`; `restore_operation.go:240-246`). A crash in the small gap leaves an unused snapshot but no database/configuration write has started. Startup correctly takes the no-write branch and removes stale snapshots (`restore_recovery.go:68-73`). This does not violate the rollback contract.

### Rejected: restoration may apply database and files in an unsafe order

The observed order is database, non-log whitelist configuration/keys, then logs, with a durable phase after each step (`restore_apply.go:20-55`). The snapshot is created before this sequence (`restore_operation.go:240-249`), and both immediate and startup recovery restore database before configuration/files (`restore_snapshot.go:124-131`; `database.md:47,61-63`). This matches the documented contract.

### Rejected: successful or failed recovery loses status token/state when restored configuration changes

Operation state, token hash, and rollback snapshots live under `config/state/backup`, which is outside the restorable whitelist (`runtime.go:26-30,49-53`; `operation.go:19-24`). The status endpoint reads that external state and requires the operation ID plus token (`controllers/backup.go:322-341`). This matches `database.md:29,39`.

### Rejected: a failed restore after snapshot readiness can continue serving without a rollback attempt

Normal errors call `rollbackAfterFailedRestore` (`restore_operation.go:169-180`), panics use the same path when a snapshot exists (`restore_operation.go:197-207`), and interrupted startup calls `snapshot.Rollback` or enters diagnostics-only mode (`restore_recovery.go:76-105`). The tested SQLite paths cover immediate panic and startup rollback (`restore_flow_test.go:282-347`).
