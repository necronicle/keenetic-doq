# keenetic-doq

[Русский](README.md)

![CI](https://github.com/necronicle/keenetic-doq/actions/workflows/ci.yml/badge.svg)
![Release](https://img.shields.io/github/v/release/necronicle/keenetic-doq)

**Telegram: [DoQ topic in @zapret2keenetic](https://t.me/zapret2keenetic/55256)** — questions, setup help, discussion (in Russian)

DNS-over-QUIC (RFC 9250) for **Keenetic** and **OpenWRT** routers.

`doqd` is a small daemon on the router. It receives plain DNS queries and
sends them to DoQ servers through an encrypted channel. doqd does not use
port 53: the stock DNS server of the router stays in place and sends the
queries to doqd.

```
Keenetic:  clients → ndnproxy :53 → <LAN-IP>:5354 (doqd) ─┐
OpenWRT:   clients → dnsmasq  :53 → 127.0.0.1:5354 (doqd) ─┤
                                                          ├ geo-blocked name → pinned unblocking server (quic://)
                                                          └ every other name → fastest plain server (quic://)
```

doqd has two lanes:

- **Geo lane.** Names of geo-blocked services (ChatGPT, Claude, Gemini...) go
  only to one pinned unblocking server. The unblocking server answers with
  the addresses of its proxies. All domains of a service get proxies in one
  country.
- **Fast lane.** Every other name goes to the fastest live plain server. The
  unblocking servers do not get it.

> This project is for research of network protocols and for study of DNS. Use it for education only.

## Contents

- [Features](#features)
- [Requirements](#requirements)
- [Install on Keenetic](#install-on-keenetic)
- [Install on OpenWRT](#install-on-openwrt)
- [How doqd connects to the router DNS](#how-doqd-connects-to-the-router-dns)
- [Management](#management)
- [Configuration](#configuration)
- [Verification](#verification)
- [FAQ](#faq)
- [Uninstall](#uninstall)
- [Building from source](#building-from-source)
- [Limitations](#limitations)

## Features

- DoQ client per RFC 9250: long-lived QUIC connections, one stream per
  query, keep-alive and TLS resumption. The query ID on the wire is 0.
- A server can have many addresses. doqd tries them as a ladder, so a dead
  address does not delay a live one. doqd remembers the address that works.
- Geo lane. doqd evaluates the unblocking servers (`geo`) once: liveness and
  TLS speed to their proxies. doqd pins the best server. The pinned server
  changes only when it fails. Every 30 s doqd checks its proxies and removes
  dead and slow proxies from the answers.
- Fast lane. A query goes to the fastest live server of `upstream`. The `geo`
  servers are not in this lane. If the config has no `upstream`, plain names
  go to the `geo` servers. An answer with SERVFAIL, REFUSED or a broken CNAME
  chain (a CNAME with no address at the end) does not win over a full answer
  of another server. If the server is silent for longer than ~3× its usual
  time, doqd also asks the next one. A dead server goes to the end of the
  queue. doqd asks the fallback servers (`fallback`) only when all other
  servers failed.
- If a connection stops answering (for example, after a WAN reconnect), doqd
  replaces it. A slow answer to one query does not break the connection.
- Answer cache by TTL with LRU eviction. Identical concurrent queries go out
  once. If the servers are down or need more than 1.8 s, doqd serves a stale
  answer from the cache (RFC 8767, up to one day, with TTL 30 s).
- Management in the same binary: `doqd add/remove/list/test/status/geo`,
  `add-domain`/`remove-domain`. You do not edit files. doqd tests a server
  with a live query before it writes the server to the config.
- One static binary without dependencies. It contains root certificates for
  firmware that has none.

## Requirements

| | Keenetic | OpenWRT |
|---|---|---|
| Firmware | KeeneticOS with [Entware](https://help.keenetic.com/hc/en-us/articles/360000925139) | OpenWRT 24.10 or 25.12 with dnsmasq (standard build) |
| Download | `curl` (`opkg install curl`) | `wget` of the firmware (uclient-fetch) or `curl` |
| Flash | space in Entware (USB or internal storage) | 4–5 MB free on JFFS2/UBIFS, 9 MB on ext4/f2fs; the installer checks it |
| Memory | about 13–15 MB RAM for the daemon | about 13–15 MB RAM for the daemon |

On Keenetic, the busybox `wget` has no TLS and cannot open `https://`. Thus
you need `curl`. All other tools for doqd and its scripts are in busybox.

The binary is 7–8.5 MB. JFFS2 and UBIFS on router flash compress files, so
there the binary uses less flash than its size. The OpenWRT installer
estimates the necessary space before the copy. If the flash still becomes
full, the installer removes doqd and restores dnsmasq.

OpenWRT 24.10.8 and 25.12.5 were tested in the QEMU emulator on aarch64,
mipsel, mips64, mips64el and armv7. The amd64, 386, riscv64 and armv5 builds
were tested with a run and a DoQ query. The loong64 build was compiled but
not run.

### Architectures

| Binary | Keenetic (Entware) | OpenWRT (`DISTRIB_ARCH`) |
|---|---|---|
| `doqd-linux-arm64` | `aarch64-*` | `aarch64_*` |
| `doqd-linux-mipsle` | `mipsel-*` | `mipsel_*` |
| `doqd-linux-mips` | `mips-*` | `mips_*` |
| `doqd-linux-armv7` | — | `arm_cortex-a*_vfp*`, `arm_cortex-a*_neon*` |
| `doqd-linux-armv5` | — | ARM without FPU: `arm_cortex-a7`, `arm_cortex-a9`, `arm_arm926ej-s`, `arm_arm1176jzf-s_vfp`, `arm_xscale` |
| `doqd-linux-amd64` | — | `x86_64` |
| `doqd-linux-386` | — | `i386_pentium4`, `i386_pentium-mmx` |
| `doqd-linux-mips64` | — | `mips64_*` |
| `doqd-linux-mips64le` | — | `mips64el_*` |
| `doqd-linux-riscv64` | — | `riscv64_*` |
| `doqd-linux-loong64` | — | `loongarch64_*` |

The installer selects the binary. If the `armv7` build does not run (the
CPU has no FPU), the installer uses `armv5`.

Go cannot build programs for ARMv4 (`arm_fa526`), big-endian ARM
(`armeb_xscale`), PowerPC (`powerpc_*`) and ARC. doqd does not run on these
platforms.

## Install on Keenetic

Connect to the router with SSH (Entware, port 222) and run:

```sh
curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh | sh
```

The installer does these steps:

1. It finds the Entware architecture.
2. It downloads the binary from the release and checks the SHA256.
3. It installs `/opt/sbin/doqd`.
4. It writes the config `/opt/etc/doqd.conf` with the LAN address of the
   router.
5. It writes the init script `/opt/etc/init.d/S56doqd` and starts the daemon.
6. It registers doqd in KeeneticOS: `ip name-server <LAN-IP>:5354`.

The installer does not overwrite an existing config. It converts configs of
old versions to the current format. A second run breaks nothing.

Without access to GitHub, copy the binary to the router and run:

```sh
sh install.sh --local ./doqd-linux-arm64
```

After the installation, turn off DoT and DoH in KeeneticOS. If they are on,
the router does not ask doqd (see [below](#keenetic)).

## Install on OpenWRT

Connect to the router with SSH and run:

```sh
wget -qO- https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install-openwrt.sh | sh
```

The installer does these steps:

1. It finds the architecture from `DISTRIB_ARCH` in `/etc/openwrt_release`.
2. It downloads the binary from the release and checks the SHA256.
3. It checks the free space on the flash.
4. It installs `/usr/sbin/doqd`.
5. It writes the config `/etc/doqd.conf` (`listen 127.0.0.1:5354`).
6. It writes the procd init script `/etc/init.d/doqd`. procd restarts the
   daemon after each failure.
7. It adds the doqd files to the sysupgrade list. Thus doqd stays after a
   firmware upgrade that keeps the settings.
8. It starts the daemon and waits for an answer from doqd. **If doqd does
   not answer, the installer stops, and dnsmasq works with its original
   servers.** The router DNS continues to work.
9. It makes doqd the only server of dnsmasq (details in the section
   [OpenWRT](#openwrt)).
10. It sends a query through dnsmasq on port 53. If there is no answer, the
    installer restores dnsmasq.

Without access to GitHub, copy the binary to the router and run:

```sh
sh install-openwrt.sh --local ./doqd-linux-mipsle
```

A second run updates doqd. The config and the original dnsmasq settings
stay.

## How doqd connects to the router DNS

### Keenetic

For KeeneticOS, doqd is a usual DNS server that you added manually. The
stock logic of the router selects the server that answers the client:

- **DoT or DoH is on — the router does not ask doqd.** Per the
  [Keenetic help](https://support.keenetic.ru/ultra/kn-1811/ru/31543.html),
  all queries then go only to the DoT/DoH servers. The ISP DNS and the
  manual servers (doqd too) are not used. There is no fallback to them.
  Turn off DoT and DoH.
- **DoT and DoH are off — a query goes to all plain servers at the same
  time**: to doqd, to the ISP DNS and to the other manual servers. The
  client gets the fastest answer
  ([Keenetic help](https://support.keenetic.ru/ultra/kn-1811/ru/22961.html)).

You can keep the ISP DNS. Then some answers come from it, and you cannot
know which ones. To send all queries through doqd, turn off the ISP DNS:

1. In the settings of the internet connection, turn on "ignore DNS from the
   ISP". In the CLI, this is `interface <connection> ip no name-servers`.
2. Run `system configuration save`.

After this, the router DNS works only while doqd works.

Devices with an internet filter profile (AdGuard DNS, Yandex.DNS, SkyDNS)
ask the DNS of the filter, not doqd.

### OpenWRT

On OpenWRT, doqd works behind dnsmasq. The installer changes three items
in `/etc/config/dhcp` (section `dhcp.@dnsmasq[0]`):

- `server 127.0.0.1#5354` — dnsmasq sends the queries to doqd.
- `noresolv 1` — dnsmasq does not use the DNS servers of the ISP (WAN). If
  it used them, dnsmasq would send each query to one of its servers, not
  always to doqd
  ([man dnsmasq](https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html),
  `--strict-order`).
- `server /openwrt.pool.ntp.org/<bootstrap>` — the names of the NTP servers
  go to the bootstrap servers as plain DNS. The reason is below.

**Why NTP goes around doqd.** After a reboot, the router clock can be late.
OpenWRT sets the clock from the newest file in `/etc`
([sysfixtime](https://github.com/openwrt/openwrt/blob/main/package/base-files/files/etc/init.d/sysfixtime)).
If the clock is late, the certificates of the DoQ servers seem not valid
yet. Then doqd cannot make a TLS connection. Without the bypass, NTP would
also ask doqd and get no answer: the clock would never become correct. With
the bypass, NTP sets the clock, and doqd starts to work without help. The
[OpenWRT guide](https://openwrt.org/docs/guide-user/base-system/dhcp_configuration)
does the same (section "Race conditions with sysntpd"). The installer takes
the names from `system.ntp.server` and the addresses from the `bootstrap`
lines in `/etc/doqd.conf`.

The installer keeps the original value of `noresolv` and the entries that
it added in `/etc/doqd/dnsmasq.saved`. The uninstaller restores `noresolv`
and removes only these entries. An entry that dnsmasq had before the
installation stays.

**Other dnsmasq servers.** If dnsmasq already has servers for all names
(https-dns-proxy, stubby, podkop and others add them), the installer does
not remove them. It shows a warning. `doqd status` shows these servers in
the line `other DNS`. dnsmasq sends a query to one of its servers, so some
queries go around doqd. To send all queries through doqd, remove these
servers or the proxy that added them.

**AdGuard Home and other DNS servers on port 53.** If dnsmasq does not
listen on port 53 (`dhcp.@dnsmasq[0].port` is not 53), the installer shows a
warning. Then set `127.0.0.1:5354` as the upstream of that DNS server
manually.

**Firmware upgrade.** A sysupgrade that keeps the settings keeps
`/etc/config/dhcp`, where dnsmasq points to doqd. Thus the installer adds
these items to `/lib/upgrade/keep.d/doqd`: the binary, the init script, the
autostart links, the config and `/etc/doqd/`. After the upgrade, doqd
starts automatically. An upgrade without the settings removes doqd and
gives back the standard dnsmasq.

## Management

All commands work the same on Keenetic and on OpenWRT. doqd finds its file
paths automatically (see [Configuration](#configuration)).

The list of servers with live probes:

```sh
~ # doqd list
GEO — geo-blocked names go only to the pinned server, marked * (/etc/doqd.conf):
 * 1. quic://dns.bezmezhau.com                   alive  rtt 81 ms   proxies 2/2 ok, tls 220 ms
FAST — every other name goes to the fastest of these:
   2. quic://dns.quad9.net                       alive  rtt 249 ms
   3. quic://p0.freedns.controld.com             alive  rtt 122 ms
   4. quic://dns.nextdns.io                      alive  rtt 151 ms

listen: 127.0.0.1:5354   daemon: running (pid 3560)
```

The GEO section shows the unblocking servers. The star marks the pinned
server. The FAST section shows the plain servers: every other name goes to
the fastest of them. doqd marks fallback servers with
`[fallback]`.

The pinned server, the ranking and the proxies:

```sh
~ # doqd geo
pinned:     quic://dns.bezmezhau.com (since 2026-10-17 01:00)
evaluation: 2026-10-17 01:00

RANKING (last evaluation):
 1. quic://dns.bezmezhau.com            coverage 7/7  proxies 2/2 alive  tls 220 ms

PINNED SERVER PROXIES (checked every 30 s):
  31.58.216.201                             healthy  tls 440 ms
  143.20.64.55                              healthy  tls 220 ms

learned geo-blocked names: 0
```

doqd does not give a `slow` proxy to clients while healthy proxies are
available.

`doqd geo reselect` evaluates the unblocking servers again immediately (it
waits up to 45 s and shows the new selection). `doqd add --geo` does not
change the selection: the pinned server stays after a restart. To evaluate
the new server, run `doqd geo reselect`.

Test any server and keep the config as it is:

```sh
~ # doqd test quic://dns.quad9.net
probing quic://dns.quad9.net
  bootstrap  2 address(es) from 1.1.1.1:53 over udp in 27 ms: 149.112.112.112, 9.9.9.9
  connect    149.112.112.112:853  OK in 113 ms
  query      keenetic.com A  answered in 47 ms
OK — answered in 188 ms
```

Add a server. doqd tests the server first. doqd does not add a dead server
(the flag `--force` skips the test). Without flags, the server goes to
`upstream`. With `--geo`, it goes to the unblocking servers. With
`--fallback`, it goes to the fallback servers. After the change, doqd
restarts the daemon:

```sh
~ # doqd add --fallback quic://dns10.quad9.net
probing quic://dns10.quad9.net ... OK (273 ms)
added to /etc/doqd.conf as fallback #5
restarting the daemon ... alive (pid 3948)
```

Remove a server by its number in `doqd list` or by its URL. You cannot
remove the last `geo` or `upstream` server:

```sh
~ # doqd remove 5
removed fallback quic://dns10.quad9.net
restarting the daemon ... alive (pid 4016)
```

Your own geo-blocked domain (with subdomains). The built-in list stays:

```sh
~ # doqd add-domain example.com
~ # doqd remove-domain example.com
```

## Configuration

| | Keenetic | OpenWRT |
|---|---|---|
| Config | `/opt/etc/doqd.conf` | `/etc/doqd.conf` |
| Binary | `/opt/sbin/doqd` | `/usr/sbin/doqd` |
| Init script | `/opt/etc/init.d/S56doqd` | `/etc/init.d/doqd` |
| Pinned server | `/opt/var/lib/doqd/geo.state` | `/etc/doqd/geo.state` |
| Log | not written | `logread -e doqd` |
| `listen` | `<LAN-IP>:5354` | `127.0.0.1:5354` |

Config keys:

| Key | Default | What it does |
|---|---|---|
| `listen` | see the table above | address and port of doqd (UDP and TCP) |
| `geo` | `quic://dns.bezmezhau.com` | unblocking server, one line per server. Geo-blocked names go only to the pinned one. These servers do not get the other names. The first `geo`, `upstream` or `fallback` line cancels the defaults of all three keys |
| `upstream` | `quic://dns.quad9.net`, `quic://p0.freedns.controld.com`, `quic://dns.nextdns.io` | plain DoQ server, one line per server. Other names go to the fastest live server of `upstream`. Without `upstream`, they go to the `geo` servers |
| `fallback` | none | fallback DoQ server. doqd asks it only when all `geo` and `upstream` servers failed. The TTL of its answers is 60 s or less. You need at least one `geo` or `upstream` |
| `geo-domain` | none | your own geo-blocked domain (with subdomains), one line per domain. It adds to the built-in list. `doqd add-domain` is easier |
| `bootstrap` | `77.88.8.8`, `77.88.8.8:1253`, `8.8.8.8`, `1.1.1.1` | plain DNS servers for the addresses of the DoQ servers (on OpenWRT, also for the NTP names). IP only, a port is optional. doqd asks all of them at the same time. If a server is silent over UDP, doqd asks it again over TCP. doqd caches an answer for its TTL |
| `cache_size` | `4096` | maximum number of cache entries |
| `min_ttl` / `max_ttl` | `60` / `86400` | TTL limits in the cache, in seconds |
| `log` | `info` | `debug`, `info`, `warn` or `error` |

The commands `doqd add`, `remove`, `add-domain` and `remove-domain` restart
the daemon. After a manual change of the config, restart the daemon:

```sh
/opt/etc/init.d/S56doqd restart    # Keenetic
/etc/init.d/doqd restart           # OpenWRT
```

## Verification

`doqd status` checks everything at once. It sends a query directly to doqd
and through the stock DNS of the router on port 53. It also shows the pinned
unblocking server and checks that the router DNS sends queries to doqd.

Keenetic:

```sh
~ # doqd status
daemon:          running (pid 4572, uptime 4s)
listen:          192.168.1.1:5354 (udp+tcp)
geo:             pinned dns.bezmezhau.com since 2026-10-17 01:00, proxies 2/2 healthy
registration:    present in KeeneticOS name-servers
resolve via doqd: NOERROR, 42 ms
resolve via :53:  NOERROR, 1 ms
```

OpenWRT:

```sh
~ # doqd status
daemon:          running (pid 3560, uptime 10s)
listen:          127.0.0.1:5354 (udp+tcp)
geo:             pinned dns.bezmezhau.com since 2026-10-17 01:00, proxies 2/2 healthy
dnsmasq:         forwards to doqd (127.0.0.1#5354)
resolve via doqd: NOERROR, 0 ms
resolve via :53:  NOERROR, 0 ms
```

On OpenWRT, the line `dnsmasq` compares two places: `/etc/config/dhcp` and
the config that dnsmasq uses now (`/var/etc/dnsmasq.conf.*`).

More checks on Keenetic:

```sh
ndmc -c 'show ip name-server'        # registration, the tool is always there
tcpdump -ni any 'udp port 853'       # DoQ traffic; needs: opkg install tcpdump
```

More checks on OpenWRT:

```sh
uci show dhcp.@dnsmasq[0]            # server and noresolv
nslookup chatgpt.com 127.0.0.1       # answer through dnsmasq and doqd
logread -e doqd                      # doqd log
```

From a computer in the same network (you need `dig`):

```sh
dig @192.168.1.1 example.com           # through the router DNS
dig @192.168.1.1 -p 5354 example.com   # directly to doqd; Keenetic only, on OpenWRT doqd listens on 127.0.0.1
```

## FAQ

### General

**Why these default servers, and not AdGuard?** Bezmezhau DNS
(`quic://dns.bezmezhau.com`) is an unblocking server. For ChatGPT, Gemini, Claude
and other services closed in the region, it answers with the addresses of its
proxies. Thus geo-blocked names go only to it. Quad9, ControlD and NextDNS do
not know these addresses and give the real ones. All other names go only to
these three, to the fastest one: the unblocking servers have no need to see
everything you open. Cloudflare and Google have no DoQ. In a number of Russian
networks, DPI (TSPU) blocks AdGuard DNS on DoQ and on DoT. Test your servers:
`doqd list` tests each one with a live query. An unavailable unblocking server
does not cause problems: queries go around it. To remove it, run
`doqd remove <number>`.

**A geo-blocked service (ChatGPT, Gemini...) does not open.** Do these steps
in sequence:

1. Run `doqd geo`. Look at the pinned server, the liveness and the TLS time
   of its proxies.
2. If the proxies are dead and no change occurred, run `doqd geo reselect`.
3. If the domain is not in the built-in list, add it:
   `doqd add-domain <domain>`.
4. Restart the browser on the device. Old connections and the device cache
   keep the old addresses.
5. Run `doqd status`. The line `other DNS` means that the router has other
   DNS servers, and a real address can come around doqd. To correct this,
   read [How doqd connects to the router DNS](#how-doqd-connects-to-the-router-dns).

**How does doqd tell a geo-blocked name from a usual one?** It uses three
sources:

- The built-in list: OpenAI, Anthropic, Gemini/AI Studio, NotebookLM, xAI.
- Your own domains: `geo-domain` in the config or `doqd add-domain`.
- Detection. For an unknown name, doqd looks at the answer of the pinned
  server. If the answer has addresses from the proxy pool of this server,
  the name is geo-blocked. After that, the name goes only to this server.

doqd classifies only A queries directly. For AAAA, HTTPS and other types, it
makes an internal A query first. A learned name stays geo-blocked until a
restart.

In HTTPS/SVCB answers for geo-blocked names, doqd removes the `ipv4hint` and
`ipv6hint` hints. They can contain the real address of the service, and the
browser would go around the proxy. If no unblocking server answered such a
query, doqd gives an empty answer (NODATA). The browser then uses the
addresses from A/AAAA.

The proxy pool grows only from answers to the probe domains: chatgpt.com,
sentinel.openai.com, tcr9i.chat.openai.com, auth.openai.com, claude.ai,
assets-proxy.anthropic.com, gemini.google.com. Without this rule, a listed
name that the server does not replace (for example, x.ai with Cloudflare
addresses) would pull all Cloudflare sites into the geo lane. Usual sites get
real addresses from the fastest plain server.

**How does doqd select the unblocking server, and why does it not jump
between servers?** At the first start (and on `doqd geo reselect`), doqd
evaluates each `geo` server:

- coverage — how many probe domains the server replaces with a live proxy;
- the median TLS handshake time to its proxies.

doqd compares the coverage first, then the speed. Servers with a median TLS
time within 20 % of the best one are equal and keep their config order. Thus
noise in the measurements does not change the selection. A server with
partial coverage becomes pinned, but the evaluation repeats (1 min, then ×2
up to 30 min) until a server with full coverage comes back.

The pinned server changes only when it fails:

- three errors in sequence on A/AAAA (SERVFAIL and REFUSED count too; errors
  on HTTPS/SVCB do not count, because geohide always resets these queries);
- or all its proxies are dead in three checks in sequence (one check every
  30 s).

Before a change, doqd tests the network with a query through the fast lane.
If the fast lane is also silent, the network is the cause, and the pinned
server stays. An evaluation where no server unblocked anything (usually no
internet) does not change the old selection and `geo.state`. It repeats
after 1 min, 2, 4... up to 30 min. A damaged `geo.state` starts a new
evaluation.

If a proxy of the pinned server becomes dead or slow, doqd immediately
removes the cached answers that contain it. The condition: the answer has a
healthy address of the same type. The filter gives an answer where all
addresses are bad without changes.

**The default servers filter something.** `dns.quad9.net` blocks malware
domains. ControlD `p0` and NextDNS without a profile of your own
(`dns.nextdns.io`) filter nothing. Quad9 without blocking:
`doqd add quic://dns10.quad9.net`, then `doqd remove` for `dns.quad9.net`.
If AdGuard is available to you: `quic://unfiltered.adguard-dns.com`.

**`doqd list` shows a server down with `bootstrap lookup ...: no bootstrap
server answered`.** The DoQ server itself can be alive. doqd did not get its
address: no bootstrap server answered the plain DNS query over UDP or TCP.
If other servers are alive, the ISP almost certainly drops DNS queries on
port 53 with the names of unblocking services. A bootstrap server on another
port helps. Yandex DNS uses port 1253. New installations already have it. In
an old config, add the line `bootstrap 77.88.8.8:1253` and restart the
daemon. Manual check: `nslookup <name> 77.88.8.8` is silent, and
`doqd test quic://<name>` answers after the change.

**`doqd list` shows a server down with `dial ...: context deadline exceeded`
or `no address answered`.** doqd found the address of the server, but the
QUIC connection (udp/853) did not start. `doqd test quic://<name>` shows
step by step which addresses it tried. If the server works from other
networks, the ISP almost certainly blocks it. doqd cannot correct this. DNS
continues to work: an unavailable server goes to the end of the queue.
Remove an unnecessary server: `doqd remove <number>`.

**I added a server, and it is down.** `doqd list` shows the liveness and the
RTT of each server. `doqd remove <number>` removes an unnecessary one.
`doqd add` tests the server and does not write a dead server without
`--force`.

**Where did comss go?** It left the defaults in 0.3.5. The DNS of comss
answered fast, but its proxies were the slowest: TLS to chatgpt.com took
0.2 to 4.7 s, through dns-ai 0.13–0.15 s. You can add comss again:
`doqd add --geo quic://dns.comss.one`.

**Where did geohide and dns-ai go?** Starting with 0.5.2, Bezmezhau DNS
(`quic://dns.bezmezhau.com`) is used by default. You can add the former servers
back at any time: `doqd add --geo quic://geohide.ru` and
`doqd add --geo quic://dns.dns-ai.ru`.

### Keenetic

**`ndmc: system failed [0xcffd0062]` / `Cli::Main: failed to initialize`,
and `doqd status` shows `registration: NOT found` or `registration:
unknown`.** doqd is installed and works. This occurs when you install from a
shell that you opened with `exec sh` in the stock router CLI (SSH on port
22, login admin). The CLI session is in use, and `ndmc` cannot open a second
one. Thus the installer could not add the name server. Type `exit` to go
back to the `(config)>` prompt, and run:

```
ip name-server <LAN-IP>:5354
system configuration save
show ip name-server
```

These commands also work in the Web CLI (`http://<router-address>/a`).
`ndmc` and `doqd status` work correctly in the Entware session (SSH on port
222, login root). Until the registration is done, queries go around doqd.

**After a router reboot, the internet is gone: IPs ping, names do not
resolve, and `doqd list` shows `lookup ... on 127.0.0.1:53: i/o timeout`.**
This is a bug of versions before 0.3.0. Update with the install command.
Since 0.3.0, doqd gets the addresses of its servers through `bootstrap`,
around the router DNS.

**doqd does not start after a router reboot.** Since 0.2.5, the daemon waits
for its address. If doqd still does not start, run `doqd status`. If no
interface has the address from `listen`, `doqd status` tells you. Usually an
upstream DHCP server gives the LAN address to the router (Relay mode), and
the address changed after the reboot. Set a permanent LAN address on the
router, correct `listen` in `/opt/etc/doqd.conf` and register doqd again:
`ip name-server <new-address>:5354`.

**Why port 5354, and not 5353?** On Keenetic, avahi-daemon (mDNS) uses port
5353.

**Why the LAN address, and not 127.0.0.1?** KeeneticOS does not accept
loopback in `ip name-server` (`Dns::Manager: invalid IP address`).

**How do I go back to 0.3.x?** Versions 0.3.x do not know the keys `geo` and
`geo-domain` and do not start with a 0.4.0+ config. Before you install an
old version, delete `/opt/etc/doqd.conf`: the old installer writes its own
config. Or change the config manually: change the `geo` lines to `upstream`,
change the `upstream` lines with Quad9 and ControlD to `fallback`, and
delete the `geo-domain` lines.

### OpenWRT

**The installer shows `doqd ... did not resolve a test name`.** doqd is
installed and works, but no DoQ server answered it. dnsmasq works with its
original servers (on a reinstall, the installer restores them), and the
router DNS works. Run `doqd list`: it shows which server is
unavailable and why. Log: `logread -e doqd`. Correct the cause and run the
installer again.

**After a reboot, DNS does not work for some seconds.** This is normal. doqd
starts after the network, but the WAN comes up later. Until the WAN is up,
doqd answers SERVFAIL. In the emulator, doqd answered 20 s after the boot.

**The router clock went back, and doqd does not work.** The log
(`logread -e doqd`) shows `x509: certificate has expired or is not yet
valid`. The installer sends the NTP names around doqd, so NTP corrects the
clock, and doqd starts to work. If you changed the NTP servers after the
installation, run the installer again: it updates the bypass. If the NTP
servers are IP addresses, no bypass is necessary.

**The router DNS does not work, and I cannot download the uninstaller.**
Give dnsmasq its servers back manually. You do not need the internet for
this:

```sh
uci del_list dhcp.@dnsmasq[0].server='127.0.0.1#5354'
uci set dhcp.@dnsmasq[0].noresolv='0'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

dnsmasq then asks the ISP DNS again. Then find the cause: `doqd list` and
`logread -e doqd`. For a full removal, use `uninstall-openwrt.sh`.

**Where is the log?** In the system log: `logread -e doqd`. All doqd lines
there have the level `daemon.err`, because doqd writes to stderr. The real
level is in the field `level=` of each line.

**Can I use doqd together with https-dns-proxy, stubby or podkop?** Yes, but
then dnsmasq has many servers. dnsmasq sends a query to one of them, and some
queries go around doqd. To send all queries through doqd, stop the other
proxy or remove its server from `dhcp.@dnsmasq[0].server`.

**Why 127.0.0.1, and not the LAN address?** Only dnsmasq on the same router
asks doqd. The address 127.0.0.1 is not available from the network, so no
device can ask doqd directly.

## Uninstall

Keenetic:

```sh
curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/uninstall.sh | sh
```

The script removes the name-server registration, stops the daemon and
removes it. The config `/opt/etc/doqd.conf` stays.

OpenWRT:

```sh
wget -qO- https://raw.githubusercontent.com/necronicle/keenetic-doq/main/uninstall-openwrt.sh | sh
```

The script first restores the original `server` and `noresolv` of dnsmasq.
The router DNS continues to work. Then the script stops the daemon and
removes it, the init script and the sysupgrade list. The config
`/etc/doqd.conf` stays.

## Building from source

You need Go 1.26 or newer.

```sh
sh scripts/build-all.sh dist          # all architectures into the directory dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o doqd ./cmd/doqd   # one architecture
```

The settings of each architecture (GOARM, GOMIPS and others) are in
`scripts/build-all.sh`.

## Limitations

- doqd encrypts the path "router → external server". The ISP sees this
  path. In the LAN, devices ask the router with plain DNS: doqd does not
  accept DoQ connections from clients. The stock DoT and DoH of KeeneticOS
  work the same way.
- doqd does not filter or block anything. It gives the answer of the server
  as it is. The exception: the filter removes dead and slow proxies of the
  unblocking server.
- doqd does not listen on the WAN. On Keenetic, it listens on the LAN
  address. On OpenWRT, it listens on 127.0.0.1.

## License

[GPL-3.0](LICENSE)
