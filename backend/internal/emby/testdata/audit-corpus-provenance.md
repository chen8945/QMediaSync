# Emby 4.10.1.0 physical item corpus

`audit-corpus.json` replays the archived read-only audit: 15 Movie items, 138
Episode items and four hidden Video parts. The collection library also returned
two source-free BoxSets despite the requested item-type filter; they remain in
the fixture so tests verify the response rather than trusting that filter.

The following archived inputs are under
`.trellis/tasks/10-04-emby-multiversion-delete-audit/research/`:

| Input | SHA-256 | Retained structure |
| --- | --- | --- |
| `live-fixture.json` | `16793fed88f11d5b0e7ffaf2aefe58c0fcd4046d181bc698dbfe6f620183b33b` | 153 explicit-field physical responses, source Id/ItemId and matching SyncFile rows |
| `multipart-fixture.json` | `0b0f6a6339aef01893c5994ec2142b2d835b4919ce2a6326b412ee76f5c20c70` | Movie 19 PartCount=4, parts 20/21/22, Episode 1194 part 1195 |
| `full_incremental_default_fixture.json` | `62b0cddbf9a47c9fa737b42bb254ace8906b55d89ac02a588d3553c2f43acdb4` | Collection responses and pagination cardinalities |

This is a structural replay, not a byte-for-byte recording. Titles, path
components, remote file/parent IDs and pickcodes are replaced with stable fake
values. Shared directories and all item/source/member IDs remain distinct and
consistent. Only the 157 video SyncFile rows are included. URLs use `qms.test`
and contain no credentials. There are no production account details.

Movie 19's PartCount comes from its separately recorded response. Episode 1194's
PartCount=2 is **harness enrichment**, derived from its one captured additional
part: the earlier ordinary query did not request PartCount, and no response
retaining that field for 1194 was archived. The four child responses themselves
are recorded evidence, including their missing SeasonId/SeriesId context.

The test supplies fictional server/user/account/root records, synthetic content
SHA-1 values and HTTP pagination envelopes. Those establish a complete isolated
model setup; they are not claims about observed cloud generation metadata.
The ordinary list excludes all four hidden parts. Requesting Path and PartCount
controls whether those fields are returned, matching the observed API field
selection behavior. No cloud client or live Emby server is used.

The portable fixture can be regenerated from the archived inputs with
`.trellis/tasks/10-04-emby-multiversion-delete-audit/verification/build-audit-corpus.py`.
The Go regression test only needs this checked-in JSON, not the task archive.
