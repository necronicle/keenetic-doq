# Network verification — 2026-11-11

[All 27 endpoints, policies, source URLs and measurements](../../docs/verification/2026-11-11-doq-apple.csv).

Per reachable endpoint: one initial control query, then 11 Apple domains × A/AAAA/HTTPS × three rounds (99 Apple queries), with EDNS1232, a three-second query limit, a 120ms pause, and at most four resolvers in parallel. The initial connection probe allows 12 seconds. Testing stops after two DNS errors; incomplete runs are marked. Each unique returned hostname/IP pair is checked twice with TCP443, correct TLS SNI and certificate validation. Root HTTP403/404 is not a successful IPA download. There were 951 actual DNS attempts (924 Apple + 27 initial controls) and 960 actual TLS attempts. A hostname/IP pair shared by several resolvers was probed once per round and attributed to each relevant resolver in the CSV; therefore CSV TLS columns sum to 1,226 resolver-attributed observations, not 1,226 separate connections.

- Hostux Standard: documented no blocking; 99/99 Apple queries, median101ms/P95176ms; 110/110 TLS checks passed. Earlier independent run: 135 queries and 122 TLS checks passed. An App Store Pages lookup returned HTTP200 and artwork payloads returned HTTP206.
- Quad9 dns12: no threat blocking + ECS, median63ms; DNS99/99 passed, TLS132/134. Of four amp-api.apps.apple.com IPv4s, 151.101.195.6 repeatedly failed TLS while the other three worked. This is partial network reachability, not proof of forged DNS answers.
- FFMUC and Digitale Gesellschaft: DNS passed, but returned IPv4s for iosapps.itunes.apple.com/pancake.apple.com failed TLS. Excluded.
- Control D and NextDNS: current DoQ handshakes failed. Earlier successful samples do not establish current health. Removed.
- AdGuard Default: DNS/TLS passed, but blocks advertising/tracking. Excluded per the no-filter requirement.
- DNS4all: Apple TLS passed, but one DNS timeout and documented sanctioned-domain blocking/beta status. Excluded.
- Other candidates: transport errors, expired certificate, incomplete tests or unverified filtering/operator policy; not added.

Google AI: MagicDNS 45.88.163.92 did not accept TCP443. Both replacement GeoHide addresses served full Gemini application HTML with HTTP200 and valid TLS. API roots responded with valid TLS. No broad google.com/googleapis.com wildcard was added.

Limitations: results are specific to this source network and time. No authenticated Gemini conversation or actual signed app update on a phone was performed. The release fixes verified configuration faults and does not promise that all Apple updates work for every ISP.
