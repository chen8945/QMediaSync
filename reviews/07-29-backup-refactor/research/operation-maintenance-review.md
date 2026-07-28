# Operation / Maintenance Review: dd4795a^..dd4795a

- **Scope**: Backend operation coordinator, task admission/worker lifecycle, maintenance middleware, restore exit path, and relevant callers/tests.
- **Date**: 2026-07-29
- **Commit reviewed**: `dd4795a^..dd4795a` (`feat: 提升备份与恢复可靠性`)
- **Authority read**: `docs/operations/database.md`, especially the operation mutual-exclusion, task-quiescence, maintenance, and restore-exit contracts at lines 25-49.

## Validation evidence

- `go test -race ./internal/backup ./internal/taskgate` passed.
- `go test -race ./internal/models ./internal/synccron ./internal/syncstrm ./internal/directoryupload ./internal/emby` passed.
- Focused maintenance-controller race tests passed:
  `go test -race ./internal/controllers -run 'TestMaintenanceMiddlewareBlocksBusinessApisAndKeepsStatusReadable|TestBackupOperationEndpointsConflictBeforeMaintenance|TestBackupStatusRequiresOperationIDAndToken|TestRollbackFailedDiagnosticStatusStillRequiresOperationIDAndToken'`.
- `git diff --check dd4795a^..dd4795a` passed.
- A broad `go test -race` invocation including all `internal/controllers` tests failed in unrelated credential-session timing tests (`TestChangeCredentialsRejectsSessionRevokedWhileWaitingForLock`, `TestChangeCredentialsRevokesConcurrentOldPasswordLogin`); the focused maintenance tests passed. No race detector report was emitted in the focused packages.

## Candidates

### 1. High — maintenance does not drain business handlers admitted before the flag flips

- **Location**: `backend/internal/controllers/maintenance.go:20-31`; maintenance starts from `backend/internal/backup/operation_backup.go:141-151` and `backend/internal/backup/restore_operation.go:227-245`.
- **Concrete trigger/state**: A mutating authenticated API request passes `MaintenanceMiddleware` while `backup.InMaintenance()` is false, then blocks on I/O, a DB lock, or application work. A backup/restore reaches task-idle, enables maintenance, and starts export or creates/applies the restore snapshot before that original request returns.
- **Observed control flow**: The middleware makes a one-time Boolean check and immediately executes `c.Next()` (`maintenance.go:21-23`); it records no in-flight request lease. The operation coordinator only stops task subsystems (`tasks.go:130-142`) and has no corresponding HTTP-handler wait. Thus a request which passed before `SetMaintenance(..., true)` remains free to read/write the business DB and mutable files while `exportArtifact` or `applyRestoreArtifact` runs.
- **Expected contract**: The operations document requires task quiescence before a consistent export and enables maintenance for all business APIs during backup/restore (`docs/operations/database.md:35,39-41,45-49`). Restore in particular must not concurrently apply its DB/file replacement while a pre-maintenance business handler is still mutating the old runtime state.
- **Why existing tests miss it**: `maintenance_test.go:61-112` creates requests only after `enterMaintenance` has completed. It verifies rejection of *new* requests, not a handler admitted before maintenance that remains in progress across the transition. `tasks_test.go` only models internal task counters.

### 2. High — download queue replacement can orphan an executing worker from the maintenance wait

- **Location**: `backend/internal/models/settings.go:253-267`; `backend/internal/models/download.go:51-58`; `backend/internal/backup/tasks.go:222-228`.
- **Concrete trigger/state**: A manual backup/restore has passed its initial `RunningTasks()` check but its asynchronous operation goroutine has not yet called `globalTaskBarrier.Block()`. Concurrently, `POST /api/setting/threads` executes `Settings.UpdateThreads`, which calls `InitDQ`. The old download queue has dispatched, or dispatches while this replacement is occurring, a worker in `DbDownloadTask.Download`.
- **Observed control flow**: `InitDQ` is not protected by `taskgate.Admit`; it calls only `oldQueue.Stop()` (not `StopAndWait()`), overwrites `models.GlobalDownloadQueue`, and starts a replacement queue (`download.go:51-58`). When the maintenance barrier later invokes `stopDownloadQueue`, it can only call `StopAndWait` on the replacement now stored in `GlobalDownloadQueue` (`tasks.go:222-224`). The worker from the overwritten queue is no longer reachable through that pointer or included in the new queue's `workerWG`; it can continue downloading and updating `db_download_tasks` while the backup/restore proceeds.
- **Expected contract**: The maintenance barrier promises it waits for actual transfer-worker quiescence, not merely that a stop was requested (`docs/operations/database.md:35,41`). The commit's own queue comment makes this requirement explicit: `StopAndWait` must cover dispatched workers before snapshot (`download.go:129-134`).
- **Why existing tests miss it**: `queue_admission_test.go:19-34` tests `Start` after a closed gate, and lines 194-231 test `StopAndWait` on a stable queue pointer. Neither test races `InitDQ`/global-pointer replacement with barrier blocking or verifies the old queue's worker remains tracked.

### 3. Medium — manual backup/restore task-conflict decision is not atomic with task admission

