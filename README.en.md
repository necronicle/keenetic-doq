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
LAN clients → ndnproxy :53 → <LAN-IP>:5354 (doqd) → [cache] → quic:// upstreams
```

The `doqd` daemon serves plain DNS on the router's LAN address (port 5354)
and registers itself with the stock `ip name-server <LAN-IP>:5354` command
as an upstream of the system DNS. Port 53 is never touched and
`opkg dns-override` is not needed.

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
- Geo-unblocking by default: the main upstreams are comss, geohide and
  dns-ai, which answer with their proxy addresses for services blocked by
  geolocation (ChatGPT, Gemini, Claude...). Quad9 and ControlD are
  `fallback`s: they are asked only when every main upstream has failed, so the real address of a
  blocked service never overtakes the proxy one. Fallback answers stay in
  the cache for at most a minute.
- Multiple upstreams: queries go to the fastest live server; if it hasn't
  answered within ~3× its usual time, the same query goes to the next one in
  parallel. A dead upstream neither eats the query's time budget nor comes
  back to the front on its own — the background check brings it back. All
  upstreams are probed at startup.
- A connection that stops answering (e.g. after a WAN reconnect) is checked
  and replaced; one slow answer doesn't tear it down.
- TTL-based response cache with LRU eviction; identical concurrent queries go
  upstream once. If the upstreams are unreachable or take longer than 1.8 s,
  a stale cached answer is served (RFC 8767, up to a day old, TTL 30 s).
- Management CLI in the same binary: `doqd add/remove/list/test/status` —
  your own DoQ servers without editing files, live-probed before applying.
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
name-server. An existing config is preserved on reinstall.

Offline variant (binary already copied to the router):

```sh
sh install.sh --local ./doqd-linux-arm64
```

## Managing upstreams

All management is done with the same binary — no manual file editing:

```sh
~ # doqd list
UPSTREAMS (/opt/etc/doqd.conf):
 1. quic://dns.comss.one                       alive  rtt 34 ms
 2. quic://geohide.ru                          alive  rtt 41 ms
 3. quic://dns.dns-ai.ru                       alive  rtt 27 ms
 4. quic://dns.quad9.net                       alive  rtt 193 ms  [fallback]
 5. quic://p0.freedns.controld.com             alive  rtt 181 ms  [fallback]

[fallback] is asked only when every other upstream has failed.

listen: 192.168.1.1:5354   daemon: running (pid 9772)
```

Probe any server without changing anything:

```sh
~ # doqd test quic://dns.quad9.net
probing quic://dns.quad9.net
  bootstrap  2 address(es) from 1.1.1.1:53 over udp in 25 ms: 149.112.112.112, 9.9.9.9
  connect    149.112.112.112:853  OK in 145 ms
  query      keenetic.com A  answered in 43 ms
OK — answered in 215 ms
```

Add your own upstream — live-probed before it is written to the config
(a dead server won't slip in by accident; override with `--force`). With
`--fallback` it becomes a fallback:

```sh
~ # doqd add --fallback quic://dns10.quad9.net
probing quic://dns10.quad9.net ... OK (198 ms)
added to /opt/etc/doqd.conf as fallback #6
restarting the daemon ... alive (pid 20702)
```

Remove — by number from `list` or by URL (the last main upstream is
protected):

```sh
~ # doqd remove 6
removed fallback quic://dns10.quad9.net
restarting the daemon ... alive (pid 20702)
```

One-command diagnostics:

```sh
~ # doqd status
daemon:          running (pid 9772, uptime 72h3m10s)
listen:          192.168.1.1:5354 (udp+tcp)
registration:    present in KeeneticOS name-servers
resolve via doqd: NOERROR, 148 ms
resolve via :53:  NOERROR, 40 ms
```

## Configuration — `/opt/etc/doqd.conf`

| Key | Default | Meaning |
|---|---|---|
| `listen` | `<LAN-IP>:5354` | listener address:port (UDP+TCP) |
| `upstream` | `quic://dns.comss.one`, `quic://geohide.ru`, `quic://dns.dns-ai.ru` | main DoQ upstream, one line per server; queries go to the fastest live one. The first `upstream` or `fallback` line overrides the defaults of both keys |
| `fallback` | `quic://dns.quad9.net`, `quic://p0.freedns.controld.com` | fallback DoQ upstream: asked only when every main upstream has failed; its answers' TTL is capped at 60 s. A config needs at least one `upstream` |
| `bootstrap` | `77.88.8.8`, `77.88.8.8:1253`, `8.8.8.8`, `1.1.1.1` | plain DNS servers used to resolve the upstream names; IPs only (a port may be given). All are asked at once, a server silent over UDP is retried over TCP; the answer is cached for its TTL |
| `cache_size` | `4096` | max cache entries |
| `min_ttl` / `max_ttl` | `60` / `86400` | cache TTL bounds, seconds |
| `log` | `info` | debug / info / warn / error |

