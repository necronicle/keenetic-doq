#!/bin/sh
# keenetic-doq uninstaller. Run ON THE ROUTER.
# Unregisters the name-server, stops and removes the daemon.
# The config /opt/etc/doqd.conf is kept (delete it manually if unwanted).
set -e

log() { echo "[keenetic-doq] $*"; }

ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then ndmq -p "$1"
    else echo "[keenetic-doq] run manually in the router CLI: $1"; fi
}

NS=$(awk '/^listen /{print $2}' /opt/etc/doqd.conf 2>/dev/null) || true

if [ -n "$NS" ]; then
    log "unregistering name-server $NS"
    ndm_cmd "no ip name-server $NS"
    ndm_cmd "system configuration save"
else
    log "config not found — unregister manually: no ip name-server <address>"
fi

[ -x /opt/etc/init.d/S56doqd ] && /opt/etc/init.d/S56doqd stop || true
rm -f /opt/etc/init.d/S56doqd /opt/sbin/doqd
log "config /opt/etc/doqd.conf kept (delete manually if unwanted)"
log "done"
