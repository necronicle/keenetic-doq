#!/bin/sh
# Прогоняет миграции конфига из install.sh на образцах конфигов прошлых версий
# (testdata/migrate/*.in) и сверяет с ожидаемым (*.want).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
sed -n '/^migrate_conf() {/,/^}/p;/^drop_comss() {/,/^}/p;/^migrate_geo() {/,/^}/p' \
    "$ROOT/install.sh" > "$WORK/fns.sh"
fail=0
for src in "$ROOT"/testdata/migrate/*.in; do
    want=${src%.in}.want
    cp "$src" "$WORK/conf"
    (
        . "$WORK/fns.sh"
        log() { :; }
        CONF="$WORK/conf"
        migrate_conf
        drop_comss
        migrate_geo
    )
    if diff -u "$want" "$WORK/conf"; then
        echo "ok:   $(basename "$src")"
    else
        echo "FAIL: $(basename "$src")"
        fail=1
    fi
done
exit $fail
