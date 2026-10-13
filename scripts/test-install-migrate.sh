#!/bin/sh
# Прогоняет миграции конфига из install.sh на образцах конфигов прошлых версий
# (testdata/migrate/*.in) и сверяет с ожидаемым (*.want).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
sed -n '/^migrate_conf() {/,/^}/p;/^drop_comss() {/,/^}/p;/^migrate_geo() {/,/^}/p;/^split_lanes() {/,/^}/p' \
    "$ROOT/install.sh" > "$WORK/fns.sh"
fail=0
# install-openwrt.sh мигрирует тем же split_lanes — копии не должны разойтись.
for f in install.sh install-openwrt.sh; do
    sed -n '/^split_lanes() {/,/^}/p' "$ROOT/$f" > "$WORK/split.$f"
done
if [ -s "$WORK/split.install.sh" ] && diff -u "$WORK/split.install.sh" "$WORK/split.install-openwrt.sh"; then
    echo "ok:   split_lanes is the same in both installers"
else
    echo "FAIL: split_lanes differs between install.sh and install-openwrt.sh"
    fail=1
fi
for src in "$ROOT"/testdata/migrate/*.in; do
    want=${src%.in}.want
    cp "$src" "$WORK/conf"
    (
        . "$WORK/fns.sh"
        log() { :; }
        # shellcheck disable=SC2034 # читают функции из fns.sh
        CONF="$WORK/conf"
        # twice: a second installer run must change nothing
        for _ in 1 2; do
            migrate_conf
            drop_comss
            migrate_geo
            split_lanes
        done
    )
    if diff -u "$want" "$WORK/conf"; then
        echo "ok:   $(basename "$src")"
    else
        echo "FAIL: $(basename "$src")"
        fail=1
    fi
done
exit $fail
