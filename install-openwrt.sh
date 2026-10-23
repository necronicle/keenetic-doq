#!/bin/sh
# keenetic-doq installer for OpenWRT. Run ON THE ROUTER.
#
#   wget -qO- https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install-openwrt.sh | sh
#   sh install-openwrt.sh --local ./doqd-linux-<arch>    # offline install
#
# What it does: picks the binary for the router architecture, installs
# /usr/sbin/doqd, writes /etc/doqd.conf (listen 127.0.0.1:5354) and the procd
# init script /etc/init.d/doqd, and starts the daemon. When doqd answers, it
# makes doqd the only upstream server of dnsmasq. The names of the NTP servers
# go to the bootstrap servers as plain DNS: after a reboot the clock can be
# wrong, and doqd cannot verify TLS certificates until NTP sets the clock.
# If doqd does not answer, dnsmasq stays as it was.
set -e

REPO="necronicle/keenetic-doq"
BIN=/usr/sbin/doqd
CONF=/etc/doqd.conf
STATE_DIR=/etc/doqd
# What the installer changed in dnsmasq, so that uninstall can undo exactly that.
SAVED=$STATE_DIR/dnsmasq.saved
INIT=/etc/init.d/doqd
KEEP=/lib/upgrade/keep.d/doqd
PORT=5354
DNSMASQ="dhcp.@dnsmasq[0]"
TMPBIN=/tmp/doqd.download

log() { echo "[keenetic-doq] $*"; }
warn() { echo "[keenetic-doq] WARNING: $*" >&2; }
die() { echo "[keenetic-doq] ERROR: $*" >&2; rm -f "$TMPBIN" /tmp/doqd.sums; exit 1; }

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

# OpenWRT package architecture (DISTRIB_ARCH) -> release binary suffix.
# ARM: hard-float ARMv7 cores (with VFP or NEON) get armv7; every other ARM
# core from ARMv5 up gets the soft-float armv5 build. Go has no build for
# ARMv4 (fa526), big-endian ARM, PowerPC or ARC.
binary_for() {
    case "$1" in
        aarch64_*)                          echo arm64 ;;
        arm_cortex-a*_neon*|arm_cortex-a*_vfp*) echo armv7 ;;
        arm_cortex-a*|arm_arm926ej-s|arm_arm1176jzf-s*|arm_mpcore*|arm_xscale|arm_arm1136*) echo armv5 ;;
        mipsel_*)                           echo mipsle ;;
        mips_*)                             echo mips ;;
        mips64el_*)                         echo mips64le ;;
        mips64_*)                           echo mips64 ;;
        x86_64)                             echo amd64 ;;
        i386_*)                             echo 386 ;;
        riscv64_*)                          echo riscv64 ;;
        loongarch64_*)                      echo loong64 ;;
        *)                                  echo "" ;;
    esac
}

fetch() { # $1 = url, $2 = output file
    if command -v curl >/dev/null 2>&1; then
        if [ -n "$GITHUB_TOKEN" ]; then
            curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
        else
            curl -fsSL -o "$2" "$1"
        fi
    elif command -v uclient-fetch >/dev/null 2>&1; then
        uclient-fetch -q -O "$2" "$1"
    else
        wget -q -O "$2" "$1"
    fi
}

# Free space in KiB on the file system that holds $1.
free_kb() {
    df -k "$1" 2>/dev/null | tail -n 1 | awk '{print $(NF-2)}'
}

# 127.0.0.1:5354 -> 127.0.0.1#5354, [::1]:5354 -> ::1#5354 (dnsmasq syntax).
dnsmasq_addr() {
    echo "$1" | sed -e 's/^\[\(.*\)\]:\([0-9]*\)$/\1#\2/' -e 't' -e 's/^\([0-9.]*\):\([0-9]*\)$/\1#\2/'
}

