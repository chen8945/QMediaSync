# Research: frontend backup contract review

- **Query**: Review frontend contract evidence for commit `dd4795a^..dd4795a`, including backup UI/API lifecycle, polling, password validation, browser storage, errors, accessibility, and backend compatibility.
- **Scope**: mixed
- **Date**: 2026-07-29

## Findings

### Files Found

| File Path | Description |
|---|---|
| `frontend/src/stores/backup.ts` | In-memory operation token, progress polling, visibility cleanup, terminal handling. |
| `frontend/src/components/AppBackupRecords.vue` | Manual-backup and saved-record restore flows; backup-list snapshot and inventory polling. |
| `frontend/src/components/AppBackupRestore.vue` | Uploaded-artifact preflight/confirm restore flow. |
| `frontend/src/components/AppBackupSettings.vue` | Scheduled-password and unencrypted-confirmation configuration flow. |
| `frontend/src/utils/backupPassword.ts` | Client password validation claimed to be equivalent to Go validation. |
| `backend/internal/controllers/backup.go` | API response/status contracts for create, restore, status, list, and configuration. |
| `backend/internal/requests/backup.go` | Request field and validation contract. |
| `backend/internal/backup/operation*.go` | Operation phase/state machine and maintenance lifecycle. |
| `backend/internal/validation/backup.go` | Authoritative server password validation. |

### Confirmed / Plausible Defect Candidates

#### P2 — Successful manual backup does not refresh the visible records or terminal-result alert

- **Severity**: P2 / Medium (user-visible completion correctness)
- **Frontend locations**: `frontend/src/components/AppBackupRecords.vue:299-347,358-385,602-607`; operation completion is received in `frontend/src/stores/backup.ts:156-160,202-205`.
- **Backend contract**: `backend/internal/backup/operation_backup.go:174-188` writes the completed record before moving the operation terminal; `backend/internal/controllers/backup.go:120-131` makes the backup-list snapshot (including `latest_operation`) the available source for the resulting record and terminal summary.
- **Concrete trigger/state**: Stay on the backup-records page, start a password-protected or explicitly confirmed unencrypted manual backup, wait until the global dialog reports `备份完成！`, and do not change the page/tab, pagination, or document visibility.
- **Observed control flow**:
  1. The accepted response starts only operation-status polling (`AppBackupRecords.vue:290`; `backup.ts:50-66`).
  2. When a `running` status arrives, `isMaintenance` becomes true and the records watcher stops only inventory polling (`AppBackupRecords.vue:602-606`).
  3. On terminal status, the store stops polling and schedules dialog reset (`backup.ts:158-160,202-205`). It does not emit a list refresh or update list state.
  4. The watcher sees maintenance become false, but invokes `syncInventoryPolling()` (`AppBackupRecords.vue:602-607`). With the usual `inventory_status === 'ready'`, that helper immediately stops rather than calls `/backup/list` (`AppBackupRecords.vue:358-376`).
  5. Thus neither the new record nor the `latest_operation` alert appears until an unrelated refresh trigger occurs. There is no backup realtime event registered; global realtime event types contain no backup event (`frontend/src/composables/useRealtimeEvents.ts:4-24`).
- **Expected contract**: Following terminal polling, the record page should re-fetch its authoritative `/backup/list` snapshot when active/visible (or otherwise reliably request it on the next active view) so a completion toast agrees with the rendered records and terminal summary. This is especially relevant because the operation token is intentionally memory-only and the list response is the recovery/terminal-state surface after it is lost.
- **Why existing tests miss it**: `frontend/test/stores/backup.test.ts:77-92` proves the store stops/reset behavior but does not mount the record page or assert a list snapshot after a terminal status. `frontend/test/components/AppBackupRecords.polling.test.ts:28-65` covers only inventory scanning pause/resume/unmount behavior; it never changes store maintenance from running to terminal or checks that a ready inventory is reloaded.

#### P3 — Frontend password validator rejects U+FEFF where the server accepts it

