#!/bin/sh
# Собирает doqd под все архитектуры роутеров: Keenetic (arm64, mipsle, mips)
# и OpenWRT (они же плюс остальные цели, которые умеет Go).
#   sh scripts/build-all.sh <каталог> [версия]
# Имя файла — doqd-linux-<суффикс>; суффиксы выбирают install.sh и
# install-openwrt.sh, менять их нельзя.
set -eu
OUT=${1:?usage: build-all.sh <dir> [version]}
VERSION=${2:-dev}
mkdir -p "$OUT"
build() { # $1 = суффикс, остальное — переменные окружения Go
    suffix=$1
    shift
    env CGO_ENABLED=0 GOOS=linux "$@" go build -trimpath \
        -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/doqd-linux-$suffix" ./cmd/doqd
    echo "built doqd-linux-$suffix"
}
build arm64    GOARCH=arm64
build mipsle   GOARCH=mipsle GOMIPS=softfloat
build mips     GOARCH=mips GOMIPS=softfloat
# ARMv7 с VFP/NEON; ядра без FPU и ARMv5/v6 получают armv5 (softfloat).
build armv7    GOARCH=arm GOARM=7
build armv5    GOARCH=arm GOARM=5
build amd64    GOARCH=amd64
# softfloat: без SSE2, пойдёт и на i386_pentium-mmx.
build 386      GOARCH=386 GO386=softfloat
build mips64   GOARCH=mips64 GOMIPS64=softfloat
build mips64le GOARCH=mips64le GOMIPS64=softfloat
build riscv64  GOARCH=riscv64
build loong64  GOARCH=loong64
