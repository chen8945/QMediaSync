# Research: artifact crypto review

- **Query**: Review only `dd4795a^..dd4795a`, concentrating on backup artifact archive traversal/zip bombs, password cryptography, authenticated integrity, atomic publication, path containment, cleanup, retention, errors, and corruption handling.
- **Scope**: internal
- **Date**: 2026-07-29
- **Commit reviewed**: `dd4795a` (`feat: 提升备份与恢复可靠性`)

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/backup/artifact.go` | v1 artifact format, size limits, manifest/path validation, encryption metadata validation |
| `backend/internal/backup/artifact_crypto.go` | Argon2id derivation and chunked AES-256-GCM payload encryption/decryption |
| `backend/internal/backup/artifact_verify.go` | outer/inner ZIP validation, integrity checks, and verified-inner staging |
| `backend/internal/backup/artifact_container.go` | outer container construction and inner-archive pre-publication verification |
| `backend/internal/backup/artifact_io.go` | restricted temporary files and atomic publication primitive |
| `backend/internal/backup/artifact_sources.go` | config/log allowlist collection and symlink rejection |
| `backend/internal/backup/restore_preflight.go` | preflight verification and disk-space check invocation |
| `backend/internal/backup/restore_apply.go` | verified inner archive application and whitelist mirroring |
| `backend/internal/backup/restore_operation.go` | confirm-to-restore flow and rollback handling |
| `backend/internal/controllers/backup.go` | record and upload restore call sites |
| `backend/internal/backup/artifact_test.go` | artifact encryption, source, and writer tests |
| `backend/internal/backup/artifact_verify_test.go` | ZIP shape, path, manifest, digest, and catalog validation tests |
| `backend/internal/backup/restore_flow_test.go` | preflight/apply/rollback flow tests |

### Confirmed / Plausible Candidates

#### 1. High — unencrypted v1 artifacts provide no authenticated integrity, despite passing the three-way instance-key check

- **Location**: `backend/internal/backup/artifact_verify.go:264-268`; `backend/internal/backup/artifact.go:115-122`; `backend/internal/backup/artifact_crypto.go:111-119`; caller path `backend/internal/controllers/backup.go:430-478`.
- **Concrete trigger/state**: An attacker obtains any unencrypted backup from an instance. This is sufficient because every unencrypted artifact includes `config/encryption.key` (required by `artifact.go:211-217` and documented in `docs/operations/database.md:31-33`). The attacker changes a JSONL record, for example an administrator password hash or API-key row, recomputes the affected manifest SHA-256, rebuilds the inner ZIP, and recomputes `payload_sha256` and `encryption_key_fingerprint` in `header.json`. The attacker then uploads that ZIP and induces an administrator to complete the explicitly-confirmed restore.
- **Observed control flow**: For unencrypted artifacts, `decryptArtifactPayload` only copies the payload after length validation (`artifact_crypto.go:111-119`). `VerifyArtifact` trusts the unkeyed header and manifest hashes, reads the archived encryption key, and accepts when the header fingerprint equals both that archived key and the current instance key (`artifact_verify.go:264-268`). A copied unencrypted artifact discloses the exact current key required to satisfy this comparison. On successful preflight/confirm, `applyRestoreArtifact` imports all catalog records (`restore_apply.go:203-221`), including credential hashes and confidential persisted fields by design (`record_codec.go:17-20`).
- **Expected contract**: A v1 artifact accepted as intact for restore must be authenticated against post-export modification. Integrity hashes alone detect accidental corruption but do not authenticate an attacker who can rewrite the container. The explicit unencrypted confirmation in `docs/operations/database.md:33` addresses confidentiality disclosure; it does not make arbitrary modified content authentic.
- **Why existing tests miss it**: `TestVerifyArtifactRejectsInnerManifestDigestMismatch` (`artifact_verify_test.go:195-216`) replaces a file without updating the manifest. `TestVerifyArtifactRequiresTableCatalogKeyAndThreeWayFingerprint` (`artifact_verify_test.go:100-150`) changes the archived key but retains a header for a different key. Neither test creates a self-consistent unencrypted container using the disclosed `config/encryption.key`, recomputed manifest entries, and recomputed outer header digest.

#### 2. Medium — compressed inner ZIP can consume up to 4 GiB during restore while the preflight space check reserves only compressed payload size

- **Location**: `backend/internal/backup/artifact.go:28-31`; `backend/internal/backup/artifact_verify.go:203-213`; `backend/internal/backup/restore_preflight.go:60-61`; `backend/internal/backup/restore_target.go:181-214`; `backend/internal/backup/restore_apply.go:227-267`.
- **Concrete trigger/state**: A valid same-instance artifact has a small `payload.bin` containing an inner Deflate ZIP whose allowed `config/logs/large.log` expands to near `artifactMaxInnerSize` (4 GiB). The upload and outer payload limits permit this: the outer payload is the compressed inner ZIP and can remain far below 1 GiB. The target filesystem has enough free space for the compressed `header.Encryption.PlaintextSize` plus its margin, but less than the expanded log plus the restore staging copy.
- **Observed control flow**: The inner verifier explicitly accepts a total uncompressed archive size through 4 GiB (`artifact_verify.go:203-213`). `runRestorePreflight` supplies only the compressed-inner payload size to `checkRestoreTargetReady` (`restore_preflight.go:60-61`), which reserves `payloadPlaintextSize + currentSize + 64 MiB` (`restore_target.go:209-214`). After a snapshot has been created, `mirrorWhitelistFiles` expands each allowed log into `StateDir()/work` (`restore_apply.go:227-267`) and then atomically copies it into `config/logs`. The check therefore can pass even though applying the verified artifact exhausts local storage; the process has already entered maintenance and must attempt rollback.
- **Expected contract**: The documented preflight requires enough space for the decrypted inner archive, pre-restore snapshot, and temporary restore database (`docs/operations/database.md:45-47`; `restore_target.go:29-31`). For a ZIP container, the required amount must account for all materialized uncompressed data, including staged whitelist logs, not only the compressed archive length. This is a zip-bomb/resource-exhaustion control gap rather than an archive traversal issue.
- **Why existing tests miss it**: `TestArtifactHeaderValidateEnforcesResourceLimits` (`artifact_test.go:114-177`) validates declared size limits, and `TestCheckSqliteTargetReadyValidatesWritabilityAndSpace` (`restore_target_test.go:152-176`) tests an artificially huge *payload* size. No test creates a highly compressible but valid 4-GiB-uncompressed inner entry, checks that preflight rejects insufficient expanded-space capacity, or verifies that log staging cannot exceed the space reservation.

### Code Patterns Observed

- Archive paths are normalized, slash-only relative paths; absolute paths and `..` traversal are rejected in `validateArtifactArchivePath` (`artifact.go:235-242`). Config output is additionally checked against a staging root before restore (`restore_apply.go:247-250`).
- Config-source collection rejects symlinked config roots, files, logs directories, and log entries (`artifact_sources.go:81-175`); SQLite target resolution performs both lexical and existing-component symlink containment checks (`restore_target.go:82-97`, `337-374`).
- Encrypted payloads use fixed-cost Argon2id (64 MiB, three iterations, four lanes) and AES-256-GCM per 1 MiB chunk. The artifact ID and chunk sequence are AAD (`artifact_crypto.go:38-46`, `61-102`, `144-165`, `183-191`).
- Outer payload SHA-256 is checked before decryption (`artifact_verify.go:162-168`), and inner entries are streamed with exact size, SHA-256, JSONL schema, and record-count validation (`artifact_verify.go:234-268`, `302-360`).
- New artifacts and JSONL staging use temp-file, `fsync`, rename, and directory sync publication (`artifact_io.go:25-79`). Restore uses a snapshot then explicit phase records and rollback after post-snapshot failures (`restore_operation.go:141-195`, `252-287`).

### Related Specs

- `docs/operations/database.md:25-49` — v1 artifact format, encryption warning, staging/publication, resource and restore contracts.
- `docs/engineering/verification.md:19-62` — applicable Go package and race-test validation commands.

## Caveats / Rejected Concerns

- **Rejected: ZIP traversal through manifest or inner entries.** The manifest and ZIP entry validators reject non-normalized, absolute, backslash, directory, and symlink entries; allowed restore output paths are restricted to the fixed config allowlist. The focused traversal tests cover malformed outer and inner paths (`artifact_verify_test.go:19-98`) and a symlinked SQLite target (`restore_target_test.go:106-118`).
- **Rejected: encrypted payload alteration is accepted.** AES-GCM authenticates each chunk with artifact-ID/sequence AAD, outer payload SHA-256 is checked before decrypting, and tests cover wrong passwords and bit-flipped ciphertext (`artifact_test.go:238-307`). The first finding applies specifically to the intentionally supported unencrypted format.
- **Rejected: obvious partial-artifact publication on ordinary write errors.** Artifact writing stages restricted temp files, syncs them, renames only after successful close, and cleans failed temporaries (`artifact_io.go:35-79`; `artifact_container.go:89-150`).
- **Not assessed as a product defect: no aggregate upload-staging quota.** `StageUploadArtifact` deliberately documents a per-artifact-only policy (`staging.go:25-27`) and its test locks that behavior in (`staging_test.go:13-54`). It remains an operational capacity concern, but is an explicit design choice rather than an accidental regression evidenced by this commit.

## Verification Performed

- `(cd backend && go test ./internal/backup)` — passed.