# The dnsmasq server entry for the `listen` address of doqd; empty if the
# address cannot be read. A wildcard address listens on loopback too, but
# dnsmasq needs a real address.
dnsmasq_target() {
    t=$(dnsmasq_addr "$1")
    case "$t" in
        "#"[0-9]*|"0.0.0.0#"[0-9]*|"::#"[0-9]*) echo "127.0.0.1#${t#*#}" ;;
        ?*"#"[0-9]*) echo "$t" ;;
    esac
}

# The bootstrap servers of the config in dnsmasq syntax; without a port, :53.
bootstrap_servers() {
    awk '$1 == "bootstrap" {print $2}' "$CONF" | while read -r b; do
        case "$b" in
            \[*\]:*|*.*:*) dnsmasq_addr "$b" ;;
            *) echo "$b" ;;
        esac
    done
}

# Undo what the installer did to dnsmasq, using $SAVED. Used by a failed
# install; uninstall-openwrt.sh has the same steps.
restore_dnsmasq() {
    [ -f "$SAVED" ] || return 0
    target=$(sed -n 's/^target=//p' "$SAVED")
    if [ -n "$target" ]; then
        uci -q del_list "$DNSMASQ.server=$target"
    fi
    sed -n 's/^ntp=//p' "$SAVED" | while read -r e; do
        uci -q del_list "$DNSMASQ.server=$e"
    done
    was=$(sed -n 's/^noresolv=//p' "$SAVED")
    if [ -n "$was" ]; then
        uci set "$DNSMASQ.noresolv=$was"
    else
        uci -q delete "$DNSMASQ.noresolv" || true
    fi
    uci commit dhcp
    /etc/init.d/dnsmasq restart >/dev/null 2>&1 || true
    rm -f "$SAVED"
}

# NTP server names as dnsmasq domains, like /openwrt.pool.ntp.org/ntp.example.
# A domain covers its subdomains, so 0..3.openwrt.pool.ntp.org become one
# entry (the same as the OpenWRT guide "Race conditions with sysntpd").
# IP addresses need no DNS and are skipped.
ntp_domains() {
    for n in $(uci -q get system.ntp.server || true); do
        case "$n" in
            *:*|*[!0-9a-zA-Z.-]*) continue ;;   # IPv6 or not a host name
        esac
        case "$n" in *[a-zA-Z]*) ;; *) continue ;; esac   # IPv4 address
        echo "$n" | sed 's/^[0-9]*\.//'
    done | sort -u | tr '\n' '/' | sed 's|^|/|; s|/$||'
}

# The entry is in the dnsmasq server list (UCI).
has_server() {
    uci -q get "$DNSMASQ.server" | tr ' ' '\n' | grep -qxF "$1"
}

# The daemon answers a query itself: `doqd status` asks it directly.
doqd_answers() {
    "$BIN" status 2>/dev/null | grep -q '^resolve via doqd: NOERROR'
}

# --- checks -----------------------------------------------------------------

[ -f /etc/openwrt_release ] || die "this is not OpenWRT (no /etc/openwrt_release).
     For Keenetic use: curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sh"
[ "$(id -u)" = 0 ] || die "run as root"
command -v uci >/dev/null 2>&1 || die "uci not found"
if [ ! -x /etc/init.d/dnsmasq ] || ! uci -q get "$DNSMASQ" >/dev/null; then
    die "dnsmasq not found: doqd on OpenWRT works as the upstream server of dnsmasq"
fi

# shellcheck disable=SC1091
. /etc/openwrt_release
arch=$DISTRIB_ARCH
suffix=$(binary_for "$arch")
[ -n "$suffix" ] || die "architecture $arch is not supported (Go has no build for it)"
log "OpenWRT $DISTRIB_RELEASE, architecture $arch -> doqd-linux-$suffix"

# --- binary -----------------------------------------------------------------

