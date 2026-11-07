#!/bin/sh
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BASE=$(mktemp -d)
trap 'rm -rf "$BASE"' EXIT

NS=192.168.1.1:5354

setup() {
    CASE=$1
    WORK="$BASE/$1"
    export WORK # read by the fake curl and ndmc
    ETC="$WORK/opt/etc"
    mkdir -p "$WORK/bin" "$ETC/init.d" "$ETC/doq-web" "$WORK/opt/sbin" "$WORK/opt/var/lib/doqd"
    printf 'listen %s\nhosts /opt/etc/doqd.hosts\n' "$NS" > "$ETC/doqd.conf"
    # Everything derived from the config, whoever wrote it.
    touch "$ETC/doqd.conf.bak" "$ETC/doqd.hosts" "$ETC/doqd.conf.pre-smartdns.AbCdEf" "$ETC/doqd.conf.smartdns.AbCdEf"
    # Not ours.
    touch "$ETC/other.conf" "$ETC/doq-web/version.txt"
    printf 'binary\n' > "$WORK/opt/sbin/doqd"
    printf 'state\n' > "$WORK/opt/var/lib/doqd/geo.state"
    cat > "$ETC/init.d/S56doqd" <<'EOF'
#!/bin/sh
[ "$1" = stop ] || exit 2
touch "$STOP_MARKER"
EOF
    chmod +x "$ETC/init.d/S56doqd"
    # NDMC_MODE: fail is ndmc in a shell started with `exec sh` from the router
    # CLI, fail0 is the same with exit status 0, ok works. Exit codes are the
    # ones captured from KeeneticOS 5.01.C.8.0-2.
    cat > "$WORK/bin/ndmc" <<'EOF'
#!/bin/sh
echo "$2" >> "$WORK/ndmc.log"
case "${NDMC_MODE:-fail}" in
    fail|fail0)
        echo 'ndmc: system failed [0xcffd0062].' >&2
        echo 'Cli::Main: failed to initialize.' >&2
        [ "${NDMC_MODE:-fail}" = fail0 ] && exit 0
        exit 1
        ;;
esac
case "$2" in
    'no ip name-server '*)
        if [ -f "$WORK/registered" ]; then
            rm -f "$WORK/registered"
        else
            echo 'Dns::Manager error[22544585]: no such server.' >&2
            exit 122
        fi
        ;;
    'show ip name-server')
        if [ -f "$WORK/registered" ]; then echo 'address: 192.168.1.1'; else echo 'address: 88.87.64.6'; fi
        ;;
esac
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
    # The config as install.sh and the sed above would see it.
    sed -i.orig "s#/opt#$WORK/opt#g" "$ETC/doqd.conf"
    rm -f "$ETC/doqd.conf.orig"
}

# Arguments are VAR=value settings for this run only. `VAR=value run` would leak
# into later scenarios: POSIX lets such assignments outlive a function call.
run() {
    if ! env "$@" STOP_MARKER="$WORK/stopped" PATH="$WORK/bin:$PATH" \
        sh "$WORK/uninstall.sh" > "$WORK/output" 2>&1; then
        fail "uninstall aborted"
    fi
    # Whatever happens to the name-server, the daemon must be gone.
    [ -f "$WORK/stopped" ] || fail "daemon not stopped"
    [ ! -e "$ETC/init.d/S56doqd" ] || fail "init script left behind"
    [ ! -e "$WORK/opt/sbin/doqd" ] || fail "binary left behind"
    [ ! -e "$WORK/opt/var/lib/doqd" ] || fail "state left behind"
    grep -q 'done' "$WORK/output" || fail "did not finish"
    # Foreign files survive in every outcome.
    [ -f "$ETC/other.conf" ] || fail "removed an unrelated file"
    [ -f "$ETC/doq-web/version.txt" ] || fail "removed the web panel's files"
}

fail() {
    cat "$WORK/output" 2>/dev/null || true
    echo "FAIL [$CASE]: $1" >&2
    exit 1
}

