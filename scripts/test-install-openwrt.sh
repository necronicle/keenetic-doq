#!/bin/sh
# Проверяет чистые функции install-openwrt.sh: выбор бинарника по архитектуре
# пакетов OpenWRT, перевод адресов в синтаксис dnsmasq и домены NTP.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
sed -n '/^binary_for() {/,/^}/p;/^dnsmasq_addr() {/,/^}/p;/^dnsmasq_target() {/,/^}/p;/^bootstrap_servers() {/,/^}/p;/^ntp_domains() {/,/^}/p' \
    "$ROOT/install-openwrt.sh" > "$WORK/fns.sh"
# shellcheck disable=SC1091
. "$WORK/fns.sh"
fail=0
check() { # $1 = что проверяем, $2 = получено, $3 = ожидалось
    if [ "$2" = "$3" ]; then
        echo "ok:   $1 -> $3"
    else
        echo "FAIL: $1 -> '$2', want '$3'"
        fail=1
    fi
}

# Все архитектуры пакетов OpenWRT 24.10 и 25.12 (downloads.openwrt.org).
while read -r arch want; do
    check "$arch" "$(binary_for "$arch")" "$want"
done <<'ARCHES'
aarch64_cortex-a53 arm64
aarch64_cortex-a72 arm64
aarch64_cortex-a76 arm64
aarch64_generic arm64
arm_arm1176jzf-s_vfp armv5
arm_arm926ej-s armv5
arm_cortex-a15_neon-vfpv4 armv7
arm_cortex-a5_vfpv4 armv7
arm_cortex-a7 armv5
arm_cortex-a7_neon-vfpv4 armv7
arm_cortex-a7_vfpv4 armv7
arm_cortex-a8_vfpv3 armv7
arm_cortex-a9 armv5
arm_cortex-a9_neon armv7
arm_cortex-a9_vfpv3-d16 armv7
arm_xscale armv5
i386_pentium-mmx 386
i386_pentium4 386
loongarch64_generic loong64
mips64_mips64r2 mips64
mips64_octeonplus mips64
mips64el_mips64r2 mips64le
mips_24kc mips
mips_4kec mips
mips_mips32 mips
mipsel_24kc mipsle
mipsel_24kc_24kf mipsle
mipsel_74kc mipsle
mipsel_mips32 mipsle
riscv64_generic riscv64
riscv64_riscv64 riscv64
x86_64 amd64
ARCHES
# Go не собирает под ARMv4, big-endian ARM, PowerPC и ARC.
for arch in arm_fa526 armeb_xscale powerpc64_e5500 powerpc_464fp powerpc_8548 arc_archs; do
    check "$arch" "$(binary_for "$arch")" ""
done

check "dnsmasq 127.0.0.1:5354" "$(dnsmasq_addr 127.0.0.1:5354)" "127.0.0.1#5354"
check "dnsmasq [::1]:5354" "$(dnsmasq_addr '[::1]:5354')" "::1#5354"
check "target 127.0.0.1:5354" "$(dnsmasq_target 127.0.0.1:5354)" "127.0.0.1#5354"
check "target 0.0.0.0:5354" "$(dnsmasq_target 0.0.0.0:5354)" "127.0.0.1#5354"
check "target :5354" "$(dnsmasq_target :5354)" "127.0.0.1#5354"
check "target [::]:5354" "$(dnsmasq_target '[::]:5354')" "127.0.0.1#5354"
check "target [::1]:5354" "$(dnsmasq_target '[::1]:5354')" "::1#5354"
check "target 127.0.0.1 (no port)" "$(dnsmasq_target 127.0.0.1)" ""
check "dnsmasq 77.88.8.8:1253" "$(dnsmasq_addr 77.88.8.8:1253)" "77.88.8.8#1253"

CONF=$WORK/doqd.conf
printf 'listen 127.0.0.1:5354\nbootstrap 77.88.8.8\nbootstrap 77.88.8.8:1253\n# bootstrap 9.9.9.9\nbootstrap [2001:db8::1]:53\n' > "$CONF"
check "bootstrap servers" "$(bootstrap_servers | tr '\n' ' ')" "77.88.8.8 77.88.8.8#1253 2001:db8::1#53 "

# uci — заглушка: список NTP-серверов из system.ntp.server.
NTP=""
 # shellcheck disable=SC2329 # Called by the extracted ntp_domains function.
uci() { [ "$*" = "-q get system.ntp.server" ] && [ -n "$NTP" ] && echo "$NTP"; }
NTP="0.openwrt.pool.ntp.org 1.openwrt.pool.ntp.org 2.openwrt.pool.ntp.org 3.openwrt.pool.ntp.org"
check "ntp default" "$(ntp_domains)" "/openwrt.pool.ntp.org"
NTP="time.google.com 10.0.0.1 ntp1.example.net 2001:db8::123"
check "ntp mixed" "$(ntp_domains)" "/ntp1.example.net/time.google.com"
NTP="192.168.1.1"
check "ntp ip only" "$(ntp_domains)" ""
NTP=""
check "ntp none" "$(ntp_domains)" ""

exit $fail