`doqd add`/`doqd remove` restart the daemon automatically; after manual
edits run `/opt/etc/init.d/S56doqd restart`.

## Verifying

`doqd status` covers it all — it resolves both straight through doqd and
through the stock `:53`, and checks the registration:

```sh
~ # doqd status
daemon:          running (pid 15644, uptime 3d1h)
listen:          192.168.1.1:5354 (udp+tcp)
registration:    present in KeeneticOS name-servers
resolve via doqd: NOERROR, 148 ms
resolve via :53:  NOERROR, 40 ms
```

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

**Why these defaults, not AdGuard?** comss, geohide and dns-ai are
geo-unblocking resolvers: for ChatGPT, Gemini, Claude and other services
closed to Russia they answer with their proxy addresses. Quad9 and ControlD
know no such addresses and return the real ones, so they are fallbacks, not
peers.
AdGuard DNS is blocked by DPI (TSPU) in a number of Russian networks on
both DoQ and DoT — `doqd test quic://dns.adguard-dns.com` will show a
handshake timeout, and shipping a knowingly dead server as a default helps
no one. Check yours: `doqd list` live-probes every server. An unblocking
server your ISP blocks does no harm — queries route around it — but you
can drop it: `doqd remove <number>`.

**A geo-blocked service (ChatGPT, Gemini...) still doesn't open.** First
upgrade to 0.3.4: before it, doqd returned whichever upstream answered
first, and Quad9 regularly overtook comss with the real address. The
installer turns Quad9 into a fallback by itself, adds ControlD next to it
and, if the config still
holds the old defaults, adds geohide and dns-ai. If that didn't help, look
at `doqd status`: an `other DNS` line means the router has other servers
configured besides doqd, and the real address may arrive past doqd.
To send everything through doqd, turn the ISP's DNS off — see
[When the router asks doqd](#when-the-router-asks-doqd). Devices have
cached the stale answer
too — restart the browser on them or wait a few minutes.

**Why not send only geo-blocked names through the unblocking servers and
the rest through plain DNS, for lower ping?** There is nothing to gain.
For ordinary domains the unblocking servers return the same real
addresses; they substitute only geo-blocked services, so the ping to a site
doesn't change. Measured on the router, 30 popular domains past the cache:
DNS answers via the unblocking servers took 25–27 ms, via
Quad9/ControlD/AliDNS 28–90 ms; the ping to the returned address was the
same or better for the unblocking servers. Repeat queries come from doqd's
cache. A split would need a list of geo-blocked domains, which always lags
behind: a site missing from it would get its real address and not open.

**The defaults filter something.** comss blocks ads, trackers and
malicious domains; `dns.quad9.net` blocks malware domains (ControlD `p0`
filters nothing). An unfiltered
fallback: `doqd add --fallback quic://dns10.quad9.net` (and `doqd remove`
for `dns.quad9.net`); `quic://unfiltered.adguard-dns.com` where AdGuard is
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
  returned as is.
- doqd listens only on the router's LAN address — nothing is exposed to
  the WAN.

## License

[GPL-3.0](LICENSE)