expect_clean() {
    if grep -q 'WARNING' "$WORK/output"; then fail "unexpected manual cleanup warning"; fi
    if grep -q 'kept' "$WORK/output"; then fail "reports something as kept"; fi
    for f in doqd.conf doqd.conf.bak doqd.hosts doqd.conf.pre-smartdns.AbCdEf doqd.conf.smartdns.AbCdEf; do
        [ ! -e "$ETC/$f" ] || fail "$f left behind"
    done
}

# The config holds the only record of the address, so it has to outlive a
# removal nobody could confirm: otherwise a rerun has nothing to work with.
expect_manual_and_kept() {
    grep -q 'WARNING' "$WORK/output" || fail "no warning"
    grep -q "no ip name-server $NS" "$WORK/output" || fail "no removal command shown"
    grep -q 'system configuration save' "$WORK/output" || fail "no save command shown"
    [ -f "$ETC/doqd.conf" ] || fail "config removed before the name-server"
    grep -q 'run this script again' "$WORK/output" || fail "no retry hint"
}

setup rci-removes-entry
touch "$WORK/registered"
run
expect_clean
[ ! -f "$WORK/registered" ] || fail "name-server still registered"
grep -q '"system configuration save"' "$WORK/curl.log" || fail "configuration not saved"
if grep -q 'system failed' "$WORK/output"; then fail "ndmc was called although RCI works"; fi
echo "ok: $CASE"

# Nothing to remove is still a confirmed result.
setup rci-entry-already-absent
run
expect_clean
echo "ok: $CASE"

setup rci-removal-refused
touch "$WORK/registered"
run RCI_NO_FAILS=1
expect_manual_and_kept
echo "ok: $CASE"

setup rci-save-fails
touch "$WORK/registered"
run RCI_SAVE_FAILS=1
expect_manual_and_kept
echo "ok: $CASE"

setup rci-down-ndmc-fails
touch "$WORK/registered"
run RCI_DOWN=1
[ -f "$WORK/ndmc.log" ] || fail "ndmc fallback not attempted"
expect_manual_and_kept
echo "ok: $CASE"

# Must not read as success just because nothing was printed to stdout.
setup rci-down-ndmc-fails-with-status-0
touch "$WORK/registered"
run RCI_DOWN=1 NDMC_MODE=fail0
expect_manual_and_kept
echo "ok: $CASE"

setup rci-down-ndmc-works
touch "$WORK/registered"
run RCI_DOWN=1 NDMC_MODE=ok
expect_clean
[ ! -f "$WORK/registered" ] || fail "name-server still registered"
grep -q 'system configuration save' "$WORK/ndmc.log" || fail "configuration not saved"
echo "ok: $CASE"

# ndmc exits 122 for an absent entry; the read-back decides, not the status.
setup rci-down-ndmc-entry-already-absent
run RCI_DOWN=1 NDMC_MODE=ok
expect_clean
echo "ok: $CASE"

setup no-way-to-reach-the-router-cli
touch "$WORK/registered"
rm -f "$WORK/bin/ndmc"
run RCI_DOWN=1
expect_manual_and_kept
echo "ok: $CASE"

setup rerun-after-a-failed-removal
touch "$WORK/registered"
run RCI_DOWN=1
expect_manual_and_kept
run
expect_clean
[ ! -f "$WORK/registered" ] || fail "name-server still registered"
echo "ok: $CASE"

# A hosts file the user keeps elsewhere is their data: keep it and say so.
setup custom-hosts-outside-our-paths
mkdir -p "$WORK/mine"
touch "$WORK/mine/hosts" "$WORK/registered"
printf 'listen %s\nhosts %s\n' "$NS" "$WORK/mine/hosts" > "$ETC/doqd.conf"
run
[ -f "$WORK/mine/hosts" ] || fail "removed a hosts file outside our paths"
grep -q "$WORK/mine/hosts" "$WORK/output" || fail "did not mention the hosts file left in place"
[ ! -e "$ETC/doqd.conf" ] || fail "config left behind"
echo "ok: $CASE"

echo 'ok: uninstall removes the config once the name-server is confirmed gone'