- **Severity**: P3 / Low (front-end/back-end validation incompatibility and misleading immediate feedback)
- **Frontend location**: `frontend/src/utils/backupPassword.ts:11-22`.
- **Backend authority**: `backend/internal/validation/backup.go:12-37` rejects only runes for which Go `unicode.IsSpace` returns true.
- **Concrete trigger/state**: Enter an otherwise valid password containing U+FEFF, for example `BackupPass123﻿`, in manual backup, restore, or scheduled-backup password input.
- **Observed control flow**: The JavaScript `/[\s]/u` expression at `backupPassword.ts:11` matches U+FEFF and returns `不能包含空白字符` at line 18. The Go loop uses `unicode.IsSpace`; Go does **not** classify U+FEFF as space, so `validation.BackupPassword` accepts the same string when its remaining ASCII upper/lower/digit and length requirements are met. The component exits before sending (`AppBackupRecords.vue:254-258`, `AppBackupRestore.vue:167-174`, `AppBackupSettings.vue:155-160`).
- **Expected contract**: The helper explicitly promises equivalence to `validation.BackupPassword` (`backupPassword.ts:2`); it must not client-reject a password the API accepts. A user restoring a valid artifact created by another client can otherwise be blocked before the server can verify it.
- **Why existing tests miss it**: `frontend/test/utils/backupPassword.test.ts:24-28` samples ordinary space, full-width space, tab, and newline only. The corresponding Go test (`backend/internal/validation/backup_test.go:17-19`) has the same limited whitespace set. Neither test exercises U+FEFF or asserts an explicit cross-runtime parity corpus.

### Code Patterns Verified

- Operation credentials are passed only as `operation_id` query data and `X-Backup-Operation-Token` request header (`frontend/src/stores/backup.ts:135-145`), matching the controller (`backend/internal/controllers/backup.go:318-341`). `frontend/src/main.ts:23-26` installs plain Pinia with no persistence plugin; no frontend consumer writes the token to browser storage.
- The restore forms send the required two phases, password, preflight ID, and explicit overwrite confirmation (`AppBackupRestore.vue:179-215`; `AppBackupRecords.vue:476-524`), matching `BackupRestoreRequest` (`backend/internal/requests/backup.go:55-86`) and the controller phase handlers (`backend/internal/controllers/backup.go:378-408,428-478`).
- Progress and inventory loops avoid overlapping work, pause while hidden, and invalidate scheduled callbacks on stop/unmount (`backup.ts:77-102,124-178`; `AppBackupRecords.vue:299-376,610-615`), consistent with `docs/engineering/frontend-development.md:19-25`.
- Scheduled-password clearing/preservation and one-request unencrypted confirmation match the server request and configuration semantics (`AppBackupSettings.vue:155-205`; `backend/internal/models/backup.go:118-127`; `backend/internal/controllers/backup.go:267-297`).

### Tests Run

```text
(cd frontend && pnpm exec vitest run test/utils/backupPassword.test.ts test/stores/backup.test.ts test/components/AppBackupRecords.polling.test.ts test/components/AppBackupSettings.test.ts test/components/AppBackupRestore.test.ts)

5 test files passed; 16 tests passed.
```

### Related Specs

- `docs/engineering/frontend-development.md:19-25` — polling uses authoritative HTTP snapshots, pauses in hidden documents, prevents overlap, and cleans invalid callbacks.
- `docs/engineering/request-validation.md:104,127-130,165-177` — backup validation, password semantics, one-shot unencrypted confirmation, and client/server validation boundary.
- `docs/engineering/verification.md:64-75,101-106` — front-end test and behavior-regression expectations.

## Caveats / Rejected Speculative Concerns

- **Rejected — browser-storage token leakage**: inspected application bootstrap and token request path; no Pinia persistence plugin or storage write is present. The relevant test also asserts no standard storage/cookie leakage.
- **Rejected — restore request/API phase incompatibility**: JSON saved-record restore and multipart uploaded restore use the fields and status treatment expected by the changed backend handlers.
- **Rejected — polling cleanup race**: the generation checks and `pollingInFlight` handling cover hidden-page, stop, and in-flight response cases; the targeted store tests exercise those paths.
- **Rejected as unconfirmed — password error accessibility**: error text lacks an explicit live-region assertion in the tests, but this static review did not establish Element Plus’s rendered `aria-describedby`/announcement behavior. It is not presented as a defect without rendered accessibility evidence.
- The report is limited to `dd4795a^..dd4795a`; the P2 list-refresh issue compares the new lifecycle but does not treat the older fixed-delay refresh as a sufficient prior contract.
