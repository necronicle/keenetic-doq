#!/bin/sh
# Standalone installer fixtures: fresh defaults, safe upgrades and idempotence.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/doqd-new" <<'MOCK'
#!/bin/sh
[ "$#" = 4 ] && [ "$1" = hosts ] && [ "$2" = check ] && [ "$3" = -c ] || exit 2
[ -f "$4" ] || exit 0
if awk '$1 == "geo" || $1 == "geo-domain" || $2 == "/missing-hosts" {bad=1} END {exit !bad}' "$4"; then
    echo 'error: invalid config or missing hosts file' >&2
    exit 1
fi
MOCK
chmod +x "$WORK/doqd-new"
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
    # A fresh install must reject pre-SmartDNS binaries before writing config.
    rm -f "$CONF"
    printf '#!/bin/sh\necho unknown command >&2\nexit 2\n' > "$WORK/doqd-old"
    chmod +x "$WORK/doqd-old"
    if migrate_smartdns "$WORK/doqd-old" > "$WORK/error" 2>&1; then
        echo 'FAIL: fresh install accepted a pre-SmartDNS binary'; exit 1
    fi
    [ ! -e "$CONF" ]
    migrate_smartdns "$WORK/doqd-new"
    [ ! -e "$CONF" ]
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
    migrate_smartdns "$WORK/doqd-new"
    if grep -q '^[[:space:]]*geo ' "$CONF"; then exit 1; fi
    grep -qx 'hosts /etc/custom-hosts' "$CONF"
    grep -qx 'upstream quic://custom.example' "$CONF"
    grep -qx 'fallback quic://backup.example' "$CONF"
    grep -qx 'cache_size 123' "$CONF"
    backup=$(find "$WORK" -name 'conf.pre-smartdns.*' | head -1)
    cmp "$backup" "$WORK/original"
    cp "$CONF" "$WORK/once"
    migrate_smartdns "$WORK/doqd-new"
    cmp "$CONF" "$WORK/once"
    rm -f "$WORK"/conf.pre-smartdns.*

    printf 'geo quic://custom.example # mine\nfallback quic://backup.example\n' > "$CONF"
    migrate_smartdns "$WORK/doqd-new"
    grep -qx 'upstream quic://custom.example # mine' "$CONF"
    grep -qx 'hosts builtin' "$CONF"
    printf 'geo quic://dns.comss.one\ngeo quic://dns.dns-ai.ru\ngeo quic://eu.geohide.ru\nfallback quic://backup.example\n' > "$CONF"
    migrate_smartdns "$WORK/doqd-new"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    grep -qx 'fallback quic://backup.example' "$CONF"
    # Retired defaults must be removed even without legacy geo rows.
    printf 'hosts /etc/custom-hosts\nupstream quic://dns.quad9.net # retired\nfallback quic://dns.nextdns.io\ncache_size 123\n' > "$CONF"
    migrate_smartdns "$WORK/doqd-new"
    grep -qx 'hosts /etc/custom-hosts' "$CONF"
    grep -qx 'cache_size 123' "$CONF"
    grep -qx 'upstream quic://dns.hostux.net' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    if grep -q 'dns.quad9.net\|dns.nextdns.io\|p0.freedns.controld.com' "$CONF"; then exit 1; fi
    cp "$CONF" "$WORK/once"
    migrate_smartdns "$WORK/doqd-new"
    cmp "$CONF" "$WORK/once"
    # An existing default-only config receives new defaults exactly once.
    printf 'hosts builtin\nupstream quic://p0.freedns.controld.com\n' > "$CONF"
    migrate_smartdns "$WORK/doqd-new"
    grep -qx 'upstream quic://dns.hostux.net' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    cp "$CONF" "$WORK/once"
    migrate_smartdns "$WORK/doqd-new"
    cmp "$CONF" "$WORK/once"
    # Removing retired Control D leaves exactly one primary, without duplicates.
    for roles in control-primary hostux-primary; do
        if [ "$roles" = control-primary ]; then
            printf 'upstream quic://p0.freedns.controld.com\nfallback quic://dns.hostux.net\n' > "$CONF"
        else
            printf 'upstream quic://dns.hostux.net\nfallback quic://p0.freedns.controld.com\n' > "$CONF"
        fi
        migrate_smartdns "$WORK/doqd-new"
        [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
        if grep -q '^fallback ' "$CONF"; then exit 1; fi
        cp "$CONF" "$WORK/once"
        migrate_smartdns "$WORK/doqd-new"
        cmp "$CONF" "$WORK/once"
    done
    # Preserve a reserve role when another custom primary still exists.
    printf 'upstream quic://custom.example\nfallback quic://dns.hostux.net\n' > "$CONF"
    migrate_smartdns "$WORK/doqd-new"
    grep -qx 'upstream quic://custom.example' "$CONF"
    grep -qx 'fallback quic://dns.hostux.net' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    # A subset of former geo defaults is a custom choice, so preserve it.
    printf 'geo quic://dns.comss.one\n' > "$CONF"
    migrate_smartdns "$WORK/doqd-new"
    grep -qx 'upstream quic://dns.comss.one' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    # Custom ordinary servers are preserved; retired defaults are removed.
    for fixture in "$ROOT"/testdata/migrate/*.in; do
        cp "$fixture" "$CONF"
        if awk '$1 == "geo-domain" {found=1} END {exit !found}' "$CONF"; then continue; fi
        awk '$1 == "upstream" || $1 == "fallback" {print}' "$CONF" > "$WORK/servers"
        migrate_smartdns "$WORK/doqd-new"
        while IFS= read -r line; do
            case "$line" in
                'upstream quic://dns.quad9.net'|'upstream quic://dns.nextdns.io'|'fallback quic://dns.quad9.net'|'fallback quic://dns.nextdns.io'|'upstream quic://p0.freedns.controld.com'|'fallback quic://p0.freedns.controld.com') continue ;;
            esac
            grep -qxF "$line" "$CONF"
        done < "$WORK/servers"
        if grep -Eq '^(upstream|fallback) quic://dns\.(quad9\.net|nextdns\.io)([[:space:]]|$)' "$CONF"; then exit 1; fi
        if awk '$1 == "geo" {found=1} END {exit !found}' "$CONF"; then exit 1; fi
    done

    printf '  geo-domain private.example\nhosts /etc/custom-hosts\n' > "$CONF"
    cp "$CONF" "$WORK/original"
    rm -f "$WORK"/conf.pre-smartdns.*
    if (migrate_smartdns "$WORK/doqd-new") > "$WORK/error" 2>&1; then echo 'FAIL: accepted geo-domain'; exit 1; fi
    cmp "$CONF" "$WORK/original"
    grep -q 'IP' "$WORK/error"
    [ -z "$(find "$WORK" -name 'conf.pre-smartdns.*')" ]
    # Both migrated and already-current configs must be validated before commit.
    for legacy in 'geo quic://custom.example' 'upstream quic://custom.example'; do
        printf '%s\nhosts /missing-hosts\n' "$legacy" > "$CONF"
        cp "$CONF" "$WORK/original"
        if (migrate_smartdns "$WORK/doqd-new") > "$WORK/error" 2>&1; then
            echo "FAIL: $installer accepted invalid custom hosts"; exit 1
        fi
        cmp "$CONF" "$WORK/original"
        [ -z "$(find "$WORK" -name 'conf.pre-smartdns.*' -o -name 'conf.smartdns.*')" ]
        grep -q 'missing hosts' "$WORK/error"
    done
    # Migration must run before either copying a binary or stopping a service.
    awk '/^check_legacy_config \|\|/ {safe=1} /cp "\$2"/ {if (!safe) exit 1}' "$ROOT/$installer"
    # Exercise the actual fresh config heredoc with shell interpolation.
    # shellcheck disable=SC2016 # Match the literal installer heredoc.
    sed -n '/    cat > "\$CONF" <<EOF/,/^EOF/p' "$ROOT/$installer" > "$WORK/fresh"
    LAN_IP=192.168.1.1 PORT=5354 sh "$WORK/fresh"
    grep -qx 'hosts builtin' "$CONF"
    grep -qx 'upstream quic://dns.hostux.net' "$CONF"
    [ "$(grep -c '^upstream ' "$CONF")" = 1 ]
    if grep -q '^geo' "$CONF"; then exit 1; fi
    echo "ok: $installer fresh defaults, migration, backup, rejection and idempotence"
done
