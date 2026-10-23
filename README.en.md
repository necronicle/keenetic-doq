# keenetic-doq

[Русский](README.md)

![CI](https://github.com/necronicle/keenetic-doq/actions/workflows/ci.yml/badge.svg)
![Release](https://img.shields.io/github/v/release/necronicle/keenetic-doq)

**Telegram: [DoQ topic in @zapret2keenetic](https://t.me/zapret2keenetic/55256)** — questions, setup help, discussion (in Russian)  
**Web UI: [keenetic-doq-web](https://github.com/accept4/keenetic-doq-web)** — web management panel by @accept4 (Keenetic)

DNS-over-QUIC for **Keenetic** and **OpenWRT**, with local SmartDNS.

`doqd` answers names from a local hosts table with the proxy addresses in that
table. All other names use ordinary DoQ upstreams and the existing DNS cache.
The separate geo-DNS lane, automatic classification and pinned geo server have
been removed.

```text
clients → router DNS → doqd → hosts match: local proxy IP response
                           → other names: cache → ordinary DoQ upstream
```

The binary includes **1025 exact names**, a snapshot dated October 24, 2026:
GeoHide EU hosts plus Sora names and `releases.openai.com` for Codex downloads.
The proxy addresses are `5.175.161.3` and `5.175.161.40`. No Cloudflare Zero Trust
account is required. **The third-party proxies are still required:** replacing
DNS answers does not create a foreign network exit or guarantee service access.

Ordinary upstream selection prefers the healthy primary with the lowest smoothed
DNS response time. If it stalls, another attempt starts in parallel. Requests
are not broadcast to every server. Fallback upstreams, caching, coalescing of
identical requests and stale responses during upstream failure are retained.

## Local rules

- Exact names, case-insensitive, ignoring the trailing dot. `example.com`
  does not include arbitrary subdomains.
- A/AAAA answers contain only addresses from hosts. Missing IPv6 records
  produce NOERROR/NODATA, not NXDOMAIN and not direct upstream addresses.
- HTTPS/SVCB for matched names return NODATA, without direct IP hints or ECH
  configuration that could bypass the proxy. HTTPS connections remain available.
- Local TTL is 60 seconds. Synthesized answers do not assert DNSSEC validation.
- No automatic hosts downloads or proxy-provider failover: the listed addresses
  are the source of truth.

These guarantees apply to queries reaching doqd. Client-side DNS, VPNs, other
router DNS servers and old client caches can bypass the rules.

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

For earlier releases, OpenWRT 24.10.8 and 25.12.5 were tested in the QEMU emulator on aarch64,
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

Existing configuration is retained subject to the legacy migration described below.

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

```sh
doqd list
doqd test quic://dns.quad9.net
doqd add quic://dns.quad9.net
doqd add --fallback quic://host
doqd remove <number-or-URL>
doqd status
doqd hosts status     # configured source and rule count, not live daemon state
doqd hosts check      # validate the configured table
doqd hosts export     # print the embedded snapshot
doqd hosts reload     # request an atomic hosts reload from the daemon
```

Use `-c /path/doqd.conf` for a custom configuration. `geo`, `geo reselect`,
`--geo`, `add-domain` and `remove-domain` have been removed. A former geo-DNS
provider can be added explicitly as an ordinary upstream with `doqd add`.
Local hosts rules take precedence over its answers.

## Configuration

Keenetic: `/opt/etc/doqd.conf`. OpenWRT: `/etc/doqd.conf`.

```text
listen 127.0.0.1:5354
hosts builtin
upstream quic://dns.quad9.net
upstream quic://p0.freedns.controld.com
upstream quic://dns.nextdns.io
cache_size 4096
min_ttl 60
max_ttl 86400
log info
```

On Keenetic, the installer uses the router LAN address for listen. `fallback`
adds a reserve DoQ server; `bootstrap` specifies an external DNS IP for resolving
upstream hostnames without a loop through doqd. The first upstream/fallback line
replaces the default server list. At least one primary upstream is required.

### Custom hosts

Export the complete snapshot on Keenetic:

```sh
doqd hosts export > /opt/etc/doqd.hosts
```

On OpenWRT use `/etc/doqd/hosts` (create the directory if necessary). Set
`hosts /absolute/path/to/file` in the configuration and restart doqd once.
SIGHUP reloads the selected hosts file, not the entire configuration.

File format:

```text
5.175.161.3 chatgpt.com releases.openai.com
5.175.161.40 chatgpt.com releases.openai.com
# Multiple names per line and multiple addresses per name are supported.
```

This is a format example, not the full service list. A custom file **replaces**
the embedded snapshot. After editing, use `doqd hosts check` and `doqd hosts reload`.
Reload is atomic; invalid input leaves the last working table active. The reload
command confirms the request, not successful application: check the daemon log.
The ordinary DNS cache cannot override local rules.

### Upgrading from 0.5.x

Use the new installer, not just a binary replacement. The daemon rejects old
`geo` and `geo-domain` directives. Installers back up changed configurations,
remove the exact former default geo trio and convert custom geo servers into
ordinary upstreams while preserving existing upstream/fallback settings.
Custom `geo-domain` entries cannot be translated without proxy IPs: installation
stops with instructions before modifying the running installation. Put the
required names and known proxy IPs in hosts, remove the old directives and retry.
Third-party management UIs may need updating for the new CLI.

## Verification

```sh
doqd -version
doqd hosts check
doqd list
doqd status
nslookup chatgpt.com
```

With `dig`, query doqd directly (replace 127.0.0.1 with LAN-IP on Keenetic):

```sh
dig @127.0.0.1 -p 5354 releases.openai.com A
dig @127.0.0.1 -p 5354 releases.openai.com AAAA
dig @127.0.0.1 -p 5354 releases.openai.com TYPE65
```

With the embedded table, A returns two proxy IPs; AAAA and HTTPS return successful
empty answers. Other names such as `example.com` resolve through upstreams.
A TLS connection or HTTP 403 alone does not prove full service functionality;
test the site and its authenticated workflow. On OpenWRT use `logread -e doqd`.

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

- DoQ encrypts DNS between the router and upstream; LAN queries remain plain DNS.
- Hosts is a snapshot, not automatic discovery of every service dependency.
- Proxy health checks and automatic removal of failed proxy addresses are not
  part of this version. A dead proxy requires updating the table.
- Client-side DNS, VPNs and competing router DNS can bypass doqd.
- doqd is not a VPN and does not proxy application traffic itself.

## License

[GPL-3.0](LICENSE)
