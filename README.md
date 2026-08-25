# keenetic-doq

[English](README.en.md)

![CI](https://github.com/necronicle/keenetic-doq/actions/workflows/ci.yml/badge.svg)
![Release](https://img.shields.io/github/v/release/necronicle/keenetic-doq)

DNS-over-QUIC (RFC 9250) для роутеров Keenetic — **рядом** со штатными
DoT/DoH, а не вместо них.

KeeneticOS умеет DoT и DoH, но не DoQ, и добавлять его не планирует.
Существующие решения (AdGuard Home, dnsproxy) требуют `opkg dns-override`:
захватывают порт 53, вытесняют штатный `ndnproxy` и ломают соседние
Entware-проекты. `keenetic-doq` работает иначе:

```
клиенты LAN → ndnproxy :53 → [штатные DoT/DoH апстримы]
                           → <LAN-IP>:5354 (doqd) → [кеш] → quic://апстримы
```

Демон `doqd` слушает обычный DNS на LAN-адресе роутера (порт 5354) и
регистрируется штатной командой `ip name-server <LAN-IP>:5354` как ещё один
апстрим системного DNS. Порт 53 никто не трогает, `opkg dns-override` не
нужен. `ndnproxy` сам предпочитает быстрейший апстрим — локальный doqd с
кешем выигрывает естественным образом.

## Возможности

- DoQ-клиент по RFC 9250: живые QUIC-соединения (стрим на запрос,
  переиспользование, keep-alive), ID=0 на проводе.
- Несколько апстримов: EWMA-статистика RTT, запрос идёт в быстрейший живой,
  мгновенный failover, фоновый health-check возвращает ожившие.
- Кеш ответов по TTL с LRU-вытеснением (память роутера бережётся).
- CLI-утилита в том же бинарнике: `doqd add/remove/list/test/status` —
  свои DoQ-серверы без правки файлов, с живой проверкой до применения.
- Статические бинарники без зависимостей.

## Требования

Keenetic с установленным [Entware](https://help.keenetic.com/hc/ru/articles/360000925139).

| Архитектура Entware | Бинарник |
|---|---|
| `aarch64-*` | `doqd-linux-arm64` |
| `mipsel-*` | `doqd-linux-mipsle` |
| `mips-*` (BE) | `doqd-linux-mips` |

## Установка одной командой

На роутере (по SSH):

```sh
curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh | sh
```

Инсталлер: определяет архитектуру, скачивает бинарник из релиза и сверяет
SHA256, ставит `/opt/sbin/doqd`, пишет конфиг `/opt/etc/doqd.conf`
(подставляя LAN-адрес) и автозапуск `/opt/etc/init.d/S56doqd`, запускает
демона и регистрирует name-server. Существующий конфиг при переустановке
не перезаписывается.

Оффлайн-вариант (бинарник уже скопирован на роутер):

```sh
sh install.sh --local ./doqd-linux-arm64
```

## Управление апстримами

Всё управление — тем же бинарником, без правки файлов руками:

```sh
~ # doqd list
UPSTREAMS (/opt/etc/doqd.conf):
 1. quic://dns.comss.one                       alive  rtt 34 ms
 2. quic://dns.quad9.net                       alive  rtt 193 ms

listen: 192.168.1.1:5354   daemon: running (pid 9772)
```

Проверить любой сервер, ничего не меняя:

```sh
~ # doqd test quic://dns.quad9.net
probing quic://dns.quad9.net ... OK — answered in 213 ms
```

Добавить свой апстрим — с живой проверкой до записи в конфиг (мёртвый
сервер случайно не попадёт; обходится флагом `--force`):

```sh
~ # doqd add quic://dns.quad9.net
probing quic://dns.quad9.net ... OK (198 ms)
added to /opt/etc/doqd.conf (upstream #3)
restarting the daemon ... alive (pid 20702)
```

Удалить — по номеру из `list` или по URL (последний апстрим удалить
нельзя):

```sh
~ # doqd remove 3
removed quic://dns.quad9.net
restarting the daemon ... alive (pid 20702)
```

Диагностика одной командой:

```sh
~ # doqd status
daemon:          running (pid 9772, uptime 72h3m10s)
listen:          192.168.1.1:5354 (udp+tcp)
registration:    present in KeeneticOS name-servers
resolve via :53: NOERROR, 41 ms
```

## Конфигурация — `/opt/etc/doqd.conf`

| Ключ | По умолчанию | Что делает |
|---|---|---|
| `listen` | `<LAN-IP>:5354` | адрес:порт слушателя (UDP+TCP) |
| `upstream` | `quic://dns.comss.one`, `quic://dns.quad9.net` | DoQ-апстрим, строка на сервер; первая строка отменяет дефолты |
| `cache_size` | `4096` | максимум записей кеша |
| `min_ttl` / `max_ttl` | `60` / `86400` | границы TTL кеша, сек |
| `log` | `info` | debug / info / warn / error |

Через `doqd add`/`doqd remove` демон перезапускается автоматически; после
ручной правки — `/opt/etc/init.d/S56doqd restart`.

## Проверка работы

```sh
# прямой запрос в doqd (порт 5354), повторный — из кеша
dig @192.168.1.1 -p 5354 example.com

# сквозной путь через штатный DNS
dig @192.168.1.1 example.com

# регистрация в системном DNS
ndmc -c 'show ip name-server'        # должен быть <LAN-IP>:5354

# доказательство QUIC: на роутере
tcpdump -ni any 'udp port 853'
```

## FAQ

**Почему в дефолтах comss и Quad9, а не AdGuard?** В ряде сетей РФ AdGuard
DNS блокируется ТСПУ и по DoQ, и по DoT — `doqd test
quic://dns.adguard-dns.com` покажет таймаут рукопожатия, а держать в
дефолтах заведомо мёртвый сервер незачем. comss стоит первым как самый
быстрый из проверенных (~130 мс с роутера против ~200 мс у Quad9),
Quad9 — резерв. Проверьте свои: `doqd list` пробует каждый сервер живым
запросом.

**Оба дефолтных сервера что-то фильтруют.** comss режет рекламу, трекеры и
вредоносные домены, `dns.quad9.net` блокирует малварные. Нужен чистый
резолвинг без чужих списков — возьмите нефильтрующие варианты:
`doqd add quic://dns10.quad9.net` (Quad9 без блокировок) или
`doqd add quic://unfiltered.adguard-dns.com`, если AdGuard у вас доступен.

**Почему порт 5354, а не 5353?** 5353 на Keenetic занят avahi-daemon
(mDNS).

**Почему LAN-адрес, а не 127.0.0.1?** KeeneticOS не принимает loopback в
`ip name-server` (`Dns::Manager: invalid IP address`).

**Добавил сервер, а он down.** `doqd list` показывает живость и RTT
каждого апстрима; `doqd remove <номер>` убирает лишний. При добавлении
`doqd add` сам проверяет сервер и не даст записать мёртвый без `--force`.

## Удаление

```sh
curl -fsSL https://raw.githubusercontent.com/necronicle/keenetic-doq/main/uninstall.sh | sh
```

Снимает регистрацию name-server, останавливает и удаляет демон. Конфиг
`/opt/etc/doqd.conf` остаётся.

## Сборка из исходников

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o doqd ./cmd/doqd
# mipsel/mips: GOARCH=mipsle|mips GOMIPS=softfloat
```

## Ограничения

- Только клиентская сторона DoQ (исходящие запросы). Входящий DoQ-сервер —
  вне объёма.
- Никакой фильтрации и блокировок: что ответил апстрим, то и вернулось.
- doqd слушает только LAN-адрес роутера — наружу (WAN) не торчит.

## Лицензия

[GPL-3.0](LICENSE)
