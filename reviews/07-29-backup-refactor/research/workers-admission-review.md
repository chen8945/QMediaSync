# Workers Admission Review

- **Commit reviewed**: `dd4795a^..dd4795a` (`dd4795ab55864a6a180862cbb68e9d2b07bdc92f`)
- **Scope**: maintenance task admission for `directoryupload`, `synccron`, `syncstrm`, `emby`, `taskgate`, transfer-queue HTTP controls, their callers, and focused tests.
- **Date**: 2026-07-29

## Authority and expected contract

- `docs/operations/database.md:39-41` requires that, before maintenance starts, the common admission barrier reject transfer queue HTTP restarts and download-concurrency adjustment; it also requires waiting for real quiescence, not merely issuing a stop request.
- `docs/architecture/upload-and-strm-processing.md:68` requires directory scanning/stability processing not to start during the quiescence window and requires directory-upload persistence to be rejected by the same admission barrier.
- `docs/architecture/emby-library-sync.md:49` requires the shared barrier to reject Emby submissions and wait for already-refreshing work.
- `backend/internal/taskgate/admission.go:23-29,45-58` implements the intended atomic registration protocol: `BlockNewTasks` flips the flag then takes the exclusive lock; a creation/start path must retain an `Admit` release until its running state is visible to the barrier.

## Candidates

### 1. High — thread-settings endpoint can replace the tracked download queue during the admission window

- **Severity**: High
- **Locations**: `backend/internal/controllers/settings.go:557-576`; `backend/internal/models/settings.go:253-267`; `backend/internal/models/download.go:51-58`; admission enforcement in `backend/internal/models/download.go:72-109,136-184`.
- **Concrete trigger/state**:
  1. A download worker of the current `models.GlobalDownloadQueue` is handling a task (or is otherwise still alive).
  2. A caller invokes `POST /api/setting/threads` in the interval after backup/restore calls `taskgate.BlockNewTasks()` but before its maintenance HTTP middleware returns 503.
  3. The handler executes `SettingsGlobal.UpdateThreads`, which persists settings and calls `InitDQ`; `InitDQ` is not admitted and replaces `GlobalDownloadQueue` with a new `DQ`. The new queue's `Start` correctly refuses admission, but the global pointer replacement has already happened. The handler then reports success because `UpdateThreads` returns true.
  4. The maintenance barrier subsequently calls `stopDownloadQueue` (`backend/internal/backup/tasks.go:222-228`) through the new global pointer. It cannot stop or `Wait` for the old, detached queue's workers. The old worker can therefore continue writing its download task/file while export/restore proceeds.
- **Observed control flow**: The new `DQ.Start`/`UpdateConcurrency` admission checks added by this commit protect only the replacement queue. They do not cover the caller's setting write or `InitDQ`'s stop/replacement sequence. `InitDQ` tests `len(old.tasks) > 0`, so an old queue with its sole task already dequeued can be replaced without even being stopped before it becomes unreachable.
- **Expected contract**: Per `database.md:41`, download concurrency adjustment must be rejected during the pre-maintenance admission window, and pre-existing dispatches must remain reachable by `StopAndWait` so quiescence is genuine before snapshot/restore.
- **Why existing tests miss it**: `backend/internal/models/queue_admission_test.go:19-34` tests `DQ.Start` directly, and `:140-192` tests `UpdateConcurrency` directly. `backend/internal/controllers/file_admission_test.go:14-47` covers only queue restart routes. No test invokes `/setting/threads` after `BlockNewTasks`, asserts no setting/queue mutation, or keeps an old worker active while the global queue pointer is replaced and the barrier waits.

### 2. Medium — manual Emby media-info parse can spawn an unadmitted, untracked worker while admissions are closed

- **Severity**: Medium
- **Locations**: `backend/internal/controllers/settings.go:152-162`; `backend/internal/emby/emby.go:811-838`.
- **Concrete trigger/state**:
  1. Backup/restore has closed `taskgate` and is waiting for existing work, but has not yet enabled the HTTP maintenance middleware.
  2. A user calls `POST /api/setting/emby/parse`.
  3. `ParseEmby` has no taskgate check and calls `StartParseEmbyMediaInfo`; that function has no `Admit` call and starts a goroutine at line 827 which invokes `ProcessLibraries`.
