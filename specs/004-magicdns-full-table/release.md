# DOQ v0.6.8 — full built-in hosts review

## Requirements and success criteria

Check every exact name and suffix rule in v0.6.7 against MagicDNS, including both Twitch endpoints. Change only rules with a verified MagicDNS proxy. Preserve the addresses and order of every unconfirmed rule. Publish the resulting binaries and announce this version in Telegram topic 55256.

## Plan and tasks

1. Query MagicDNS for the exact name or a concrete subdomain, and a fresh child for every suffix rule.
2. Check HTTPS with the target hostname and certificate validation through the candidate proxy addresses. Confirm a useful response through a fixed MagicDNS address. Keep ordinary CDN answers dynamic and keep unconfirmed rules intact.
3. Compare the resulting table to the input, run resolver/export tests and release checks, publish the tag and report the verified outcome to the chat.

## Evidence

The [rule-by-rule CSV](../../docs/verification/2026-11-24-magicdns-hosts.csv) contains all 641 input rules. Compared with v0.6.7, 80 mappings changed, 5 already had the verified single address and 556 retained their original ordered addresses. The built-in name and mask sets match v0.6.7 exactly.

The four IP columns record individual HTTPS probes from the test network with certificate verification. ERR28 means timeout, ERR35 TLS failure and ERR60 certificate failure. The subsequent checks use the path shown and require two useful responses (including a recognizable API authentication response). Redirect checks record the final destination and HTTP status. A wildcard additionally requires that MagicDNS route both the tested host and a fresh child under that suffix to the selected IP. A representative HTTPS response does not establish that all possible children or authenticated actions work.

For Twitch, the MagicDNS resolver supplied ordinary CDN answers. Using those answers, the public Twitch player API issued an anonymous playback token, and the ESLCS HLS master playlist returned 200 and listed stream variants. Forcing either Twitch hostname to a tested MagicDNS IP failed. Playing video and the pre-existing Twitch proxy routes are outside that successful playlist check.
