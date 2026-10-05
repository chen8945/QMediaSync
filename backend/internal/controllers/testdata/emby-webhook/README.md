# Emby Webhook captured fixtures

These 12 JSON files are sanitized **captured request bodies**, extracted from the
task's archived receiver wrappers. They are not hand-written examples. The server
was Emby 4.10.1.0 with official Webhooks 1.0.38.0; plugin rounds used
MediaInfoKeeper 1.7.5.5 and Strm Assistant Pro 3.0.0.53. The official baseline had
MediaInfoKeeper installed with the relevant enhancements disabled.

`provenance.json` records each source path (relative to the task's `research/`),
the archived wrapper SHA-256 and transformed fixture SHA-256. `original_body_sha256`
is null where the older receiver did not retain raw request bytes; a reserialized
body is not presented as the original HTTP byte stream.

Sanitization replaces server/user identity and name, local test directory prefixes,
URL hosts and synthetic pickcodes with stable fixture values. Other URL query
parameters, userinfo and fragments are removed. Numeric item IDs, parent/season/
series relationships, missing fields, seven-digit timestamps, Unicode, commas,
literal JSON escapes and actual newline semantics are preserved. No payload was
upgraded to include structured sources or a deletion success flag.

| Fixture | Captured meaning |
| --- | --- |
| official-fast-new / official-fast-deleted | Same Episode 1514, delivered after its actual deletion; Date is not a file generation. |
| official-season / official-series | Parent events contain no child ID list. |
| official-library-root | Library removal emitted Folder deleted while files remained. |
| mik-notification-success | Notification-only deletion, one local source; no User object. |
| sa-notification-success | Notification-only deletion, JSON file path differs from Description directory. |
| sa-notification-failure | Same valid deep format even though original delete returned HTTP 500 and original item/STRM survived. |
| sa-multiple-sources | Multiple local paths displayed as LF-separated text. |
| sa-newline-comma | A single actual filename contains comma and LF; cannot split Mount Paths into authorized files. |
| sa-hidden-video | AdditionalPart Video, one HTTP source with synthetic pickcode. |
| sa-official-mirror | Official notification of the same Movie as sa-notification-success; independent receipts. |

Success/failure and plugin notification-only configuration are facts from the
adjacent archived action records, **not fields in these HTTP bodies**:
`mediainfokeeper/cases/notification-only-local-strm.json`,
`strmassistant/cases/notification-only-local-strm.json`, and
`strmassistant/cases/notification-only-original-failure.json` (204, 204 and 500).
Parser tests check that failure is accepted as evidence, never as proof of success;
worker tests separately control the observed live state.

`library.modified`, malformed JSON, duplicate keys, conflicting server identity,
CRLF variants and credential-bearing URLs in request unit tests are explicitly
**constructed defensive/compatibility cases**. No real modified sample was captured.

The read-only generator is kept in the task's
`verification/build-webhook-fixtures.py`; normal module tests only need these
portable files and do not depend on the ignored task directory or any live service.
