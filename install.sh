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

# 0.3.4: Quad9 turns into a fallback. Before, queries went to whichever
# upstream answered first, and Quad9 regularly overtook comss with the real
# address of a geo-blocked service. A config still holding the old built-in
# pair also gets the other two unblocking servers. Configs that already use
# `fallback`, or where Quad9 is the only upstream, are left alone.
migrate_conf() {
    grep -q '^fallback[[:space:]]' "$CONF" && return 0
    grep -qx 'upstream quic://dns.quad9.net' "$CONF" || return 0
    n=$(grep -c '^upstream[[:space:]]' "$CONF")
    [ "$n" -gt 1 ] || return 0
    add=no
    [ "$n" -eq 2 ] && grep -qx 'upstream quic://dns.comss.one' "$CONF" && add=yes
    last=$(grep -n '^upstream[[:space:]]' "$CONF" | tail -n 1 | cut -d: -f1)
    awk -v last="$last" -v add="$add" '
        function fallback() {
            print ""
            print "# Fallback: asked only when every upstream above has failed. It returns real"
            print "# addresses, so it must never overtake them. Add with: doqd add --fallback"
            print "fallback quic://dns.quad9.net"
        }
        /^# DoQ upstreams, in order of preference/ || /^# DoQ-апстримы, в порядке предпочтения/ {
            print "# DoQ upstreams: queries go to the fastest live one. Manage with: doqd add / doqd remove"
            next
        }
        $0 == "upstream quic://dns.quad9.net" { if (NR == last) fallback(); next }
        { print }
        add == "yes" && $0 == "upstream quic://dns.comss.one" {
            print "upstream quic://geohide.ru"
            print "upstream quic://dns.dns-ai.ru"
        }
        NR == last { fallback() }
    ' "$CONF" > "$CONF.new" && mv "$CONF.new" "$CONF" || { rm -f "$CONF.new"; return 1; }
    log "config: quic://dns.quad9.net is now a fallback (asked only when the others fail)"
    [ "$add" = yes ] && log "config: added upstreams quic://geohide.ru and quic://dns.dns-ai.ru"
    return 0
}

# Existing config is preserved on reinstall/upgrade.
[ -f "$CONF" ] && { migrate_conf || log "WARNING: could not update $CONF, left as is"; }
if [ ! -f "$CONF" ]; then
    cat > "$CONF" <<EOF
# doqd — DNS-over-QUIC forwarder. https://github.com/necronicle/keenetic-doq
listen $LAN_IP:$PORT

# DoQ upstreams: queries go to the fastest live one. Manage with: doqd add / doqd remove
# These three answer with their proxy addresses for services blocked by
# geolocation (ChatGPT, Gemini, Claude...).
upstream quic://dns.comss.one
upstream quic://geohide.ru
upstream quic://dns.dns-ai.ru

# Fallback: asked only when every upstream above has failed. It returns real
# addresses, so it must never overtake them. Add with: doqd add --fallback
fallback quic://dns.quad9.net

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