get_binary() { # $1 = suffix
    base="https://github.com/$REPO/releases/latest/download"
    log "downloading doqd-linux-$1"
    fetch "$base/doqd-linux-$1" "$TMPBIN" || return 1
    fetch "$base/SHA256SUMS" /tmp/doqd.sums || die "SHA256SUMS download failed"
    want=$(awk -v f="doqd-linux-$1" '$2==f{print $1}' /tmp/doqd.sums)
    got=$(sha256sum "$TMPBIN" | awk '{print $1}')
    rm -f /tmp/doqd.sums
    [ -n "$want" ] && [ "$got" = "$want" ] || die "SHA256 mismatch — corrupted download"
    log "SHA256 OK"
}

check_legacy_config || die "resolve legacy geo-domain settings before upgrading"

if [ "$1" = "--local" ]; then
    [ -f "$2" ] || die "file $2 not found"
    cp "$2" "$TMPBIN"
else
    if ! command -v curl >/dev/null 2>&1 && ! command -v uclient-fetch >/dev/null 2>&1 \
        && ! command -v wget >/dev/null 2>&1; then
        die "no download tool (curl, uclient-fetch or wget) — copy the binary to the router
     and run: sh install-openwrt.sh --local ./doqd-linux-$suffix"
    fi
    get_binary "$suffix" || die "download failed (no internet? use --local)"
fi
chmod 755 "$TMPBIN"
if ! "$TMPBIN" -version >/dev/null 2>&1; then
    # An ARMv7 core without a floating-point unit cannot run the hard-float build.
    if [ "$suffix" = armv7 ] && [ "$1" != "--local" ]; then
        log "the armv7 build does not run here (no FPU?), trying armv5"
        suffix=armv5
        get_binary "$suffix" || die "download failed"
        chmod 755 "$TMPBIN"
    fi
    "$TMPBIN" -version >/dev/null 2>&1 || die "the binary does not run on this router (wrong architecture?)"
fi
log "binary: $("$TMPBIN" -version)"

# The binary goes to flash. Do not fill the flash: a full overlay breaks every
# later configuration change. JFFS2 and UBIFS compress files; their
# compressors (zlib, lzo, lzma) do not always reach gzip, so the estimate is
# the gzip size plus half. Other file systems store the full size.
size_kb=$(( ($(wc -c < "$TMPBIN") + 1023) / 1024 ))
need_kb=$((size_kb + 256))
fstype=$(awk '$2 == "/overlay" {t = $3} END {print t}' /proc/mounts)
case "$fstype" in
    jffs2|ubifs)
        if gz=$(gzip -c "$TMPBIN" 2>/dev/null | wc -c) && [ "$gz" -gt 0 ]; then
            # shellcheck disable=SC2017 # Round up KiB before adding flash overhead.
            need_kb=$(( (gz + 1023) / 1024 * 3 / 2 + 256 ))
        fi
        ;;
esac
free=$(free_kb /usr/sbin)
if [ -n "$free" ] && [ "$free" -lt "$need_kb" ] && [ ! -f "$BIN" ]; then
    die "not enough flash: about $need_kb KiB needed, $free KiB free"
fi

# Copy next to the old binary and rename: the running daemon keeps its file.
# If the flash cannot hold two copies, replace the old one in place.
install_binary() {
    if [ -z "$free" ] || [ "$free" -ge "$need_kb" ] || [ ! -f "$BIN" ]; then
        if cp "$TMPBIN" "$BIN.new" 2>/dev/null; then
            mv -f "$BIN.new" "$BIN"
            return 0
        fi
        rm -f "$BIN.new"
    fi
    [ -f "$BIN" ] || return 1
    log "not enough flash for two copies — replacing the binary in place"
    if [ -x "$INIT" ]; then
        "$INIT" stop >/dev/null 2>&1 || true
    fi
    rm -f "$BIN"
    cp "$TMPBIN" "$BIN" 2>/dev/null && return 0
    rm -f "$BIN"
    return 1
}
# Without its binary doqd must not stay half installed: procd would restart a
# missing file forever, and sysupgrade would keep a dead entry.
remove_doqd() {
    restore_dnsmasq
    if [ -x "$INIT" ]; then
        "$INIT" stop >/dev/null 2>&1 || true
        "$INIT" disable >/dev/null 2>&1 || true
    fi
    rm -f "$BIN" "$BIN.new" "$INIT" "$KEEP"
}
migrate_smartdns "$TMPBIN" || die "config migration failed; fix $CONF before upgrading"