- **Observed control flow**: The goroutine is started after the barrier is closed and is not included in `taskBarrier`'s Emby in-flight count (`backend/internal/backup/tasks.go:94-99,277-282`). Its later `AddDownloadTaskFromEmbyMedia` calls are rejected by the model-level admission guard, but the new worker and remote Emby traversal were still admitted; the barrier neither waits for them nor prevents them from starting.
- **Expected contract**: The shared task-admission requirement applies to every worker-creation path during the quiescence window. A request received in this window must not spawn new Emby work; if pre-existing work is allowed to finish, it must be represented in the wait condition.
- **Why existing tests miss it**: The added Emby tests cover the four database-backed Emby item-sync entry points and refresh-task claim registration (`backend/internal/models/emby_library_refresh_task_test.go:2380-2488`). There is no test for `ParseEmby`/`StartParseEmbyMediaInfo` with `taskgate.BlockNewTasks`, nor a test that the backup barrier observes the spawned worker.

## Positive evidence

- Directory monitor persistence uses admission across the processed-file claim and transaction (`backend/internal/directoryupload/service.go:834-869`), while scans reject the closed gate (`scan_executor.go:62-95`) and service shutdown waits runtimes and the scan executor (`scanner.go:130-156`).
- Sync queue enqueue, processor start, resume, new source queue creation, and Cron initialization are guarded (`backend/internal/synccron/newqueue.go:106-145,227-239,486-519,597-629`; `synccron.go:280-431`).
- STRM worker creation is admitted and shutdown waits its worker WaitGroup (`backend/internal/syncstrm/generation_service.go:803-852`). Emby item-sync and refresh claim paths retain admission until the database running state is registered (`backend/internal/emby/emby.go:46-93,263-310,425-475,624-671`; `models/emby_library_refresh_task.go:1672-1705`).
- Transfer queue restart HTTP routes reject the blocked window with HTTP 409 (`backend/internal/controllers/file.go:153-163,277-287`), and queue `StopAndWait` tracks dispatched workers (`models/upload.go:47-79,221-236`; `models/download.go:72-109,129-184`).

## Rejected speculative concerns

- **Rejected — `taskgate` RWMutex deadlock from early manual `releaseAdmission()` plus deferred release.** `Admit` wraps `RUnlock` in `sync.Once` (`taskgate/admission.go:55-58`), so the later defer is idempotent. The early releases in Emby and refresh-claim flows correctly transfer quiescence tracking to persisted running state.
- **Rejected — scan executor could outlive directory service shutdown merely because `Enqueue` uses `IsBlocked` rather than `Admit`.** `StopDirectoryUploadService` cancels all rule contexts and waits both runtimes and `scanExecutor.Wait` (`directoryupload/scanner.go:130-156`); queued/running scans honor those contexts (`scan_executor.go:125-155`). This is not a plausible maintenance write race from the examined paths.
- **Rejected — dynamic `DQ.UpdateConcurrency` itself bypasses maintenance.** It now obtains `taskgate.Admit` before mutating worker count or starting workers (`models/download.go:146-184`), and the focused admission test covers this direct method. Candidate 1 is specifically the separate `UpdateThreads → InitDQ` path.

## Verification

- Passed: `git diff --check dd4795a^..dd4795a`.
- Passed: `(cd backend && go test ./internal/taskgate ./internal/directoryupload ./internal/synccron ./internal/syncstrm ./internal/emby ./internal/controllers ./internal/models)`.
- Passed: `(cd backend && go test -race ./internal/taskgate ./internal/models ./internal/emby ./internal/synccron ./internal/directoryupload ./internal/syncstrm)`.
- Caveat: the combined race command including `./internal/controllers` failed in existing `TestChangeCredentialsRejectsSessionRevokedWhileWaitingForLock` after roughly a minute; it did not report a race and is unrelated to the admission paths reviewed here.
