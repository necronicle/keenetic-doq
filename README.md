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

Keenetic с установленным [Entware](https://help.keenetic.com/hc/ru/articles/360000925139)
и пакетом `curl` (`opkg install curl`) — он нужен для скачивания.
Встроенный в busybox `wget` на Keenetic собран без TLS и `https://` не
открывает, поэтому именно curl. Всё остальное, что использует doqd и его
скрипты, входит в busybox и есть всегда.

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
resolve via doqd: NOERROR, 148 ms
resolve via :53:  NOERROR, 40 ms
```

## Конфигурация — `/opt/etc/doqd.conf`

| Ключ | По умолчанию | Что делает |
|---|---|---|
| `listen` | `<LAN-IP>:5354` | адрес:порт слушателя (UDP+TCP) |
| `upstream` | `quic://dns.comss.one`, `quic://dns.quad9.net` | DoQ-апстрим, строка на сервер; первая строка отменяет дефолты |
| `bootstrap` | `77.88.8.8`, `8.8.8.8`, `1.1.1.1` | обычные DNS-серверы для резолва имён апстримов; только IP, перебор по порядку |
| `cache_size` | `4096` | максимум записей кеша |
| `min_ttl` / `max_ttl` | `60` / `86400` | границы TTL кеша, сек |
| `log` | `info` | debug / info / warn / error |

Через `doqd add`/`doqd remove` демон перезапускается автоматически; после
ручной правки — `/opt/etc/init.d/S56doqd restart`.

## Проверка работы

Всё нужное делает `doqd status` — он резолвит и напрямую через doqd, и
через штатный `:53`, и проверяет регистрацию:

```sh
~ # doqd status
daemon:          running (pid 15644, uptime 3d1h)
listen:          192.168.1.1:5354 (udp+tcp)
registration:    present in KeeneticOS name-servers
resolve via doqd: NOERROR, 148 ms
resolve via :53:  NOERROR, 40 ms
```

Регистрация подробно и доказательство, что трафик реально идёт по QUIC:

```sh
ndmc -c 'show ip name-server'        # штатная утилита, есть всегда
tcpdump -ni any 'udp port 853'       # нужен пакет: opkg install tcpdump
```

`dig` в Entware тоже не входит: либо `opkg install bind-dig`, либо
запускайте его с компьютера в той же сети:

```sh
dig @192.168.1.1 -p 5354 example.com   # прямо в doqd, повтор — из кеша
dig @192.168.1.1 example.com           # сквозной путь через штатный DNS
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

**`ndmc: system failed [0xcffd0062]` / `Cli::Main: failed to initialize`,
а `doqd status` пишет `registration: NOT found`.** Это не про doqd — он
установлен и работает, просто из вашей SSH-сессии недоступен CLI роутера
(так бывает, если сессия идёт в OPKG-окружении или у учётной записи нет
прав на командную строку). Допишите регистрацию через веб-интерфейс:
откройте `http://<адрес-роутера>/a` (Web CLI) и выполните две команды:

```
ip name-server <LAN-IP>:5354
system configuration save
```

После этого `doqd status` покажет `registration: present`. Пока
регистрации нет, запросы идут мимо doqd — через штатный DNS.

**После перезагрузки роутера пропал интернет: IP пингуются, имена не
резолвятся, а `doqd list` пишет `lookup dns.comss.one on 127.0.0.1:53:
i/o timeout`.** Это баг версий до 0.3.0. doqd резолвил имена своих апстримов
системным резолвером, а на Keenetic это `127.0.0.1` — тот самый DNS роутера,
в списке серверов которого прописан сам doqd. Пока WAN поднят, на запрос
отвечают серверы провайдера; на загрузке, пока соединение не установлено,
doqd остаётся единственным name-server-ом, и его собственный запрос про
`dns.comss.one` возвращается ему же. DNS роутера встаёт колом. Лечится
обновлением: `curl -fsSL
https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh | sh`
— начиная с 0.3.0 имена апстримов резолвятся через отдельные
`bootstrap`-серверы, минуя DNS роутера. Конфиг править не нужно, но если
внешний 53-й порт у вашего провайдера закрыт, пропишите свои:
`bootstrap <IP>` в `/opt/etc/doqd.conf`.

**После перезагрузки роутера doqd не поднимается.** С версии 0.2.5 демон
дожидается своего адреса: на загрузке `/opt` монтируется раньше, чем
интерфейс получает адрес, и одиночная попытка bind раньше убивала демона
насовсем (init-скрипт Entware повторных попыток не делает). Если и на
0.2.5 не поднимается — посмотрите `doqd status`: когда адрес из `listen`
не поднят ни на одном интерфейсе, он скажет об этом прямо. Обычно это
значит, что LAN-адрес роутеру выдаёт вышестоящий DHCP (режим Relay) и
после перезагрузки он сменился: пропишите роутеру постоянный LAN-адрес,
поправьте `listen` в `/opt/etc/doqd.conf` и перерегистрируйте
`ip name-server <новый-адрес>:5354`.

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

- Шифруется участок «роутер → внешний резолвер» — тот, который видит
  провайдер. Внутри LAN устройства обращаются к роутеру обычным DNS: doqd
  не принимает DoQ-подключения от клиентов локальной сети. Ровно так же
  устроены и штатные DoT/DoH в KeeneticOS.
- Никакой фильтрации и блокировок: что ответил апстрим, то и вернулось.
- doqd слушает только LAN-адрес роутера — наружу (WAN) не торчит.

## Лицензия

[GPL-3.0](LICENSE)
