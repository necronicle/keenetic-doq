# Tasks and evidence

- [x] T001 / FR1: Repeat live Gemini probes; update resolver/export assertions, observe RED, replace the 46 mappings and verify GREEN.
- [x] T002 / FR2: Compare public DoQ candidates and Apple addresses; retain only unfiltered Hostux in defaults.
- [x] T003 / FR2: Update both installers and migration tests; observe RED then GREEN for removal of old defaults, primary replenishment, existing Hostux reserve promotion, custom-setting retention and idempotence.
- [x] T004 / FR3: Synchronize docs/changelog and save the complete endpoint comparison with operator sources.

## Requirement-to-evidence mapping

- FR1: TestBuiltin and TestHostsExportAndConfiguredStatus verify new addresses. Live Gemini pages returned HTTP200 and full Google Gemini application HTML via both GeoHide IPs; primary API roots completed TLS. Exactly 46 former MagicDNS names changed; other mappings/masks are unchanged.
- FR2: TestDefault and the two installer migration suites check the Hostux-only pool, former defaults removed from primary/reserve roles, backups, validation, custom setting retention and repeat execution. Comparison: 27 endpoints, 951 DNS attempts plus TLS checks of returned Apple addresses. Hostux: 99/99 Apple queries and 110/110 TLS probes. See verification.md and ../../docs/verification/2026-11-11-doq-apple.csv.
- FR3: Both README examples and installer fresh defaults match Config.Default. All Go race/vet, installer/uninstaller, shellcheck and cross-build checks are rerun for the final release tree. Successful outputs are required before commit/tag publication.
- FR4: Publication is verified by GitHub Actions run conclusions, release tag/commit and attached assets with SHA256SUMS. Telegram confirmation must contain the target chat/topic and sent message ID. These external results are reported after execution, not inferred from this task list.

## Open verification

Authenticated Gemini chat and a signed IPA update on an actual iPhone remain untested. All network measurements describe this source network/time. The running router is unchanged.
