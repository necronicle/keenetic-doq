#!/bin/sh
# keenetic-doq uninstaller. Run ON THE ROUTER.
# Unregisters the name-server, stops and removes the daemon and its config.
# To upgrade or reinstall keeping the settings, rerun install.sh instead:
# it never overwrites an existing config.
set -e

CONF=/opt/etc/doqd.conf

log() { echo "[keenetic-doq] $*"; }

if [ -f /etc/openwrt_release ]; then
    echo "[keenetic-doq] ERROR: this is OpenWRT — use uninstall-openwrt.sh" >&2
    exit 1
fi

ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then ndmq -p "$1"
    else echo "[keenetic-doq] run manually in the router CLI: $1"; fi
}

# The local RCI API needs no login and, unlike ndmc, also works in a shell
# started with `exec sh` from the router CLI. It answers HTTP 200 even when a
# command fails, so the verdict comes from the "status" field of the reply.
rci() {
    command -v curl >/dev/null 2>&1 || return 1
    reply=$(curl -fsS -m 10 -H 'Content-Type: application/json' \
        -d "[{\"parse\":\"$1\"}]" http://127.0.0.1:79/rci/ 2>/dev/null | tr -d ' \t\r\n')
    case $reply in
        *'"status":"error"'*) return 1 ;;
        *'"prompt"'*) printf '%s' "$reply" ;;
        *) return 1 ;;
    esac
}

unregister_via_rci() {
    # Removing an absent entry is an error too, so the read-back decides.
    rci "no ip name-server $NS" >/dev/null || true
    shown=$(rci "show ip name-server") || return 1
    case $shown in *"\"address\":\"${NS%:*}\""*) return 1 ;; esac
    rci "system configuration save" >/dev/null
}

# ndmc can fail while still exiting 0, and with neither tool present ndm_cmd only
# prints a hint, so a clean status alone proves nothing.
ndm_try() {
    out=$(ndm_cmd "$1" 2>&1) || return 1
    case $out in
        *"failed to initialize"*|*"system failed"*|*"run manually"*) return 1 ;;
    esac
    printf '%s' "$out"
}

unregister_via_ndm() {
    # ndmc exits non-zero for an absent entry, so here too the read-back decides.
    ndm_try "no ip name-server $NS" >/dev/null || true
    shown=$(ndm_try "show ip name-server") || return 1
    case $shown in *"${NS%:*}"*) return 1 ;; esac
    ndm_try "system configuration save" >/dev/null
}

NS=$(awk '/^listen /{print $2}' "$CONF" 2>/dev/null) || true
removal_unconfirmed=

if [ -n "$NS" ]; then
    log "unregistering name-server $NS"
    if unregister_via_rci || unregister_via_ndm; then
        log "name-server removed"
    else
        removal_unconfirmed=1
        log "WARNING: could not confirm name-server removal. Run in the router Web CLI"
        log "         (http://${NS%:*}/a):  no ip name-server $NS"
        log "                             system configuration save"
    fi
else
    log "no listen address in $CONF — if a name-server is still registered: no ip name-server <address>"
fi

[ -x /opt/etc/init.d/S56doqd ] && /opt/etc/init.d/S56doqd stop || true
rm -f /opt/etc/init.d/S56doqd /opt/sbin/doqd
rm -f /opt/var/lib/doqd/geo.state /tmp/doqd.state.json
rmdir /opt/var/lib/doqd 2>/dev/null || true

if [ -n "$removal_unconfirmed" ]; then
    # The config is the only record of the address a retry has to remove.
    log "config $CONF kept for now: after removing the name-server, run this script again"
else
    custom_hosts=$(awk '$1 == "hosts" && $2 != "builtin" {print $2}' "$CONF" 2>/dev/null) || true
    rm -f "$CONF" "$CONF.bak" "$CONF".pre-smartdns.* "$CONF".smartdns.* /opt/etc/doqd.hosts
    log "config removed"
    case $custom_hosts in
        ""|/opt/etc/doqd.hosts) ;;
        *) log "your hosts file $custom_hosts was left in place" ;;
    esac
fi
log "done"
