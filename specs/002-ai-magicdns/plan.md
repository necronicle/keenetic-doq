# AI MagicDNS migration implementation plan

Goal: ship the owner's requested AI proxy migration as v0.6.6 and announce it.
Spec: spec.md. Technical context: Go 1.26, embedded hosts, existing GitHub tag workflow.

## Constraints and review focus

Preserve the exact rule set, non-AI mappings, upstreams, custom hosts and router state. Verify exact OpenAI overrides as well as wildcard masks, shared Google/Microsoft exclusions, and published binary contents. Existing DNS response construction retains both addresses without health-based primary/backup semantics.

## Tasks

1. Establish candidate-domains.txt AI boundary; obtain DNS policy coverage and probe candidate endpoints through each IP. Select final domains.txt only where TLS succeeds through both IPs; retain old mappings for failures and unsupported DNS masks. Save all results with failures visible.
2. Rebuild hosts from base a30766a, replacing only selected supported hosts mappings; update existing resolver/export tests to match the requested pair; keep unrelated DNS expectations. Update bilingual documentation and v0.6.6 changelog.
3. Compare entire hosts mapping against base a30766a; run go vet, go test -race, four installer/uninstaller suites, shellcheck and 11 cross-builds. Review the diff before publication.
4. Commit and publish main/tag using existing workflow; verify workflow success, release version, all downloads/checksums and embedded hosts; publish release notes and announce to Telegram topic 55256.

## Requirement evidence

FR-001/002: mapping comparison plus resolver/export tests. FR-003: docs/changelog diff. FR-004: docs/verification/2026-11-13-ai-magicdns.csv plus the final coverage, retry and DNS CSVs (2026-11-13-ai-magicdns-coverage.csv, 2026-11-13-ai-magicdns-retry.csv, 2026-11-13-ai-magicdns-dns.csv). FR-005: verification.md, GitHub workflow/release and Telegram result. Unavailable authenticated generation stays explicitly open.
