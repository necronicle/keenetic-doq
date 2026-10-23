#!/bin/sh
# Standalone installer fixtures: fresh defaults, safe upgrades and idempotence.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
for installer in install.sh install-openwrt.sh; do
    rm -f "$WORK"/conf.pre-smartdns.*
    sed -n '/^check_legacy_config() {/,/^}/p;/^migrate_smartdns() {/,/^}/p' "$ROOT/$installer" > "$WORK/fns"
    [ -s "$WORK/fns" ] || { echo "FAIL: $installer lacks SmartDNS migration"; exit 1; }
    if [ "$installer" = install.sh ]; then cp "$WORK/fns" "$WORK/keenetic-fns"; else cmp "$WORK/fns" "$WORK/keenetic-fns"; fi
    # shellcheck disable=SC1091
    . "$WORK/fns"
    log() { :; }
    die() { echo "$*" >&2; exit 1; }
    CONF=$WORK/conf
    export CONF
    cat > "$CONF" <<'CONF'
listen 127.0.0.1:5354
 geo quic://dns.dns-ai.ru
geo quic://eu.geohide.ru # default
geo quic://dns.comss.one
upstream quic://custom.example
fallback quic://backup.example
cache_size 123
hosts /etc/custom-hosts
CONF
    cp "$CONF" "$WORK/original"
    migrate_smartdns
    if grep -q '^[[:space:]]*geo ' "$CONF"; then exit 1; fi
    grep -qx 'hosts /etc/custom-hosts' "$CONF"
    grep -qx 'upstream quic://custom.example' "$CONF"
    grep -qx 'fallback quic://backup.example' "$CONF"
    grep -qx 'cache_size 123' "$CONF"
    backup=$(find "$WORK" -name 'conf.pre-smartdns.*' | head -1)
    cmp "$backup" "$WORK/original"
    cp "$CONF" "$WORK/once"
    migrate_smartdns
    cmp "$CONF" "$WORK/once"
    rm -f "$WORK"/conf.pre-smartdns.*

    printf 'geo quic://custom.example # mine\nfallback quic://backup.example\n' > "$CONF"
    migrate_smartdns
    grep -qx 'upstream quic://custom.example # mine' "$CONF"
    grep -qx 'hosts builtin' "$CONF"
    printf 'geo quic://dns.comss.one\ngeo quic://dns.dns-ai.ru\ngeo quic://eu.geohide.ru\nfallback quic://backup.example\n' > "$CONF"
    migrate_smartdns
    [ "$(grep -c '^upstream ' "$CONF")" = 3 ]
    grep -qx 'fallback quic://backup.example' "$CONF"
    # A subset of former defaults is a custom choice, so preserve it.
    printf 'geo quic://dns.comss.one\n' > "$CONF"
    migrate_smartdns
    grep -qx 'upstream quic://dns.comss.one' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    # Historical ordinary upstreams are preserved, not reclassified/dropped.
    for fixture in "$ROOT"/testdata/migrate/*.in; do
        cp "$fixture" "$CONF"
        if awk '$1 == "geo-domain" {found=1} END {exit !found}' "$CONF"; then continue; fi
        awk '$1 == "upstream" || $1 == "fallback" {print}' "$CONF" > "$WORK/servers"
        migrate_smartdns
        while IFS= read -r line; do grep -qxF "$line" "$CONF"; done < "$WORK/servers"
        if awk '$1 == "geo" {found=1} END {exit !found}' "$CONF"; then exit 1; fi
    done

    printf '  geo-domain private.example\nhosts /etc/custom-hosts\n' > "$CONF"
    cp "$CONF" "$WORK/original"
    rm -f "$WORK"/conf.pre-smartdns.*
    if (migrate_smartdns) > "$WORK/error" 2>&1; then echo 'FAIL: accepted geo-domain'; exit 1; fi
    cmp "$CONF" "$WORK/original"
    grep -q 'IP' "$WORK/error"
    [ -z "$(find "$WORK" -name 'conf.pre-smartdns.*')" ]
    # Migration must run before either copying a binary or stopping a service.
    awk '/^check_legacy_config \|\|/ {safe=1} /cp "\$2"/ {if (!safe) exit 1}' "$ROOT/$installer"
    # Exercise the actual fresh config heredoc with shell interpolation.
    # shellcheck disable=SC2016 # Match the literal installer heredoc.
    sed -n '/    cat > "\$CONF" <<EOF/,/^EOF/p' "$ROOT/$installer" > "$WORK/fresh"
    LAN_IP=192.168.1.1 PORT=5354 sh "$WORK/fresh"
    grep -qx 'hosts builtin' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 3 ]
    if grep -q '^geo' "$CONF"; then exit 1; fi
    echo "ok: $installer fresh defaults, migration, backup, rejection and idempotence"
done
