# Gemini hosts and DNS defaults

Created: 2026-11-11. Scope: owner's requested configuration repair, using installed Spec Kit templates without reinitializing this repository.

## User scenarios and requirements

- US1 / FR1: Gemini and its existing Google AI hosts must use reachable proxies instead of 45.88.163.92. Preserve exact names, wildcard policy and unrelated services. Verify TLS and the Gemini web response through each replacement IP; authenticated generation remains a separate check.
- US2 / FR2: Use unfiltered ordinary upstreams only. Use Hostux as the sole default after DNS and Apple TLS validation; FFMUC is excluded from defaults by explicit owner choice after its returned Apple IPv4 failed TLS. Remove Quad9, NextDNS and Control D from fresh configs and upgrades, including fallback rows; retain all other custom servers and config values. If no primary remains, insert the approved default pool; preserve other custom primary pools. If no primary remains and Hostux is already a fallback, promote that existing row without duplication. Migration must retain backup, validation and idempotence guarantees.
- FR3: Synchronize Russian/English docs, tests and changelog; preserve existing hosts names/masks outside the 46 replacements.
- FR4: Publish all approved changes as v0.6.5 using the existing release workflow, verify release assets/checksums, then announce in the DoQ topic (55256) of zapret2keenetic. Owner explicitly authorized publication and the Telegram message. Do not change the running router.

## Acceptance

Live probes establish replacement reachability. Existing Go DNS resolution/export tests assert the new hosts addresses and defaults. Both installer migration suites cover removal, primary replenishment, preservation of custom rows and repeat execution. Repository tests, vet and relevant shell checks pass.

## Open verification

Authenticated Gemini conversation from a user's phone is not available in this session. A successful page/TLS response is not proof of authenticated generation or availability from every ISP.

## Follow-up acceptance: Apple and unfiltered upstreams

Only add a new resolver after confirming operator-documented no-filter policy, repeated A/AAAA/HTTPS queries for Apple hosts, and TLS using returned IPv4/IPv6 addresses with correct SNI/certificate validation. Record failed candidates as well as successes. Do not equate root HTTP 403/404 with a tested signed app download. Verify an App Store lookup/content sample if accessible. Preserve a separate open item for an authenticated iPhone update.