if ! install_binary; then
    remove_doqd
    die "cannot write $BIN (flash full?). doqd removed, dnsmasq uses its own servers again"
fi
rm -f "$TMPBIN"
left=$(free_kb /usr/sbin)
if [ -n "$left" ] && [ "$left" -lt 128 ]; then
    remove_doqd
    die "the flash is almost full after the copy ($left KiB free) — doqd removed"
fi

# --- config, init script, sysupgrade ----------------------------------------

mkdir -p "$STATE_DIR"
if [ ! -f "$CONF" ]; then
    cat > "$CONF" <<EOF
# doqd — DNS-over-QUIC forwarder. https://github.com/necronicle/keenetic-doq
# dnsmasq sends every query here (server 127.0.0.1#$PORT in /etc/config/dhcp).
listen 127.0.0.1:$PORT

# Local exact-name proxy mappings embedded in the binary.
# To replace them: set hosts /absolute/path/to/hosts and restart doqd.
# After editing that file: doqd hosts reload
hosts builtin

# Ordinary DoQ resolvers for names outside the local hosts table.
upstream quic://dns.quad9.net
upstream quic://p0.freedns.controld.com
upstream quic://dns.nextdns.io

# Plain-DNS servers used ONLY to resolve the upstream names above, and by
# dnsmasq for the NTP server names. They must be external: dnsmasq on the
# router forwards to doqd — asking it would mean asking ourselves.
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

# log level: debug | info | warn | error (read the log with: logread -e doqd)
log info
EOF
    log "config written: $CONF"
fi

LISTEN=$(awk '$1 == "listen" {print $2}' "$CONF")
[ -n "$LISTEN" ] || LISTEN="127.0.0.1:$PORT"
TARGET=$(dnsmasq_target "$LISTEN")
[ -n "$TARGET" ] || die "cannot read the listen address \"$LISTEN\" in $CONF"

cat > "$INIT" <<'EOF'
#!/bin/sh /etc/rc.common
# doqd — DNS-over-QUIC forwarder. https://github.com/necronicle/keenetic-doq

# After network (20): lo and WAN are up, so the first bind does not fail.
START=21
STOP=89
USE_PROCD=1

PROG=/usr/sbin/doqd
CONF=/etc/doqd.conf

start_service() {
	procd_open_instance
	procd_set_param command "$PROG" -c "$CONF"
	# dnsmasq sends every query to doqd: restart it always, not 5 times.
	procd_set_param respawn 3600 5 0
	procd_set_param stderr 1
	procd_set_param file "$CONF"
	procd_close_instance
}
EOF
chmod 755 "$INIT"
# disable first: an older version may have used another START number.
"$INIT" disable 2>/dev/null || true
"$INIT" enable

