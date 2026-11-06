#!/bin/sh
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BASE=$(mktemp -d)
trap 'rm -rf "$BASE"' EXIT

NS=192.168.1.1:5354

# ndmc behaves the way it does in a shell started with `exec sh` from the
# router CLI. Every scenario runs with it, because that is the bug under test.
setup() {
    WORK="$BASE/$1"
    export WORK # read by the fake curl
    mkdir -p "$WORK/bin" "$WORK/opt/etc/init.d" "$WORK/opt/sbin" "$WORK/opt/var/lib/doqd"
    printf 'listen %s\n' "$NS" > "$WORK/opt/etc/doqd.conf"
    printf 'binary\n' > "$WORK/opt/sbin/doqd"
    printf 'state\n' > "$WORK/opt/var/lib/doqd/geo.state"
    cat > "$WORK/opt/etc/init.d/S56doqd" <<'EOF'
#!/bin/sh
[ "$1" = stop ] || exit 2
touch "$STOP_MARKER"
EOF
    chmod +x "$WORK/opt/etc/init.d/S56doqd"
    cat > "$WORK/bin/ndmc" <<'EOF'
#!/bin/sh
echo 'ndmc: system failed [0xcffd0062].' >&2
echo 'Cli::Main: failed to initialize.' >&2
exit 1
EOF
    chmod +x "$WORK/bin/ndmc"
    # Responses below are the ones captured from KeeneticOS 5.01.C.8.0-2.
    # The file "registered" holds the router's state; RCI_* select failures.
    cat > "$WORK/bin/curl" <<'EOF'
#!/bin/sh
body=
while [ $# -gt 0 ]; do
    case "$1" in -d) body=$2; shift ;; esac
    shift
done
echo "$body" >> "$WORK/curl.log"
[ -z "${RCI_DOWN:-}" ] || exit 7
status() {
    printf '[\n  {\n    "parse": {\n      "prompt": "(config)",\n      "status": [\n        {\n          "status": "%s",\n          "message": "%s"\n        }\n      ]\n    }\n  }\n]\n' "$1" "$2"
}
case "$body" in
    *'"no ip name-server '*)
        if [ -n "${RCI_NO_FAILS:-}" ]; then
            status error "not allowed"
        elif [ -f "$WORK/registered" ]; then
            rm -f "$WORK/registered"
            status message "deleted."
        else
            status error "no such server: 192.168.1.1:5354."
        fi
        ;;
    *'"show ip name-server"'*)
        if [ -f "$WORK/registered" ]; then
            printf '[\n  {\n    "parse": {\n      "server": [\n        {\n          "address": "192.168.1.1",\n          "port": 5354\n        }\n      ],\n      "prompt": "(config)"\n    }\n  }\n]\n'
        else
            printf '[\n  {\n    "parse": {\n      "server": [\n        {\n          "address": "88.87.64.6"\n        }\n      ],\n      "prompt": "(config)"\n    }\n  }\n]\n'
        fi
        ;;
    *'"system configuration save"'*)
        if [ -n "${RCI_SAVE_FAILS:-}" ]; then status error "save failed"; else status message "saved."; fi
        ;;
    *) status error "unexpected command" ;;
esac
EOF
    chmod +x "$WORK/bin/curl"
    sed -e "s#/opt#$WORK/opt#g" -e "s#/etc/openwrt_release#$WORK/etc/openwrt_release#g" \
        "$ROOT/uninstall.sh" > "$WORK/uninstall.sh"
}

run() {
    if ! STOP_MARKER="$WORK/stopped" PATH="$WORK/bin:$PATH" \
        sh "$WORK/uninstall.sh" > "$WORK/output" 2>&1; then
        fail "uninstall aborted"
    fi
    # Whatever happens to the name-server, the daemon must be gone.
    [ -f "$WORK/stopped" ] || fail "daemon not stopped"
    [ ! -e "$WORK/opt/etc/init.d/S56doqd" ] || fail "init script left behind"
    [ ! -e "$WORK/opt/sbin/doqd" ] || fail "binary left behind"
    [ ! -e "$WORK/opt/var/lib/doqd/geo.state" ] || fail "geo state left behind"
    [ -f "$WORK/opt/etc/doqd.conf" ] || fail "config was removed"
    grep -q 'done' "$WORK/output" || fail "did not finish"
}

fail() {
    cat "$WORK/output" 2>/dev/null || true
    echo "FAIL [$CASE]: $1" >&2
    exit 1
}

expect_clean() {
    if grep -q 'WARNING' "$WORK/output"; then fail "unexpected manual cleanup warning"; fi
    if grep -q 'system failed' "$WORK/output"; then fail "ndmc was called although RCI works"; fi
}

expect_manual_instructions() {
    grep -q 'WARNING' "$WORK/output" || fail "no warning"
    grep -q "no ip name-server $NS" "$WORK/output" || fail "no removal command shown"
    grep -q 'system configuration save' "$WORK/output" || fail "no save command shown"
}

CASE=rci-removes-entry
setup "$CASE"
touch "$WORK/registered"
run
expect_clean
[ ! -f "$WORK/registered" ] || fail "name-server still registered"
grep -q '"system configuration save"' "$WORK/curl.log" || fail "configuration not saved"
echo "ok: $CASE"

# A rerun after an earlier uninstall: the entry is already gone.
CASE=rci-entry-already-absent
setup "$CASE"
run
expect_clean
echo "ok: $CASE"

CASE=rci-removal-refused
setup "$CASE"
touch "$WORK/registered"
RCI_NO_FAILS=1 run
expect_manual_instructions
echo "ok: $CASE"

CASE=rci-save-fails
setup "$CASE"
touch "$WORK/registered"
RCI_SAVE_FAILS=1 run
expect_manual_instructions
echo "ok: $CASE"

CASE=rci-unreachable-falls-back-to-ndmc
setup "$CASE"
touch "$WORK/registered"
RCI_DOWN=1 run
grep -q 'system failed' "$WORK/output" || fail "ndmc fallback not attempted"
expect_manual_instructions
echo "ok: $CASE"

echo 'ok: uninstall removes the name-server over RCI and falls back to manual instructions'
