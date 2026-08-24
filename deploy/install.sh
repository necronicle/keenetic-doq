#!/bin/sh
# Установка doqd на Keenetic (Entware). Запускать НА РОУТЕРЕ.
# Использование:
#   ./install.sh --local ./doqd     # бинарник уже скопирован рядом
#   ./install.sh                    # скачать из GitHub Releases (нужен публичный
#                                   # репозиторий или GITHUB_TOKEN в окружении)
set -e

REPO="necronicle/keenetic-doq"
BIN=/opt/sbin/doqd
CONF=/opt/etc/doqd.conf
INIT=/opt/etc/init.d/S56doqd
NS="127.0.0.1:5353"

log() { echo "[keenetic-doq] $*"; }
die() { echo "[keenetic-doq] ОШИБКА: $*" >&2; exit 1; }

# Команда в CLI Keenetic: ndmc (новый) или ndmq (старый)
ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then
        ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then
        ndmq -p "$1"
    else
        die "не найден ndmc/ndmq — выполните в CLI роутера вручную: $1"
    fi
}

[ -f /opt/etc/init.d/rc.func ] || die "Entware не найден (/opt/etc/init.d/rc.func)"

arch=$(opkg print-architecture | awk '!/all|noarch/ {print $2}' | head -1)
case "$arch" in
    aarch64*) goarch=arm64 ;;
    mipsel*)  goarch=mipsle ;;
    mips*)    goarch=mips ;;
    *)        die "неизвестная архитектура Entware: $arch" ;;
esac
log "архитектура: $arch → doqd-linux-$goarch"

if [ "$1" = "--local" ]; then
    [ -f "$2" ] || die "файл $2 не найден"
    cp "$2" "$BIN.new"
else
    url="https://github.com/$REPO/releases/latest/download/doqd-linux-$goarch"
    log "скачиваю $url"
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -o "$BIN.new" "$url" \
            || die "не скачалось (приватный репозиторий? используйте --local)"
    else
        curl -fsSL -o "$BIN.new" "$url" \
            || die "не скачалось (приватный репозиторий? используйте --local)"
    fi
fi
chmod 755 "$BIN.new"
"$BIN.new" -version >/dev/null || die "бинарник не запускается (не та архитектура?)"

[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
mv "$BIN.new" "$BIN"

script_dir=$(dirname "$0")
[ -f "$CONF" ] || cp "$script_dir/doqd.conf" "$CONF"
cp "$script_dir/S56doqd" "$INIT"
chmod 755 "$INIT"

"$INIT" start
sleep 1
"$INIT" check | grep -q alive || die "doqd не запустился, смотрите логи"

log "регистрирую name-server $NS в KeeneticOS"
ndm_cmd "ip name-server $NS"
ndm_cmd "system configuration save"

log "готово. Проверка: ndnproxy теперь видит doqd как апстрим (show ip name-server)."
