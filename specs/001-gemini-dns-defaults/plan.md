# Implementation and release plan

Go 1.26, embedded hosts, POSIX shell installers; no new dependencies.

1. Replace the 46 unavailable MagicDNS mappings with both verified GeoHide IPs. Preserve all other names and wildcard policy.
2. Use Hostux alone as the default. Both installers remove the exact former Quad9/NextDNS/Control D URLs, validate and back up the staged config, preserve other custom servers, and ensure one primary without duplicating an existing Hostux fallback.
3. Update existing tests before implementation, observe RED then GREEN, synchronize both READMEs and v0.6.5 changelog, and retain the 27-endpoint Apple comparison in docs/verification.
4. Run all CI/release checks locally, independent code review, and versioned builds for all 11 router architectures. Commit and push the approved changes to main after verifying the remote base.
5. Wait for main CI, push tag v0.6.5, verify the release workflow and published assets/checksums, set the release notes, and send the announcement as a reply to Telegram topic 55256.

Review focus: preserving custom settings, exact retired URLs, Hostux fallback promotion only when no primary remains, migration validation/backup/idempotence, no broad Google masks, synchronized defaults, and no claim that TLS proves an authenticated app update.
