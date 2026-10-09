# keenetic-doq

[Русский](README.md)

![CI](https://github.com/necronicle/keenetic-doq/actions/workflows/ci.yml/badge.svg)
![Release](https://img.shields.io/github/v/release/necronicle/keenetic-doq)

**Telegram: [DoQ topic in @zapret2keenetic](https://t.me/zapret2keenetic/55256)** — questions, setup help, discussion (in Russian)

DNS-over-QUIC (RFC 9250) for Keenetic routers — **instead of** the stock
DoT/DoH, without touching port 53.

KeeneticOS supports DoT and DoH but not DoQ, and has no plans to add it.
Existing solutions (AdGuard Home, dnsproxy) require `opkg dns-override`:
they capture port 53, displace the stock `ndnproxy` and break neighboring
Entware projects. `keenetic-doq` works differently:

```
LAN clients → ndnproxy :53 → <LAN-IP>:5354 (doqd) → [cache] ─┬ geo-blocked name → pinned unblocking server (quic://)
                                                              └ everything else → fastest of the servers (quic://)
```

The `doqd` daemon serves plain DNS on the router's LAN address (port 5354)
and registers itself with the stock `ip name-server <LAN-IP>:5354` command
as an upstream of the system DNS. Port 53 is never touched and
`opkg dns-override` is not needed.

doqd has two lanes. Names of geo-blocked services (ChatGPT, Claude,
Gemini...) are resolved by one pinned unblocking server (geohide or dns-ai
by default): it answers with its proxy addresses, so all domains of a
service get the proxies of one country. Every other name goes to the
fastest server.

> This project is intended for research into network protocols and for studying how DNS works. It is to be used for educational purposes only.

## When the router asks doqd

To KeeneticOS doqd is an ordinary, manually added DNS server. Which server
answers a client is decided by the router's stock logic:

- **DoT/DoH enabled — doqd is not used at all.** Per the
  [Keenetic manual](https://support.keenetic.ru/ultra/kn-1811/ru/31543.html)
  every query then goes to the DoT/DoH servers only, while the ISP's DNS
  and manually added servers, doqd included, are not used. There is no
  fallback to them either — users have confirmed this. So DoT/DoH must be
  turned off for doqd to work.
- **DoT/DoH disabled — a query goes to all plain servers at once**: doqd,
  the ISP's DNS and other manually added ones, and the client gets the
  fastest answer
  ([Keenetic manual](https://support.keenetic.ru/ultra/kn-1811/ru/22961.html)).
  The ISP's DNS may stay, but then part of the answers come from it, and
  which part cannot be predicted. To send everything through doqd, turn
  the ISP's DNS off in the internet connection settings ("ignore the
  ISP's DNS", in the CLI `interface <connection> ip no name-servers`),
  then `system configuration save`. The router's DNS then works only
  while doqd is alive.

Devices with an internet filter profile (AdGuard DNS, Yandex.DNS, SkyDNS)
use the filter's DNS, bypassing doqd.

## Features

- RFC 9250 DoQ client: long-lived QUIC connections (stream per query,
  connection reuse, keep-alive, TLS session resumption), message ID 0 on
  the wire.
- Servers with several addresses: the dial races them in a staggered
  fashion, a dead address doesn't hold up a live one; the winner is
  remembered.
- Geo lane for unblocking: the unblocking servers (`geo`, geohide and
  dns-ai by default) are evaluated once on liveness and TLS speed to their
  proxies; the best one is pinned and replaced only on failure.
  Geo-blocked names — from the built-in list (OpenAI, Anthropic,
  Gemini/AI Studio, NotebookLM, xAI), your own (`geo-domain`) and
  auto-detected ones — go only to it. The pinned server's proxy addresses
  are checked every 30 s; dead and slow ones are filtered out of answers.
- Fast lane: every other name goes to the fastest live server among
  `upstream` and `geo` (by default Quad9 and ControlD plus the unblocking
  servers); if it hasn't answered within ~3× its usual time, the same query
  goes to the next one in parallel. A dead server neither eats the query's
  time budget nor comes back to the front on its own — the background
  check brings it back. All servers are probed at startup. Fallbacks
  (`fallback`, none by default) are asked only when every other server has
  failed.
- A connection that stops answering (e.g. after a WAN reconnect) is checked
  and replaced; one slow answer doesn't tear it down.
- TTL-based response cache with LRU eviction; identical concurrent queries go
  upstream once. If the upstreams are unreachable or take longer than 1.8 s,
  a stale cached answer is served (RFC 8767, up to a day old, TTL 30 s).
- Management CLI in the same binary: `doqd add/remove/list/test/status/geo`,
  `add-domain`/`remove-domain` — your own DoQ servers and geo-blocked
  domains without editing files, live-probed before applying.
- Static binaries with no dependencies.

## Requirements

A Keenetic router with [Entware](https://help.keenetic.com/hc/en-us/articles/360021214160)
and the `curl` package (`opkg install curl`) — downloads need it. The
busybox `wget` on Keenetic is built without TLS and cannot open
`https://`, hence curl specifically. Everything else doqd and its scripts
use comes from busybox and is always present.

| Entware architecture | Binary |
|---|---|
| `aarch64-*` | `doqd-linux-arm64` |
| `mipsel-*` | `doqd-linux-mipsle` |
| `mips-*` (BE) | `doqd-linux-mips` |

## One-command install

On the router (over SSH):

```sh
curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh | sh
```

The installer detects the architecture, downloads the release binary and
verifies its SHA256, installs `/opt/sbin/doqd`, writes the config
`/opt/etc/doqd.conf` (with the router's LAN address) and the autostart
script `/opt/etc/init.d/S56doqd`, starts the daemon and registers the
name-server. An existing config is preserved on reinstall; configs from
older versions are moved to the `geo` key by the installer (re-running it
on a 0.4.0 config changes nothing).

Offline variant (binary already copied to the router):

```sh
sh install.sh --local ./doqd-linux-arm64
```

## Managing upstreams

All management is done with the same binary — no manual file editing:

```sh
~ # doqd list
GEO — geo-blocked names go only to the pinned server, marked * (/opt/etc/doqd.conf):
   1. quic://geohide.ru                          alive  rtt 212 ms  proxies 4/4 alive, tls 176 ms
 * 2. quic://dns.dns-ai.ru                       alive  rtt 317 ms  proxies 4/4 ok, tls 102 ms
FAST — every other name goes to the fastest of these and the servers above:
   3. quic://dns.quad9.net                       alive  rtt 209 ms
   4. quic://p0.freedns.controld.com             alive  rtt 186 ms

listen: 192.168.1.1:5354   daemon: running (pid 11236)
```

The GEO section lists the unblocking servers, the asterisk marks the pinned
one; FAST — every other name goes to the fastest of these servers and the
GEO ones. Fallback servers (if any) are marked `[fallback]`.

The pinned server, the ranking and the state of its proxies:

```sh
~ # doqd geo
pinned:     quic://dns.dns-ai.ru (since 2026-10-09 12:34)
evaluation: 2026-10-09 12:34

RANKING (last evaluation):
 1. quic://dns.dns-ai.ru               coverage 3/3  proxies 2/2 alive  tls 128 ms
 2. quic://geohide.ru                  coverage 3/3  proxies 4/4 alive  tls 176 ms

PINNED SERVER PROXIES (checked every 30 s):
  13.140.94.151                            healthy  tls 102 ms
  160.79.104.10                            healthy  tls 82 ms
  2607:6bc0::10                            healthy  tls 85 ms
  62.60.230.61                             healthy  tls 148 ms

learned geo-blocked names: 0
```

`doqd geo reselect` re-evaluates the unblocking servers right away instead
of waiting for a failure (it waits up to 45 s and shows the new choice).

Probe any server without changing anything:

```sh
~ # doqd test quic://dns.quad9.net
probing quic://dns.quad9.net
  bootstrap  2 address(es) from 1.1.1.1:53 over udp in 25 ms: 149.112.112.112, 9.9.9.9
  connect    149.112.112.112:853  OK in 145 ms
  query      keenetic.com A  answered in 43 ms
OK — answered in 215 ms
```

Add your own server — live-probed before it is written to the config
(a dead server won't slip in by accident; override with `--force`).
Without flags it goes to `upstream` (fast lane), with `--geo` to the
unblocking servers, with `--fallback` it becomes a fallback:

```sh
~ # doqd add --fallback quic://dns10.quad9.net
probing quic://dns10.quad9.net ... OK (198 ms)
added to /opt/etc/doqd.conf as fallback #6
restarting the daemon ... alive (pid 20702)
```

Remove — by number from `list` or by URL (the last `geo`/`upstream`
server is protected):

```sh
~ # doqd remove 6
removed fallback quic://dns10.quad9.net
restarting the daemon ... alive (pid 20702)
```

Your own geo-blocked domain (subdomains included) goes to the geo lane;
the built-in list stays:

```sh
~ # doqd add-domain example.com
~ # doqd remove-domain example.com
```

One-command diagnostics:

```sh
~ # doqd status
daemon:          running (pid 11236, uptime 3m30s)
listen:          192.168.1.1:5354 (udp+tcp)
geo:             pinned dns.dns-ai.ru since 2026-10-09 12:34, proxies 4/4 healthy
registration:    present in KeeneticOS name-servers
resolve via doqd: NOERROR, 0 ms
resolve via :53:  NOERROR, 0 ms
```

## Configuration — `/opt/etc/doqd.conf`

| Key | Default | Meaning |
|---|---|---|
| `listen` | `<LAN-IP>:5354` | listener address:port (UDP+TCP) |
| `geo` | `quic://geohide.ru`, `quic://dns.dns-ai.ru` | unblocking server, one line per server: geo-blocked names go only to the pinned one of them; they may also answer other names (fast lane). The first `geo`, `upstream` or `fallback` line overrides the defaults of all three keys |
| `upstream` | `quic://dns.quad9.net`, `quic://p0.freedns.controld.com` | plain DoQ server, one line per server: every other name goes to the fastest live one among `upstream` and `geo` |
| `fallback` | none | fallback DoQ server: asked only when every `geo` and `upstream` server has failed; its answers' TTL is capped at 60 s. A config needs at least one `geo` or `upstream` |
| `geo-domain` | none | your own geo-blocked domain (subdomains included), one line per domain, on top of the built-in list. `doqd add-domain` is easier |
| `bootstrap` | `77.88.8.8`, `77.88.8.8:1253`, `8.8.8.8`, `1.1.1.1` | plain DNS servers used to resolve the upstream names; IPs only (a port may be given). All are asked at once, a server silent over UDP is retried over TCP; the answer is cached for its TTL |
| `cache_size` | `4096` | max cache entries |
| `min_ttl` / `max_ttl` | `60` / `86400` | cache TTL bounds, seconds |
| `log` | `info` | debug / info / warn / error |

The pinned server is stored in `/opt/var/lib/doqd/geo.state` and survives
a restart.

`doqd add`/`doqd remove`/`doqd add-domain` restart the daemon automatically; after manual
edits run `/opt/etc/init.d/S56doqd restart`.

## Verifying

`doqd status` covers it all — it resolves both straight through doqd and
through the stock `:53`, shows the pinned unblocking server and checks the
registration (sample output above, in "Managing upstreams").

The registration in detail, and proof that the traffic really goes over
QUIC:

```sh
ndmc -c 'show ip name-server'        # stock utility, always present
tcpdump -ni any 'udp port 853'       # needs a package: opkg install tcpdump
```

Entware does not ship `dig` either: `opkg install bind-dig`, or run it
from a computer on the same network:

```sh
dig @192.168.1.1 -p 5354 example.com   # straight to doqd, repeat is cached
dig @192.168.1.1 example.com           # end-to-end via the stock DNS
```

## FAQ

**Why these defaults, not AdGuard?** geohide and dns-ai are
geo-unblocking resolvers: for ChatGPT, Gemini, Claude and other services
closed to Russia they answer with their proxy addresses, so geo-blocked
names go only to them. Quad9 and ControlD know no such addresses and return
the real ones; for every other name they are peers of the unblocking
servers: the query goes to the fastest. AdGuard DNS is blocked by DPI
(TSPU) in a number of Russian networks on both DoQ and DoT — `doqd test
quic://dns.adguard-dns.com` will show a handshake timeout, and shipping a
knowingly dead server as a default helps no one. Check yours: `doqd list`
live-probes every server. An unblocking server your ISP blocks does no
harm — queries route around it — but you can drop it: `doqd remove
<number>`.

**Where did comss go?** It left the defaults in 0.3.5. doqd then picked the
server with the fastest DNS answer, and that was usually comss, yet its
proxies for geo-blocked services turned out to be the slowest: a TLS
handshake with chatgpt.com through them took 0.2 to 4.7 s, through dns-ai
0.13–0.15 s. Sites opened noticeably slower even though DNS answered fast.
Since 0.4.0 proxy speed is taken into account when choosing the unblocking
server, and slow proxies of the pinned server are cut by the filter. To
bring comss back: `doqd add --geo quic://dns.comss.one`. On upgrade the
installer removes comss from the config if other unblocking servers remain.

**A geo-blocked service (ChatGPT, Gemini...) doesn't open.** First upgrade
to 0.4.0: before it, doqd took the fastest answer, so domains of one site
got proxies of different servers, and fast DNS did not mean fast proxies.
The installer moves the config to the `geo` key by itself. Then look at
`doqd geo`: the pinned server and the liveness and TLS of its proxies. If
the proxies are dead and the server was not replaced, `doqd geo reselect`
re-evaluates right away. If the domain is not in the built-in list and doqd
did not detect it on its own, add it: `doqd add-domain <domain>`. After the
change restart the browser on your devices: old connections and the
devices' cache hold the previous addresses. Also check `doqd status`: an
`other DNS` line means the router has other servers configured besides
doqd, and the real address may arrive past doqd. To send everything
through doqd, turn the ISP's DNS off — see
[When the router asks doqd](#when-the-router-asks-doqd).

**How does doqd tell geo-blocked from ordinary?** Three sources. The
built-in list — OpenAI, Anthropic, Gemini/AI Studio, NotebookLM, xAI. Your
own domains — `geo-domain` in the config or `doqd add-domain`. And
auto-detection: for an unknown name doqd looks at the pinned server's
answer, and if it holds addresses from that server's proxy pool, the name
is considered geo-blocked and from then on goes only to it. Only A queries
are classified directly; for AAAA, HTTPS and other types doqd first makes an
internal A query. A name once learned stays geo-blocked until restart: a
later "plain" result does not override it. The server's proxy pool grows
only from answers to the three probe domains (chatgpt.com, claude.ai,
gemini.google.com), not from every geo name: otherwise a listed name the
server does not substitute (e.g. x.ai with real Cloudflare addresses) would
drag every Cloudflare site into the geo lane. Ordinary sites thus get real
addresses from the fastest server, and the ping to them does not depend on
the unblocking servers.

**How does doqd pick the unblocking server, and why doesn't it jump between
them?** On first start (and on `doqd geo reselect`) doqd evaluates every
`geo` server: liveness and the median TLS handshake to its proxies for
chatgpt.com, claude.ai and gemini.google.com. The best by coverage and
speed is pinned; servers whose median TLS is within 20 % of the best count
as equal and keep the config order, so the choice does not flip on
measurement noise. The pinned server changes only on failure: three errors
in a row, or all its proxies dead for three checks in a row (checks run
every 30 s). The choice is stored in `/opt/var/lib/doqd/geo.state` and
survives a restart; an evaluation interrupted by shutdown is not saved, and
a corrupt `geo.state` triggers a fresh evaluation. When a proxy of the
pinned server turns dead or slow, cached answers containing it are dropped
at once.

**How do I add my own geo-blocked domain?** `doqd add-domain example.com`
— the domain and its subdomains go to the pinned unblocking server; the
daemon restarts by itself. To remove: `doqd remove-domain example.com`
(built-in domains stay). By hand — a `geo-domain example.com` line in
`/opt/etc/doqd.conf` and `/opt/etc/init.d/S56doqd restart`.

**The defaults filter something.** `dns.quad9.net` blocks malware
domains (ControlD `p0`
filters nothing). An unfiltered
option: `doqd add quic://dns10.quad9.net` (and `doqd remove` for
`dns.quad9.net`); `quic://unfiltered.adguard-dns.com` where AdGuard is
reachable.

**`ndmc: system failed [0xcffd0062]` / `Cli::Main: failed to initialize`,
and `doqd status` says `registration: NOT found` or `registration: unknown`.**
This is not about doqd — it is installed and running. This is what an
install from a shell opened with `exec sh` inside the stock router CLI
looks like (SSH to port 22 as admin, then `exec sh`): the CLI session is
already taken by the parent, `ndmc` cannot open a nested one, so the
installer could not register the name-server and `doqd status` from that
same shell cannot check it. You are already in the router CLI — you do
not need `ndmc`. Type `exit` to get back to the `(config)>` prompt and
run there:

```
ip name-server <LAN-IP>:5354
system configuration save
show ip name-server
```

The same commands work in the Web CLI (`http://<router-address>/a`), and
`ndmc` and `doqd status` work normally from the Entware session — SSH to
port 222 as root. Until the registration is there, queries bypass doqd
and go through the stock DNS.

**Internet is gone after a router reboot: raw IPs ping, names do not
resolve, and `doqd list` reports `lookup dns.comss.one on 127.0.0.1:53:
i/o timeout`.** A bug in releases before 0.3.0. doqd resolved its upstream
names through the system resolver, which on Keenetic is `127.0.0.1` — the
router's own DNS proxy, whose name-server list contains doqd itself. While
the WAN is up the ISP's servers answer first and the loop never closes; at
boot, before the connection is established, doqd is the only name-server,
so its own query for `dns.comss.one` comes right back to it and the
router's DNS wedges. Fix by upgrading: `curl -fsSL
https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh | sh`
— since 0.3.0 upstream names are resolved through separate `bootstrap`
servers, bypassing the router's DNS. No config change needed, but if your
ISP blocks outbound port 53, set your own with `bootstrap <IP>` in
`/opt/etc/doqd.conf`.

**doqd does not come up after a router reboot.** Since 0.2.5 the daemon waits
for its address: at boot `/opt` is mounted before the interface gets its
address, and a single bind attempt used to kill the daemon for good (the
Entware init script never retries). If it still does not come up on 0.2.5,
check `doqd status` — it says so plainly when the `listen` address is not
on any interface. That usually means the router's LAN address comes from an
upstream DHCP server (Relay mode) and changed across the reboot: give the
router a fixed LAN address, fix `listen` in `/opt/etc/doqd.conf` and
re-register it with `ip name-server <new-address>:5354`.

**Why port 5354 and not 5353?** 5353 on Keenetic is taken by avahi-daemon
(mDNS).

**Why the LAN address and not 127.0.0.1?** KeeneticOS rejects loopback in
`ip name-server` (`Dns::Manager: invalid IP address`).

**`doqd list` shows an upstream down with `bootstrap lookup ...: no
bootstrap server answered`.** The DoQ server itself may be perfectly fine:
doqd could not learn its address. None of the bootstrap servers answered a
plain DNS query for that upstream's name, over UDP or TCP. If other upstreams
are alive, your ISP is almost certainly dropping port-53 DNS queries for the
names of censorship-bypass services. A bootstrap server on another port
helps — Yandex DNS also listens on 1253. New installs already have it; in an
older config add `bootstrap 77.88.8.8:1253` to `/opt/etc/doqd.conf` and run
`/opt/etc/init.d/S56doqd restart`. To check by hand: `nslookup <name>
77.88.8.8` stays silent, while `doqd test quic://<name>` answers after the
change. Before 0.3.2 the error in this situation named the last server in the
list (`1.1.1.1:53: dial udp ... i/o timeout`), which had nothing to do with it.

**`doqd list` shows an upstream down with `dial ...: context deadline
exceeded` or `no address answered`.** The server's address was found, but
the QUIC connection (udp/853) didn't come up. `doqd test quic://<name>`
shows step by step which addresses were tried and how each attempt ended. If
the server works from other networks, your ISP is almost certainly blocking
it — this happens to censorship-bypass servers and to AdGuard. doqd can't
help there, but DNS keeps working: since 0.3.3 an unreachable upstream moves
to the back of the queue and doesn't delay queries (before 0.3.3 it came back
to the front every 30 seconds and a burst of queries got SERVFAIL). You can
drop it with `doqd remove <number>`.

**I added a server and it shows down.** `doqd list` shows liveness and RTT
for every upstream; `doqd remove <number>` drops the bad one. `doqd add`
probes servers itself and won't write a dead one without `--force`.

## Uninstall

```sh
curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/uninstall.sh | sh
```

Unregisters the name-server, stops and removes the daemon. The config
`/opt/etc/doqd.conf` is kept.

## Building from source

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o doqd ./cmd/doqd
# mipsel/mips: GOARCH=mipsle|mips GOMIPS=softfloat
```

## Limitations

- The encrypted leg is "router → upstream resolver" — the one your ISP
  sees. Inside the LAN devices talk to the router over plain DNS: doqd
  does not accept DoQ connections from LAN clients. The stock DoT/DoH in
  KeeneticOS work exactly the same way.
- No filtering or blocking of any kind: whatever the upstream answers is
  returned as is (except dead and slow proxies of the unblocking server,
  which the filter cuts out).
- doqd listens only on the router's LAN address — nothing is exposed to
  the WAN.

## License

[GPL-3.0](LICENSE)
