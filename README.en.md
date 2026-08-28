# keenetic-doq

[Русский](README.md)

![CI](https://github.com/necronicle/keenetic-doq/actions/workflows/ci.yml/badge.svg)
![Release](https://img.shields.io/github/v/release/necronicle/keenetic-doq)

DNS-over-QUIC (RFC 9250) for Keenetic routers — **alongside** the stock
DoT/DoH, not instead of them.

KeeneticOS supports DoT and DoH but not DoQ, and has no plans to add it.
Existing solutions (AdGuard Home, dnsproxy) require `opkg dns-override`:
they capture port 53, displace the stock `ndnproxy` and break neighboring
Entware projects. `keenetic-doq` works differently:

```
LAN clients → ndnproxy :53 → [stock DoT/DoH upstreams]
                           → <LAN-IP>:5354 (doqd) → [cache] → quic:// upstreams
```

The `doqd` daemon serves plain DNS on the router's LAN address (port 5354)
and registers itself with the stock `ip name-server <LAN-IP>:5354` command
as one more upstream of the system DNS. Port 53 is never touched and
`opkg dns-override` is not needed. `ndnproxy` naturally prefers the fastest
upstream — the local doqd with its cache wins on merit.

## Features

- RFC 9250 DoQ client: long-lived QUIC connections (stream per query,
  connection reuse, keep-alive), message ID 0 on the wire.
- Multiple upstreams: EWMA RTT stats, queries go to the fastest live
  server, instant failover, background health checks revive dead ones.
- TTL-based response cache with LRU eviction (router memory is precious).
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
 2. quic://dns.quad9.net                       alive  rtt 193 ms

listen: 192.168.1.1:5354   daemon: running (pid 9772)
```

Probe any server without changing anything:

```sh
~ # doqd test quic://dns.quad9.net
probing quic://dns.quad9.net ... OK — answered in 213 ms
```

Add your own upstream — live-probed before it is written to the config
(a dead server won't slip in by accident; override with `--force`):

```sh
~ # doqd add quic://dns.quad9.net
probing quic://dns.quad9.net ... OK (198 ms)
added to /opt/etc/doqd.conf (upstream #3)
restarting the daemon ... alive (pid 20702)
```

Remove — by number from `list` or by URL (the last upstream is
protected):

```sh
~ # doqd remove 3
removed quic://dns.quad9.net
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
| `upstream` | `quic://dns.comss.one`, `quic://dns.quad9.net` | DoQ upstream, one line per server; the first line overrides the built-in defaults |
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

**Why comss and Quad9 as defaults, not AdGuard?** In a number of Russian
networks AdGuard DNS is blocked by DPI (TSPU) on both DoQ and DoT —
`doqd test quic://dns.adguard-dns.com` will show a handshake timeout, and
shipping a knowingly dead server as a default helps no one. comss comes
first as the fastest of the tested ones (~130 ms from the router vs
~200 ms for Quad9), Quad9 is the failover. Check yours: `doqd list`
live-probes every server.

**Both defaults filter something.** comss blocks ads, trackers and
malicious domains; `dns.quad9.net` blocks malware domains. If you want
plain resolving with nobody's blocklists, add an unfiltered server:
`doqd add quic://dns10.quad9.net` (Quad9 without blocking) or
`doqd add quic://unfiltered.adguard-dns.com` where AdGuard is reachable.

**`ndmc: system failed [0xcffd0062]` / `Cli::Main: failed to initialize`,
and `doqd status` says `registration: NOT found`.** This is not about
doqd — it is installed and running; your SSH session simply cannot reach
the router CLI (typical when the session runs inside the OPKG environment
or the account has no command-line rights). Add the registration through
the web interface instead: open `http://<router-address>/a` (Web CLI) and
run two commands:

```
ip name-server <LAN-IP>:5354
system configuration save
```

`doqd status` will then report `registration: present`. Until it does,
queries bypass doqd and go through the stock DNS.

**doqd stops working after a router reboot.** Since 0.2.5 the daemon waits
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
