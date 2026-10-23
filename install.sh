#!/bin/sh
# keenetic-doq installer. Run ON THE ROUTER (Entware required).
#
#   curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh | sh
#   sh install.sh --local ./doqd-linux-arm64    # offline install
#
# What it does: detects the arch, installs /opt/sbin/doqd, writes
# /opt/etc/doqd.conf (with the router's LAN address) and the Entware
# init script, starts the daemon and registers it as an extra system
# name-server (ip name-server <LAN-IP>:5354). Port 53 is never touched.
set -e

REPO="necronicle/keenetic-doq"
BIN=/opt/sbin/doqd
CONF=/opt/etc/doqd.conf
INIT=/opt/etc/init.d/S56doqd
PORT=5354

log() { echo "[keenetic-doq] $*"; }
die() { echo "[keenetic-doq] ERROR: $*" >&2; exit 1; }

# Upgrade directly to local SmartDNS. Do not replay historical migrations:
# ordinary upstream/fallback rows are user settings and must stay untouched.
check_legacy_config() {
    [ -f "$CONF" ] || return 0
    if awk '$1 == "geo-domain" {found=1} END {exit !found}' "$CONF"; then
        echo "Cannot migrate $CONF: geo-domain has no proxy IP address. Back up the config, put explicit IP/name rows for these names in a custom hosts file, set hosts /absolute/path, then remove geo-domain rows and rerun. Exact names only; subdomains need their own rows. The binary and service have not been changed." >&2
        return 1
    fi
}

migrate_smartdns() { # $1 = verified new binary, not the installed daemon
    if [ ! -f "$CONF" ]; then "$1" hosts check -c "$CONF"; return $?; fi
    check_legacy_config || return 1
    geos=$(awk '$1 == "geo" {print $2}' "$CONF" | LC_ALL=C sort | tr '\n' ' ')
    drop=no
    [ "$geos" = "quic://dns.comss.one quic://dns.dns-ai.ru quic://eu.geohide.ru " ] && drop=yes
    stage=$(mktemp "$CONF.smartdns.XXXXXX") || return 1
    # Keep permissions/ownership (configs may contain private upstream URLs).
    if ! cp -p "$CONF" "$stage"; then rm -f "$stage"; return 1; fi
    if ! awk -v drop="$drop" '
        $1 == "geo" {
            if (drop == "yes") next
            sub(/geo/, "upstream")
        }
        $1 == "hosts" {hosts=1}
        $1 == "upstream" {upstreams++}
        {print}
        END {
            if (!hosts) print "hosts builtin"
            if (!upstreams) {
                print "upstream quic://dns.quad9.net"
                print "upstream quic://p0.freedns.controld.com"
                print "upstream quic://dns.nextdns.io"
            }
        }
    ' "$CONF" > "$stage"; then rm -f "$stage"; return 1; fi
    # Validate with the new version before changing config, binary or service.
    # In particular a missing/invalid custom hosts file must leave working DNS alone.
    if ! "$1" hosts check -c "$stage"; then
        rm -f "$stage"
        echo "Config/hosts validation failed; $CONF and the running service are unchanged." >&2
        return 1
    fi
    if cmp -s "$CONF" "$stage"; then rm -f "$stage"; return 0; fi
    backup=$(mktemp "$CONF.pre-smartdns.XXXXXX") || { rm -f "$stage"; return 1; }
    if ! cp -p "$CONF" "$backup"; then rm -f "$stage" "$backup"; return 1; fi
    if ! mv "$stage" "$CONF"; then rm -f "$stage"; return 1; fi
    log "config migrated to local SmartDNS; original saved as $backup"
    if [ "$drop" = yes ]; then
        log "config: removed the exact former default geo trio"
    elif [ -n "$geos" ]; then
        log "config: custom geo servers are now ordinary upstreams for all non-local names"
    fi
}

# Keenetic CLI: ndmc (current) or ndmq (legacy)
ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then
        ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then
        ndmq -p "$1"
    else
        die "ndmc/ndmq not found — run manually in the router CLI: $1"
    fi
}

fetch() { # $1 = url, $2 = output file
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
    else
        curl -fsSL -o "$2" "$1"
    fi
}

if [ -f /etc/openwrt_release ]; then
    die "this is OpenWRT, not Keenetic — use the OpenWRT installer:
     wget -qO- https://raw.githubusercontent.com/$REPO/main/install-openwrt.sh | sh"
fi
[ -f /opt/etc/init.d/rc.func ] || die "Entware not found (/opt/etc/init.d/rc.func)"

# curl is an Entware package, not a busybox applet, and the busybox wget
# on Keenetic is built without TLS — so downloads need curl specifically.
if [ "$1" != "--local" ] && ! command -v curl >/dev/null 2>&1; then
    die "curl not found — install it first: opkg install curl
     (or copy the binary to the router and run: sh install.sh --local ./doqd-linux-<arch>)"
fi

# KeeneticOS rejects 127.0.0.1 in `ip name-server`, so doqd listens on
# the router's LAN address (br0) and that same address is registered.
LAN_IP=$(ip -4 -o addr show br0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)
[ -n "$LAN_IP" ] || LAN_IP=$(ifconfig br0 2>/dev/null | awk '/inet addr/{gsub("addr:","",$2); print $2}' | head -1)
[ -n "$LAN_IP" ] || die "cannot detect the LAN address (br0)"
log "LAN address: $LAN_IP"

