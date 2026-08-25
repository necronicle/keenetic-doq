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

[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
mv "$BIN.new" "$BIN"

# Existing config is preserved on reinstall/upgrade.
if [ ! -f "$CONF" ]; then
    cat > "$CONF" <<EOF
# doqd — DNS-over-QUIC forwarder. https://github.com/necronicle/keenetic-doq
listen $LAN_IP:$PORT

# DoQ upstreams, in order of preference. Manage with: doqd add / doqd remove
upstream quic://dns.comss.one
upstream quic://dns.quad9.net

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

# ndmc reports "Cli::Main: failed to initialize" yet still exits 0, so the
# registration is verified by reading it back instead of trusting the code.
if ndm_cmd "show ip name-server" 2>/dev/null | grep -q "${NS%:*}"; then
    log "registration confirmed"
else
    echo "[keenetic-doq] WARNING: could not register the name-server via the router CLI." >&2
    echo "[keenetic-doq] doqd is installed and running — only the registration is missing," >&2
    echo "[keenetic-doq] so queries still go through the stock DNS, not through doqd." >&2
    echo "[keenetic-doq] Finish it in the router Web CLI — open http://$LAN_IP/a and run:" >&2
    echo "[keenetic-doq]     ip name-server $NS" >&2
    echo "[keenetic-doq]     system configuration save" >&2
    echo "[keenetic-doq] (ndmc over SSH fails this way when the session runs in the OPKG" >&2
    echo "[keenetic-doq]  environment or the account has no command-line rights)" >&2
fi

log "done. Useful commands:"
log "  doqd status                       # health check"
log "  doqd list                         # upstreams with live probes"
log "  doqd add quic://dns.example.com   # add your own DoQ upstream"