# sysupgrade with "keep settings" keeps /etc/config/dhcp, where dnsmasq points
# to doqd. Keep doqd too, or the router has no DNS after the upgrade.
mkdir -p "$(dirname "$KEEP")"
{
    echo "$CONF"
    echo "$STATE_DIR/"
    # Preserve a custom hosts source even when it lives outside /etc/doqd.
    awk '$1 == "hosts" && $2 != "builtin" {print $2}' "$CONF"
    echo "$BIN"
    echo "$INIT"
    # The list itself, or the next sysupgrade would not keep doqd any more.
    echo "$KEEP"
    ls /etc/rc.d/*doqd 2>/dev/null || true
} > "$KEEP"

"$INIT" stop >/dev/null 2>&1 || true
"$INIT" start
log "waiting for doqd to answer"
tries=0
until doqd_answers; do
    tries=$((tries + 1))
    if [ "$tries" -ge 5 ]; then
        warn "doqd is installed and running, but it did not resolve a test name."
        if [ -f "$SAVED" ]; then
            restore_dnsmasq
            warn "dnsmasq uses the WAN DNS servers again, so the router DNS keeps working."
        else
            warn "dnsmasq is left as it was, so the router DNS keeps working."
        fi
        warn "Check the servers with: doqd list   and the log with: logread -e doqd"
        warn "Then run this installer again."
        exit 1
    fi
    sleep 3
done
log "doqd answers on $LISTEN"

# --- dnsmasq ----------------------------------------------------------------

port=$(uci -q get "$DNSMASQ.port" || true)
if [ -n "$port" ] && [ "$port" != 53 ]; then
    warn "dnsmasq does not serve DNS on port 53 here (port $port): another DNS server"
    warn "answers the clients (AdGuard Home?). Set its upstream to $LISTEN yourself."
fi

# Remember the original state once: a second run must not overwrite it.
if [ ! -f "$SAVED" ]; then
    echo "noresolv=$(uci -q get "$DNSMASQ.noresolv" || true)" > "$SAVED"
fi
# $SAVED lists only the entries the installer added. An entry the user had
# before stays theirs: uninstall must not remove it.
add_server() { # $1 = entry, $2 = record key
    if has_server "$1"; then
        return 0
    fi
    uci add_list "$DNSMASQ.server=$1"
    echo "$2=$1" >> "$SAVED"
}

# The doqd entry. After a change of `listen` the old entry is removed.
old_target=$(sed -n 's/^target=//p' "$SAVED")
sed -i '/^target=/d' "$SAVED"
if [ -n "$old_target" ]; then
    uci -q del_list "$DNSMASQ.server=$old_target"
fi
add_server "$TARGET" target

# The NTP server names: plain DNS through the bootstrap servers (see the top).
# The old entries go first: the NTP servers or the bootstrap list can change.
sed -n 's/^ntp=//p' "$SAVED" | while read -r e; do
    uci -q del_list "$DNSMASQ.server=$e"
done
sed -i '/^ntp=/d' "$SAVED"
ntp_names=$(ntp_domains)
if [ -n "$ntp_names" ]; then
    for b in $(bootstrap_servers); do
        add_server "$ntp_names/$b" ntp
    done
fi

uci set "$DNSMASQ.noresolv=1"
uci commit dhcp
# A failed restart is caught by the check through port 53 below.
/etc/init.d/dnsmasq restart >/dev/null 2>&1 || true

# Other servers for every name stay as they are, but they compete with doqd.
others=""
for s in $(uci -q get "$DNSMASQ.server" || true); do
    case "$s" in
        /*|"$TARGET") ;;
        *) others="$others $s" ;;
    esac
done
if [ -n "$others" ]; then
    warn "dnsmasq has other servers for all names:$others"
    warn "It can send queries there instead of doqd. If another DNS proxy set them"
    warn "(https-dns-proxy, stubby, podkop...), remove them or that proxy:"
    warn "    uci del_list $DNSMASQ.server='<server>'; uci commit dhcp; /etc/init.d/dnsmasq restart"
fi

# The end-to-end check: a query through dnsmasq on port 53. procd restarts
# dnsmasq in the background, so give it a few seconds.
ok=no
for _ in 1 2 3 4 5; do
    sleep 2
    if "$BIN" status 2>/dev/null | grep -q '^resolve via :53:  NOERROR'; then
        ok=yes
        break
    fi
done
if [ "$ok" = yes ]; then
    log "dnsmasq forwards to doqd ($TARGET), the WAN DNS servers are off"
else
    restore_dnsmasq
    die "dnsmasq did not answer through doqd — dnsmasq is restored, doqd stays installed.
     See: doqd status   and   logread -e doqd"
fi

log "done. Useful commands:"
log "  doqd status                       # health check"
log "  doqd list                         # upstreams with live probes"
log "  doqd add quic://dns.example.com   # add your own DoQ upstream"
log "  logread -e doqd                   # daemon log"
