# keenetic-doq

DNS-over-QUIC (RFC 9250) для роутеров Keenetic — **рядом** со штатными
DoT/DoH, а не вместо них.

KeeneticOS умеет DoT и DoH, но не DoQ, и добавлять его не планирует.
Существующие решения (AdGuard Home, dnsproxy) требуют `opkg dns-override`:
захватывают порт 53, вытесняют штатный `ndnproxy` и ломают соседние
проекты. `keenetic-doq` работает иначе:

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
- Статические бинарники: aarch64, mipsel, mips (BE). Без зависимостей.

## Установка

Нужен Entware. На роутере:

```sh
cd /opt/tmp
curl -fsSLO https://raw.githubusercontent.com/necronicle/keenetic-doq/main/deploy/install.sh
curl -fsSLO https://raw.githubusercontent.com/necronicle/keenetic-doq/main/deploy/doqd.conf
curl -fsSLO https://raw.githubusercontent.com/necronicle/keenetic-doq/main/deploy/S56doqd
sh install.sh                     # скачает бинарник из GitHub Releases
```

Либо, если бинарник уже скопирован на роутер (например, пока репозиторий
приватный):

```sh
sh install.sh --local ./doqd-linux-arm64
```

Инсталлер: определяет архитектуру, ставит `/opt/sbin/doqd`, кладёт конфиг
`/opt/etc/doqd.conf` (подставляя LAN-адрес) и автозапуск
`/opt/etc/init.d/S56doqd`, запускает демон и регистрирует name-server.

## Конфигурация — `/opt/etc/doqd.conf`

| Ключ | По умолчанию | Что делает |
|---|---|---|
| `listen` | `<LAN-IP>:5354` | адрес:порт слушателя (UDP+TCP) |
| `upstream` | `quic://dns.comss.one`, `quic://unfiltered.adguard-dns.com` | DoQ-апстрим, строка на сервер; первая строка отменяет дефолты |
| `cache_size` | `4096` | максимум записей кеша |
| `min_ttl` / `max_ttl` | `60` / `86400` | границы TTL кеша, сек |
| `log` | `info` | debug / info / warn / error |

После правки: `/opt/etc/init.d/S56doqd restart`.

Почему comss первым: AdGuard DoQ/DoT в ряде сетей РФ блокируется ТСПУ;
failover это переживает, но быстрее, когда первым стоит доступный сервер.

## Проверка

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

## Удаление

```sh
sh /opt/tmp/keenetic-doq/uninstall.sh
```

Снимает регистрацию name-server, останавливает и удаляет демон.

## Сборка из исходников

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o doqd ./cmd/doqd
# mipsel/mips: GOARCH=mipsle|mips GOMIPS=softfloat
```

## Ограничения

- Только клиентская сторона DoQ (исходящие запросы). Входящий DoQ-сервер —
  вне объёма.
- Никакой фильтрации и блокировок: что ответил апстрим, то и вернулось.
- KeeneticOS не принимает `127.0.0.1` в `ip name-server`, поэтому doqd
  слушает LAN-адрес; порт 5353 занят avahi (mDNS), поэтому 5354.
