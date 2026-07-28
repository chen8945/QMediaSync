# Research: controller-api-security review

- **Query**: Review only `dd4795a^..dd4795a` for controller/API security: authorization, operation tokens, state leakage, multipart bounds/cleanup, validation, response compatibility, paths, and maintenance bypasses.
- **Scope**: internal
- **Date**: 2026-07-29

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/main.go` | Registers CORS, maintenance middleware, unauthenticated status route, and authenticated backup routes. |
| `backend/internal/controllers/backup.go` | Backup/restore acceptance, status-token validation, and multipart upload-restore handler. |
| `backend/internal/controllers/base.go` | Cross-origin request-header allowlist. |
| `backend/internal/backup/staging.go` | Post-parse 1 GiB per-artifact staging limit. |
| `backend/internal/backup/operation.go` | State-token hashing, constant-time authorization, and state leakage controls. |
| `backend/internal/controllers/maintenance.go` | Maintenance exception restricted to `GET /api/backup/status`. |
| `backend/internal/controllers/strm_webhook.go` | API-key authentication and batch admission handling. |
| `backend/internal/helpers/local_secret.go` | Instance-local AES-GCM secret storage and key-text accessor. |
| `docs/architecture/authentication-sessions.md` | Authority for status token and trusted-origin behavior. |
| `docs/operations/database.md` | Authority for restore upload, status access, and maintenance behavior. |

### Confirmed candidates

#### 1. Medium — the new status-token header is not CORS-allowed, breaking documented cross-origin status polling

- **Location**: `backend/internal/controllers/base.go:220-229`; introduced cross-origin dependency at `backend/internal/controllers/backup.go:318-342`; route registration at `backend/main.go:612-614`.
- **Concrete trigger/state**: Deploy the UI on an explicitly configured trusted origin different from the backend (a supported configuration), start a backup or restore, then have the browser poll `/api/backup/status` using the accepted operation ID and `X-Backup-Operation-Token` header.
- **Observed control flow**: The new status contract requires that non-simple header (`backup.go:318-320`, `docs/architecture/authentication-sessions.md:41-43`). The browser therefore sends an `OPTIONS` preflight. `Cors()` accepts a trusted origin but its `Access-Control-Allow-Headers` at `base.go:224` omits `X-Backup-Operation-Token`. The browser rejects the preflight and does not send the authorized `GET`; the status endpoint is the sole business-state endpoint allowed while maintenance is active.
- **Expected contract**: The authority document explicitly supports configured trusted origins (`authentication-sessions.md:49,62`) and requires the operation token in this request header. A trusted cross-origin browser must be able to send that documented header; this is necessary to observe the only permitted status endpoint during maintenance (`database.md:38-41`).
- **Why existing tests miss it**: `maintenance_test.go` directly invokes a Gin router and supplies the header, bypassing browser preflight and `Cors()`. `auth_security_test.go` exercises origin policy but has no `OPTIONS /api/backup/status` assertion requiring `X-Backup-Operation-Token` in `Access-Control-Allow-Headers`. Frontend store tests mock Axios rather than browser CORS enforcement.

#### 2. Medium — multipart parsing accepts an unbounded request before the advertised 1 GiB staging limit is applied

- **Location**: `backend/internal/controllers/backup.go:413-444`; `backend/internal/backup/staging.go:25-66`; server setup at `backend/main.go:94-98,197-209`.
- **Concrete trigger/state**: An authenticated caller sends `POST /api/backup/upload-restore` with `phase=preflight` and a multipart `file` part substantially larger than 1 GiB (or a slow, indefinitely growing chunked body). This needs only an otherwise valid session/API key; the file need not be a valid artifact.
- **Observed control flow**: `UploadAndRestore` first calls `c.ShouldBind(&req)` (`backup.go:413`) and then `c.Request.FormFile("file")` (`backup.go:430`). For multipart requests Gin delegates to `Request.ParseMultipartForm`; Go parses the **whole** body, retaining only a memory threshold and spilling the remainder to OS temporary files. Only after that parsing has completed does `StageUploadArtifact(file)` read the parsed file and limit its copy to `artifactMaxUploadSize` (1 GiB) (`staging.go:32,52-58`). The application defines neither `http.MaxBytesReader` nor a `Content-Length` guard for this route, nor a server request-read timeout/maximum body setting (`main.go:185-188,200-203`). Thus the limit protects the application-owned staging directory but not disk/memory consumed by the request parser before the handler reaches it. Go's HTTP server eventually calls `MultipartForm.RemoveAll()` after the handler, but that cleanup is after the unbounded temporary allocation and cannot prevent disk exhaustion while the body is received.
- **Expected contract**: The new upload path presents a single-artifact 1 GiB boundary (`artifact.go:26`; `staging.go:25-32`) and is part of a recovery endpoint. The HTTP boundary must reject excess data before multipart parsing/spooling, so an authenticated or compromised credential cannot exhaust host temporary storage or tie up request handling merely by bypassing the later copy limit.
- **Why existing tests miss it**: `staging_test.go:13-54` invokes `stageUploadArtifact` directly, so it starts after Gin/net/http multipart parsing. `maintenance_test.go` only verifies conflict behavior and uses form-encoded data. There is no controller/integration test posting an over-limit multipart body and asserting that parsing/staging does not create unbounded temporary data, and no test for a request-body limit.

### Rejected concerns

- **Operation-token replay/state leakage — rejected.** The token is issued with `crypto/rand`, only its SHA-256 is persisted (`operation.go:339-348`), comparisons use `subtle.ConstantTimeCompare` (`operation.go:373-387`), and unauthorized status requests return a uniform no-data error (`backup.go:327-334`). The token is intentionally reusable for polling during the current operation; “one-time” refers to its one-time plaintext delivery, consistent with the authority document.
- **Maintenance bypass through status-route method/path tricks — rejected.** `isMaintenanceExempt` permits only exact-path `GET /api/backup/status` (`maintenance.go:35-43`), and the global middleware precedes the unauthenticated route and JWT/API-key chain (`main.go:612-636`). Tests cover blocked POST status access and ordinary business endpoints (`maintenance_test.go:61-112`).
- **STRM Webhook batch admission bypass — rejected.** The changed batch transaction acquires task admission (`strm_webhook.go:394-400`); single-file and directory paths delegate to model enqueue functions that independently admit/check the same gate (`models/strm_generation_task.go:107-136`).
- **Uploaded-artifact lifecycle cleanup — rejected.** Partial staging files are deleted on copy/sync/close failure (`staging.go:44-66`); failed upload preflight deletes the staged artifact (`restore_preflight.go:34-42`); successful restore deletes it (`restore_operation.go:183-188`); startup/scheduled cleanup invalidates matching preflights (`staging.go:69-91`). This does not remedy candidate 2's pre-handler parser allocation.
- **`local_secret.go` cryptographic regression — rejected.** It uses a per-message random GCM nonce (`local_secret.go:60-75`), validates ciphertext minimum length before open (`local_secret.go:77-102`), and `LocalEncryptionKeyText` returns the trimmed persisted key text needed by the documented instance-fingerprint format rather than exposing it through an HTTP response.

## Verification

- Passed: `git diff --check dd4795a^ dd4795a`.
- Passed: `(cd backend && go test ./internal/controllers ./internal/requests ./internal/validation ./internal/helpers ./internal/backup)`.

## Caveats / Not Found

- No product files were modified.
- The multipart issue is independently supported by the resolved project versions: `backend/go.mod` uses Gin v1.12.0, whose multipart bind/form helpers call `Request.ParseMultipartForm`; Go's multipart implementation parses full file parts and spills excess file data to temporary files. This review did not execute a multi-gigabyte request against the host.
