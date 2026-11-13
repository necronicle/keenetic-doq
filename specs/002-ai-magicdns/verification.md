# Verification — 2026-11-13

## Approved scope and mapping (FR-001/002)

The owner approved preserving old addresses for unsupported AI services after the full-migration probe identified regressions. Final domains.txt contains 101 migrated rules: 75 exact names and 26 masks. The original 196-rule AI candidate scope is candidate-domains.txt; all 95 excluded AI rules retain their old addresses.

Whole-table comparison against a30766a confirms exactly 101 changed mappings, 538 unchanged mappings, and the same 405 exact names / 234 masks. Every selected rule has exactly 217.144.190.216 followed by 109.120.187.249. No upstream configuration, custom-hosts logic, unrelated service mapping or live router was changed.

## Live network evidence (FR-004)

- Initial sample: 60 HTTPS requests through the new pair, saved in docs/verification/2026-11-13-ai-magicdns.csv. Gemini and ChatGPT pages return HTTP 200 with valid certificates.
- Comparison: 18 HTTPS requests through the former GeoHide pair, saved in docs/verification/2026-11-13-ai-geohide-comparison.csv. Cursor, OpenRouter and Manus work there while new-IP TLS connections fail. These services retain all prior mappings.
- DNS policy: 196 candidate rules queried over TCP against MagicDNS, including synthetic names for masks, saved in docs/verification/2026-11-13-ai-magicdns-dns.csv. 127 return MagicDNS proxy addresses. Non-proxy answers are not treated as proxy support.
- Extended HTTPS: 158 requests covering all 79 candidate base/exact endpoints, plus 18 retry requests, saved in docs/verification/2026-11-13-ai-magicdns-coverage.csv and docs/verification/2026-11-13-ai-magicdns-retry.csv. Final selection requires a completed HTTP response with a certificate validated for the requested hostname through each IP, plus DNS policy support. Retried transient failures remain visible in the first CSV. Persistent failures keep old mappings, including codeiumdata.com, oaiusercontent.com and its mask, OpenRouter and geller-pa.googleapis.com. Exact files.oaiusercontent.com successfully migrates independently of its retained wildcard.
- API path samples: docs/verification/2026-11-13-ai-api.txt records OpenAI /v1/models returning 401 missing authentication, Gemini /v1beta/models returning 403 permission denied, and Anthropic GET /v1/messages returning 405 method not allowed. Certificates validate through both IPs. These are transport checks without credentials, not generation checks.

## Repository verification (FR-003/005)

- Existing resolver/export assertions updated for new IPs and retained exception mappings.
- README.md, README.en.md and CHANGELOG.md describe the same migration boundary, custom-hosts update behavior and absence of primary/backup failover.
- go vet and go test -race pass; all four installer/uninstaller suites and shellcheck pass.
- All 11 router architectures built using scripts/build-all.sh with version v0.6.6. Final review and GitHub release evidence are recorded below as they become available.

## Independent review

Read-only reviewer independently confirmed all 101 changes, all 538 preserved rules, full counts, TLS/DNS evidence for the final scope, tests and docs. No Critical/Important findings. The single minor finding (plan evidence linked only the initial CSV) was corrected by linking final coverage/retry/DNS evidence.

Review limits retained: authenticated generation, every ISP and future proxy stability are open as described below. These cannot be established by this data-only change or unauthenticated network probes.

## Publication

Completed for source commit e6ba4da53f9285dfdc5fc9ea3c1bef2568a9faf9 (annotated tag v0.6.6):

- Release workflow succeeded: https://github.com/necronicle/keenetic-doq/actions/runs/43401355726
- Source CI succeeded: https://github.com/necronicle/keenetic-doq/actions/runs/43401355616
- Public, non-prerelease, latest release: https://github.com/necronicle/keenetic-doq/releases/tag/v0.6.6
- All 12 assets downloaded: 11 binaries and SHA256SUMS. Every checksum matches the downloaded file; every binary contains the exact final hosts.txt bytes. Go build metadata identifies module version v0.6.6, source revision e6ba4da and an unmodified source tree. Published build toolchain: Go 1.26.10. Version ldflags are not exposed by this toolchain's build metadata; the release source script supplies main.version from the tag.
- Verified checksum manifest copied to docs/verification/2026-11-13-v066-SHA256SUMS.
- Telegram Bot API returned ok=true, message_id=91323, message_thread_id=55256, is_topic_message=true in chat -1003834368989 (@zapret2keenetic). Announcement: https://t.me/zapret2keenetic/91323

SC-001, SC-002 and SC-003 are satisfied by the mapping/test checks, downloaded release assets and Telegram result respectively. Post-publication evidence is recorded on main after the immutable release tag; no tag was moved.

## Open verification

Authenticated AI conversations and API generation require user sessions/keys. A valid TLS connection and HTTP response (including root 403/404/421 or Cloudflare challenge) do not establish authenticated service operation. The final mask probes confirm MagicDNS DNS policy and the base hostname's TLS, not every possible future subdomain. Availability from each ISP and third-party proxy stability remain external conditions. Custom hosts files require manual updates.
