#!/bin/sh
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BASE=$(mktemp -d)
trap 'rm -rf "$BASE"' EXIT

setup() {
    CASE=$1
    WORK="$BASE/$1"
    ETC="$WORK/etc"
    mkdir -p "$WORK/bin" "$ETC/init.d" "$ETC/doqd" "$WORK/usr/sbin" "$WORK/lib/upgrade/keep.d"
    printf 'listen 127.0.0.1:5354\nhosts builtin\n' > "$ETC/doqd.conf"
    touch "$ETC/doqd.conf.bak" "$ETC/doqd.conf.pre-smartdns.AbCdEf" "$ETC/doqd.conf.smartdns.AbCdEf"
    touch "$ETC/doqd/geo.state" "$ETC/doqd/hosts" "$ETC/openwrt_release" "$ETC/other.conf"
    touch "$WORK/usr/sbin/doqd" "$WORK/lib/upgrade/keep.d/doqd"
    printf '#!/bin/sh\nexit 0\n' > "$ETC/init.d/doqd"
    printf '#!/bin/sh\nexit 0\n' > "$ETC/init.d/dnsmasq"
    printf '#!/bin/sh\nexit 0\n' > "$WORK/bin/uci"
    chmod +x "$ETC/init.d/doqd" "$ETC/init.d/dnsmasq" "$WORK/bin/uci"
    sed -e "s#/etc/#$ETC/#g" -e "s#/usr/sbin/#$WORK/usr/sbin/#g" -e "s#/lib/upgrade/#$WORK/lib/upgrade/#g" \
        -e "s#/tmp/doqd.state.json#$WORK/doqd.state.json#g" \
        "$ROOT/uninstall-openwrt.sh" > "$WORK/uninstall.sh"
}

run() {
    if ! PATH="$WORK/bin:$PATH" sh "$WORK/uninstall.sh" > "$WORK/output" 2>&1; then
        fail "uninstall aborted"
    fi
    grep -q 'done' "$WORK/output" || fail "did not finish"
    for f in etc/init.d/doqd usr/sbin/doqd lib/upgrade/keep.d/doqd etc/doqd; do
        [ ! -e "$WORK/$f" ] || fail "$f left behind"
    done
    [ -f "$ETC/other.conf" ] || fail "removed an unrelated file"
    [ -x "$ETC/init.d/dnsmasq" ] || fail "removed dnsmasq"
}

fail() {
    cat "$WORK/output" 2>/dev/null || true
    echo "FAIL [$CASE]: $1" >&2
    exit 1
}

expect_config_gone() {
    for f in doqd.conf doqd.conf.bak doqd.conf.pre-smartdns.AbCdEf doqd.conf.smartdns.AbCdEf; do
        [ ! -e "$ETC/$f" ] || fail "$f left behind"
    done
}

setup with-installer-record
printf 'target=127.0.0.1#5354\nnoresolv=0\n' > "$ETC/doqd/dnsmasq.saved"
run
expect_config_gone
if grep -q 'kept' "$WORK/output"; then fail "reports something as kept"; fi
echo "ok: $CASE"

setup without-installer-record
run
expect_config_gone
if grep -q 'kept' "$WORK/output"; then fail "reports something as kept"; fi
echo "ok: $CASE"

setup custom-hosts-inside-state-dir
printf 'listen 127.0.0.1:5354\nhosts /etc/doqd/hosts\n' | sed "s#/etc/#$ETC/#" > "$ETC/doqd.conf"
run
expect_config_gone
if grep -q 'kept' "$WORK/output"; then fail "reports the removed hosts file as kept"; fi
echo "ok: $CASE"

setup custom-hosts-outside-our-paths
mkdir -p "$WORK/mine"
touch "$WORK/mine/hosts"
printf 'listen 127.0.0.1:5354\nhosts %s\n' "$WORK/mine/hosts" > "$ETC/doqd.conf"
run
expect_config_gone
[ -f "$WORK/mine/hosts" ] || fail "removed a hosts file outside our paths"
grep -q "$WORK/mine/hosts" "$WORK/output" || fail "did not mention the hosts file left in place"
echo "ok: $CASE"

echo 'ok: OpenWRT uninstall removes the config'
