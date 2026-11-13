# Feature Specification: AI hosts through current MagicDNS proxies

Created: 2026-11-13. Branch: fix/ai-magicdns-066. Status: authorized by owner.
Input: move hosts for AI services to 217.144.190.216 and 109.120.187.249, release an update, then announce it in the DoQ Telegram topic 55256. After live probes found regressions, the owner approved moving supported services while retaining old IPs for all other services.
Uses the installed official Spec Kit spec/plan/tasks templates in a scoped existing repository; no project reinitialization. Supersedes the proxy choice of specs/001-gemini-dns-defaults only for this release; ordinary DNS defaults remain unchanged.

## User Scenarios & Testing

### US1 — AI routing (P1)
A user with built-in hosts upgrades and receives both new proxy addresses for supported AI service entries, their supporting API/CDN names, and existing supported wildcard masks. Entries lacking successful TLS checks through both IPs retain their old mappings; unsupported masks also retain their old mappings. Exact names continue to take priority over masks. The final migration boundary is domains.txt; candidate-domains.txt records the full original AI scope: Google AI, OpenAI/ChatGPT/Sora, Anthropic/Claude, AI coding assistants, model platforms, generation and translation services. Shared general-purpose sites are retained unless already part of Google AI's supporting exact-name set.

Acceptance: exported hosts and resolver responses use exactly the requested pair; the set of names/masks and all unrelated address mappings are preserved. Existing custom hosts continue to replace the built-in snapshot.

### US2 — Release and announcement (P1)
Publish v0.6.6 through the existing tagged GitHub workflow with all architecture binaries and checksums, verify published checksums, then post a concise Russian announcement and release link to topic 55256 in @zapret2keenetic.

## Requirements

- FR-001: Every rule in domains.txt has exactly 217.144.190.216 then 109.120.187.249, including explicit OpenAI entries formerly pointing at 217.60.179.6.
- FR-002: Retain old IPs for AI entries not proven supported by the new pair, including the Cursor family and OpenRouter. Preserve all 405 exact names and 234 masks, all unrelated proxy mappings, ordinary upstream configuration and custom-hosts behavior. Do not change the live router.
- FR-003: Update Russian/English docs, export/resolver expectations and changelog. Say that two returned addresses do not implement primary/backup failover.
- FR-004: Record HTTPS status and certificate validation through each IP for migrated exact names and the base names of migrated masks. A probe must complete TLS with a valid certificate through each IP. Record HTTP errors/challenges honestly; these do not prove generation works. DNS proxy coverage is required for migrated masks. HTTP errors/challenges and authenticated generation are separate from network reachability.
- FR-005: Run release checks and all-architecture builds; publish and validate v0.6.6 assets, then send the authorized Telegram announcement.

## Success Criteria

SC-001: all migrated mappings equal the requested pair, unrelated mapping differences are zero, and repository release checks pass.
SC-002: GitHub release contains 11 binaries and SHA256SUMS with matching downloaded hashes.
SC-003: Telegram returns a sent message in topic 55256, containing the published release URL.

## Open Verification

Authenticated AI conversations and API generation require user sessions/keys and are not established by page requests. ISP-specific reachability and availability of third-party proxies remain external conditions. Custom hosts files require manual updates if their owners want these mappings.
