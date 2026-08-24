#!/bin/sh
# Полное удаление doqd с роутера. Запускать НА РОУТЕРЕ.
set -e
NS="127.0.0.1:5354"
log() { echo "[keenetic-doq] $*"; }

ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then ndmq -p "$1"
    else echo "[keenetic-doq] выполните в CLI роутера вручную: $1"; fi
}

log "снимаю регистрацию name-server"
ndm_cmd "no ip name-server $NS"
ndm_cmd "system configuration save"

[ -x /opt/etc/init.d/S56doqd ] && /opt/etc/init.d/S56doqd stop || true
rm -f /opt/etc/init.d/S56doqd /opt/sbin/doqd
log "конфиг /opt/etc/doqd.conf оставлен (удалите вручную при желании)"
log "готово"