arch=$(opkg print-architecture | awk '!/all|noarch/ {print $2}' | head -1)
case "$arch" in
    aarch64*) goarch=arm64 ;;
    mipsel*)  goarch=mipsle ;;
    mips*)    goarch=mips ;;
    *)        die "unsupported Entware architecture: $arch" ;;
esac
log "architecture: $arch -> doqd-linux-$goarch"

check_legacy_config || die "resolve legacy geo-domain settings before upgrading"

if [ "$1" = "--local" ]; then
    [ -f "$2" ] || die "file $2 not found"
    cp "$2" "$BIN.new"
else
    base="https://github.com/$REPO/releases/latest/download"
    log "downloading doqd-linux-$goarch"
    fetch "$base/doqd-linux-$goarch" "$BIN.new" \
        || die "download failed (no internet? private repo? use --local)"
    if command -v sha256sum >/dev/null 2>&1; then
        fetch "$base/SHA256SUMS" /opt/tmp/doqd.sums || die "SHA256SUMS download failed"
        want=$(awk -v f="doqd-linux-$goarch" '$2==f{print $1}' /opt/tmp/doqd.sums)
        got=$(sha256sum "$BIN.new" | awk '{print $1}')
        rm -f /opt/tmp/doqd.sums
        [ -n "$want" ] && [ "$got" = "$want" ] || die "SHA256 mismatch — corrupted download"
        log "SHA256 OK"
    else
        log "WARNING: sha256sum not found, skipping checksum verification"
    fi
fi
chmod 755 "$BIN.new"
"$BIN.new" -version >/dev/null || die "binary does not run (wrong architecture?)"

migrate_smartdns "$BIN.new" || die "config migration failed; fix $CONF before upgrading"

[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
mv "$BIN.new" "$BIN"

if [ ! -f "$CONF" ]; then
    cat > "$CONF" <<EOF
# doqd — DNS-over-QUIC forwarder. https://github.com/necronicle/keenetic-doq
listen $LAN_IP:$PORT

# Local exact-name proxy mappings embedded in the binary.
# To replace them: set hosts /absolute/path/to/hosts and restart doqd.
# After editing that file: doqd hosts reload
hosts builtin

# Ordinary DoQ resolvers for names outside the local hosts table.
upstream quic://dns.quad9.net
upstream quic://p0.freedns.controld.com
upstream quic://dns.nextdns.io

# Plain-DNS servers used ONLY to resolve the upstream names above. They must
# be external: any DNS on the router itself is the router's own proxy, which
# forwards to doqd — asking it would mean asking ourselves.
# All are asked at once; a server silent over UDP is retried over TCP.
# 77.88.8.8:1253 is Yandex DNS on a non-standard port, for ISPs that drop
# port-53 queries for some names.
bootstrap 77.88.8.8
bootstrap 77.88.8.8:1253
bootstrap 8.8.8.8
bootstrap 1.1.1.1

# cache: max entries / TTL bounds in seconds
cache_size 4096
min_ttl 60
max_ttl 86400

# log level: debug | info | warn | error
log info
EOF
    log "config written: $CONF"
fi

cat > "$INIT" <<'EOF'
#!/bin/sh

ENABLED=yes
PROCS=doqd
ARGS="-c /opt/etc/doqd.conf"
PREARGS=""
DESC="DNS-over-QUIC forwarder"
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
EOF
chmod 755 "$INIT"

# Register exactly the address doqd listens on per the config.
NS=$(awk '/^listen /{print $2}' "$CONF")
[ -n "$NS" ] || NS="$LAN_IP:$PORT"

"$INIT" start
sleep 1
"$INIT" check | grep -q alive || die "doqd did not start, check the logs"

log "registering name-server $NS in KeeneticOS"
ndm_cmd "ip name-server $NS" || true
ndm_cmd "system configuration save" || true

# ndmc may report "Cli::Main: failed to initialize" and still exit 0, so the
# registration is verified by reading it back instead of trusting the code.
if ndm_cmd "show ip name-server" 2>/dev/null | grep -q "${NS%:*}"; then
    log "registration confirmed"
else
    echo "[keenetic-doq] WARNING: could not register the name-server via the router CLI." >&2
    echo "[keenetic-doq] doqd is installed and running — only the registration is missing," >&2
    echo "[keenetic-doq] so queries still go through the stock DNS, not through doqd." >&2
    echo "[keenetic-doq] This happens in a shell started with \`exec sh\` inside the router CLI:" >&2
    echo "[keenetic-doq] ndmc cannot open a nested CLI session there. Type \`exit\` to get back" >&2
    echo "[keenetic-doq] to the (config)> prompt (or open Web CLI at http://$LAN_IP/a) and run:" >&2
    echo "[keenetic-doq]     ip name-server $NS" >&2
    echo "[keenetic-doq]     system configuration save" >&2
    echo "[keenetic-doq] (\`doqd status\` from that same shell cannot verify the registration either;" >&2
    echo "[keenetic-doq]  check with \`show ip name-server\` in the CLI, or from the Entware SSH on port 222)" >&2
fi

log "done. Useful commands:"
log "  doqd status                       # health check"
log "  doqd list                         # upstreams with live probes"
log "  doqd add quic://dns.example.com   # add your own DoQ upstream"
