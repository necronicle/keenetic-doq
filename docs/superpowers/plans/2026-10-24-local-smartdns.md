# Local SmartDNS implementation plan

Goal: Replace geo DNS lanes with exact local hosts overrides and ordinary DoQ upstreams; publish v0.6.0 and announce the release in the authorised Telegram topic.
Spec: ../specs/2026-10-24-local-smartdns-design.md
Architecture: smartdns resolver wrapper before the existing cached resolver. Embedded hosts snapshot by default, optional custom file and atomic SIGHUP reload. No geo runtime or classification remains.

Global constraints: exact case-insensitive names; 1025 approved names; no ws.chatgpt.com addition; local A/AAAA TTL 60; missing families and HTTPS/SVCB NODATA; no AD; no external query for those types of matched names. Other names/types use existing picker/cache. Geo DNS never a default. Explicit old geo config becomes an error with migration advice, never silently loses user config.

Shared interfaces: config.Config.Hosts string (default "builtin"); remove Geo, GeoDomains, FastLane; smartdns.Builtin() []byte; smartdns.Load(path string) (*Table,error); (*Table).Count() int. smartdns.New(path string,next Resolver) (*Resolver,error), Resolve and Reload methods. CLI hosts export prints builtin snapshot; hosts check validates selected source; hosts reload sends SIGHUP; hosts status describes current config source/count (not live success proof).

Task 1 core (internal/smartdns, internal/config, cmd/doqd, internal/geo removal):
- [ ] Write and observe failing parser, DNS family/SVCB, reload and config tests.
- [ ] Implement snapshot, atomic layer, config and daemon integration; remove geo package.
- [ ] Run targeted tests and commit owned files.
Task 2 CLI (internal/cli, internal/platform):
- [ ] Write failing removal/help/hosts validation tests.
- [ ] Remove geo UI/flags/editors, preserve ordinary upstream management, add hosts commands.
- [ ] Run CLI/platform tests and commit owned files.
Task 3 packaging (install scripts, uninstall scripts, scripts tests):
- [ ] Write failing fresh-install and legacy migration fixtures.
- [ ] Remove legacy geo migrations/defaults. Back up and migrate old geo rows to ordinary upstream; old geo-domain requires explicit actionable stop before install mutation. Default shipped geo trio is removed as a group; preserve custom upstream/fallback. Use hosts builtin default with optional custom file preserved.
- [ ] Run shell tests/shellcheck and commit owned files.
Task 4 integration/release (README, changelog, workflows, release):
- [ ] Update both READMEs, explain exact matching and dependency on third-party proxies, custom file replacement/reload and old config migration.
- [ ] Run full Go suite, race, vet, install tests, shellcheck, all cross-builds and UDP/TCP integration.
- [ ] Fresh whole-branch review; fix important findings and verify.
- [ ] Push feature branch, publish tag v0.6.0 with release notes/assets; verify remote CI/assets.
- [ ] Announce only verified release in Telegram topic 55256, chat -1003834368989.

Review focus: CNAME chains/ANY should not reveal direct IP for matched names; malformed input and atomic reload; CLI on stopped daemon; installer retains custom settings and fails safely before mutation; old config cannot silently use removed geo behaviour.
