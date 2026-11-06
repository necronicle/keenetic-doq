#!/bin/sh
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/bin" "$WORK/opt/etc/init.d" "$WORK/opt/sbin" "$WORK/opt/var/lib/doqd"
printf 'listen 192.168.1.1:5354\n' > "$WORK/opt/etc/doqd.conf"
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

sed -e "s#/opt#$WORK/opt#g" -e "s#/etc/openwrt_release#$WORK/etc/openwrt_release#g" \
    "$ROOT/uninstall.sh" > "$WORK/uninstall.sh"
if ! STOP_MARKER="$WORK/stopped" PATH="$WORK/bin:$PATH" sh "$WORK/uninstall.sh" > "$WORK/output" 2>&1; then
    cat "$WORK/output"
    echo 'FAIL: ndmc failure aborted uninstallation' >&2
    exit 1
fi
[ -f "$WORK/stopped" ]
[ ! -e "$WORK/opt/etc/init.d/S56doqd" ]
[ ! -e "$WORK/opt/sbin/doqd" ]
[ ! -e "$WORK/opt/var/lib/doqd/geo.state" ]
[ -f "$WORK/opt/etc/doqd.conf" ]
grep -q 'no ip name-server 192.168.1.1:5354' "$WORK/output"
grep -q 'system configuration save' "$WORK/output"
grep -q 'done' "$WORK/output"
echo 'ok: uninstall continues with manual DNS cleanup instructions when ndmc fails'
