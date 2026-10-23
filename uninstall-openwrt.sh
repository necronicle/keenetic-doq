#!/bin/sh
# keenetic-doq uninstaller for OpenWRT. Run ON THE ROUTER.
#
#   wget -qO- https://raw.githubusercontent.com/necronicle/keenetic-doq/main/uninstall-openwrt.sh | sh
#
# Gives dnsmasq back its servers first, so the router DNS keeps working, then
# stops and removes the daemon. The config /etc/doqd.conf is kept.
set -e

BIN=/usr/sbin/doqd
CONF=/etc/doqd.conf
STATE_DIR=/etc/doqd
SAVED=$STATE_DIR/dnsmasq.saved
INIT=/etc/init.d/doqd
KEEP=/lib/upgrade/keep.d/doqd
DNSMASQ="dhcp.@dnsmasq[0]"

log() { echo "[keenetic-doq] $*"; }

[ -f /etc/openwrt_release ] || { echo "[keenetic-doq] ERROR: this is not OpenWRT — for Keenetic use uninstall.sh" >&2; exit 1; }

if [ -f "$SAVED" ]; then
    target=$(sed -n 's/^target=//p' "$SAVED")
    if [ -n "$target" ]; then
        uci -q del_list "$DNSMASQ.server=$target" || true
    fi
    sed -n 's/^ntp=//p' "$SAVED" | while read -r e; do
        uci -q del_list "$DNSMASQ.server=$e" || true
    done
    was=$(sed -n 's/^noresolv=//p' "$SAVED")
    if [ -n "$was" ]; then
        uci set "$DNSMASQ.noresolv=$was"
    else
        uci -q delete "$DNSMASQ.noresolv" || true
    fi
    uci commit dhcp
    /etc/init.d/dnsmasq restart >/dev/null 2>&1 || true
    log "dnsmasq: doqd removed from its servers, noresolv restored"
else
    # No record of the installer changes: remove the doqd server only.
    # The same conversion as in install-openwrt.sh.
    listen=$(awk '$1 == "listen" {print $2}' "$CONF" 2>/dev/null || true)
    target=$(echo "${listen:-127.0.0.1:5354}" | sed -e 's/^\[\(.*\)\]:\([0-9]*\)$/\1#\2/' -e 't' -e 's/^\([0-9.]*\):\([0-9]*\)$/\1#\2/')
    case "$target" in
        "#"[0-9]*|"0.0.0.0#"[0-9]*|"::#"[0-9]*) target="127.0.0.1#${target#*#}" ;;
    esac
    if uci -q get "$DNSMASQ.server" | tr ' ' '\n' | grep -qxF "$target"; then
        uci -q del_list "$DNSMASQ.server=$target" || true
        uci commit dhcp
        /etc/init.d/dnsmasq restart >/dev/null 2>&1 || true
        log "dnsmasq: server $target removed"
    fi
    if [ "$(uci -q get "$DNSMASQ.noresolv" || true)" = 1 ]; then
        log "WARNING: dnsmasq has noresolv=1. If it has no other servers now, turn it off:"
        log "    uci set $DNSMASQ.noresolv=0; uci commit dhcp; /etc/init.d/dnsmasq restart"
    fi
fi

if [ -x "$INIT" ]; then
    "$INIT" stop >/dev/null 2>&1 || true
    "$INIT" disable >/dev/null 2>&1 || true
fi
rm -f "$INIT" "$BIN" "$BIN.new" "$KEEP"
rm -f "$SAVED" "$STATE_DIR/geo.state" /tmp/doqd.state.json
rmdir "$STATE_DIR" 2>/dev/null || true
log "config $CONF kept (delete it manually if unwanted)"
log "done"