- **Location**: `backend/internal/backup/operation_backup.go:52-69`; `backend/internal/backup/restore_operation.go:60-114`; `backend/internal/backup/tasks.go:130-142`; `backend/internal/taskgate/admission.go:23-58`.
- **Concrete trigger/state**: `StartManualBackup` (or `ConfirmRestore`) observes zero `RunningTasks`, then a new internal task succeeds at `taskgate.Admit` before the background operation invokes `globalTaskBarrier.Block`. This is especially likely because manual backup returns immediately after launching `go runBackupOperation` (`operation_backup.go:69`) and restore similarly launches `go runRestoreOperation` (`restore_operation.go:108`).
- **Observed control flow**: The request takes a non-atomic snapshot of task activity, obtains the coordinator, and only later the background goroutine closes task admission. The new task is validly admitted in that gap. `BlockNewTasks` correctly waits for that admission lease to finish, but then `waitForIdleTasks` waits for the task; manual backup has `idleWait == 0` (`operation_backup.go:64-69,200-206`) and restore waits on `context.Background()` (`restore_operation.go:214-225`). The request has already returned `202`, rather than the documented `409` conflict.
- **Expected contract**: Manual backup and restore encountering running work must return HTTP 409, neither enter maintenance nor wait; only scheduled backup is allowed to wait up to 30 minutes (`docs/operations/database.md:41`). The taskgate's stated purpose is to prevent precisely this admission race (`taskgate/admission.go:23-29`).
- **Why existing tests miss it**: `operation_backup_test.go:55-68` fixes `inFlight` before calling the API; `tasks_test.go:18-74` exercises a barrier after a pre-existing counter. Neither interleaves a successful `Admit` after the request's initial `RunningTasks` check but before `Block`.

### 4. Medium — restore resumes all workers before recording the terminal operation and lifting maintenance

- **Location**: `backend/internal/backup/restore_operation.go:158-166` and `199-206`; ordering contract in `backend/internal/backup/tasks.go:166-180`.
- **Concrete trigger/state**: `prepareRestore` has already enabled maintenance, but fails before returning a snapshot — for example, `RecordPhase(OperationPhaseValidated)` or `CreateRestoreSnapshot` fails after `SetMaintenance(true)` (`restore_operation.go:227-245`). The same ordering is used if the restore goroutine panics before snapshot assignment.
- **Observed control flow**: The failure path calls `globalTaskBarrier.Resume()` first (`restore_operation.go:164`, `203`), which calls `taskgate.AllowNewTasks` and starts queue/cron/runtime services (`tasks.go:167-180`), and only then records the failed terminal state via `finishOperation`. The coordinator clears maintenance inside that terminal transition (`operation.go:457-467`). Therefore cron callbacks or queue workers can be admitted and begin business work during the interval in which maintenance still rejects all HTTP APIs.
- **Expected contract**: `taskBarrier.Resume` itself documents the opposite ordering: “终态已经解除维护，先恢复准入再重新启动各子系统” (`tasks.go:175-179`). The normal backup path also defers `Resume` until after `finishOperation` on every return (`operation_backup.go:135-139,170-188`).
- **Why existing tests miss it**: Restore-flow tests test panic rollback and terminal state (`restore_flow_test.go:329-348`) but replace the orderly-exit hook and do not instrument `taskgate`, cron initialization, or queue start to assert that resume occurs only after terminal maintenance release.

### 5. Medium — a concurrent operation can consume a valid restore preflight token without accepting the restore

- **Location**: `backend/internal/backup/restore_operation.go:93-105`.
- **Concrete trigger/state**: A user submits restore confirmation. Full artifact verification and target checks finish; then another request or scheduled backup obtains the coordinator before this confirmation does. The confirmation's preflight record is still valid at that moment.
- **Observed control flow**: `ConfirmRestore` calls `ConsumePreflight` at line 99, which persistently marks the single-use record consumed, and only afterwards calls `coordinator.Begin` at line 103. If `Begin` returns `ErrOperationInProgress`, no restore has been accepted or started, but the user cannot retry after the competing operation finishes because their preflight ID was burned.
- **Expected contract**: The function comment says it should “原子创建 operation” only once all checks are consistent (`restore_operation.go:52-55`), while `ConsumePreflight` defines the ID as single-use (`preflight.go:100-134`). A conflict that accepts no restore should not irreversibly consume its confirmation credential.
- **Why existing tests miss it**: `preflight_test.go` and `restore_flow_test.go:80-123` test replay rejection and normal consumption, but no test concurrently transitions the coordinator to active between `ConsumePreflight` and `Begin`.

## Rejected concerns

- **Rejected — `taskgate.BlockNewTasks` has a simple check-then-lock admission race.** It rechecks the atomic blocked flag after acquiring `RLock`, while `BlockNewTasks` stores true then obtains the writer lock (`taskgate/admission.go:25-29,45-58`). An admission which observed false before the store either completes its registration before the writer lock is acquired or retries and is rejected; this is the intended lease protocol and is covered by `admission_test.go:29-67`.
- **Rejected — cron-stop contexts are read without synchronization.** `cronStopped` writes occur under the barrier mutex and `runningCronCount` locks the same mutex (`tasks.go:183-213`); the implementation explicitly avoids a data race.
- **Rejected as separate finding — state-file persistence precedes publication of the maintenance atomic.** `SetMaintenance` persists before `maintenance.Store` (`operation.go:533-541`), which creates a very small additional visibility window. It is not reported independently because Candidate 1 is broader: any request admitted by the one-shot middleware before the flag becomes visible is never drained, even with reversed store/persist ordering.
