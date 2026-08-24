# keenetic-doq Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** DNS-over-QUIC прокси `doqd` для Keenetic, который встаёт апстримом рядом со штатными DoT/DoH, не трогая порт 53.

**Architecture:** Go-демон слушает обычный DNS (UDP+TCP) на `127.0.0.1:5353`, резолвит через кеш → пул DoQ-апстримов (RFC 9250, живые QUIC-соединения, failover). Инсталлер регистрирует его в KeeneticOS командой `ip name-server 127.0.0.1:5353`; `ndnproxy` продолжает владеть портом 53.

**Tech Stack:** Go 1.23+, `github.com/quic-go/quic-go v0.48.2`, `github.com/miekg/dns v1.1.62`, shell-скрипты для Entware, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-08-24-keenetic-doq-design.md`

## Global Constraints

- Модуль: `github.com/necronicle/keenetic-doq`; бинарник называется `doqd`.
- Go ≥ 1.23; зависимости ровно: `github.com/quic-go/quic-go v0.48.2`, `github.com/miekg/dns v1.1.62`.
- Все сборки: `CGO_ENABLED=0`, `-ldflags "-s -w"` (статические бинарники).
- Никогда не биндить порт 53. Слушатель по умолчанию: `127.0.0.1:5353`.
- Апстримы по умолчанию (в этом порядке): `quic://unfiltered.adguard-dns.com`, `quic://dns.adguard-dns.com`.
- RFC 9250: на проводе DNS message ID = 0; кадр = 2-байтовый big-endian length prefix + сообщение.
- Каждый коммит завершается трейлером `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Тест-роутер: `192.168.1.1`, SSH порт `222`, root, aarch64 (доступ: `sshpass`, креды в памяти проекта). Прод-действий на чужих устройствах нет.

## File Structure

```
go.mod, go.sum
cmd/doqd/main.go                 — wiring: config → upstreams → picker → cache → resolver → server
internal/config/config.go        — Default() + Parse()/Load() конфига key value
internal/config/config_test.go
internal/cache/cache.go          — TTL+LRU кеш DNS-ответов
internal/cache/cache_test.go
internal/upstream/doq.go         — DoQ-клиент (quic-go), RFC 9250
internal/upstream/doq_test.go    — в т.ч. in-process тестовый DoQ-сервер
internal/upstream/picker.go      — выбор апстрима: EWMA RTT, cooldown, health-check
internal/upstream/picker_test.go
internal/resolver/resolver.go    — cache→upstream клей
internal/resolver/resolver_test.go
internal/server/server.go        — UDP+TCP dns.Server
internal/server/server_test.go
deploy/doqd.conf                 — дефолтный конфиг
deploy/S56doqd                   — init-скрипт Entware
deploy/install.sh, deploy/uninstall.sh
.github/workflows/ci.yml         — vet+test на push/PR
.github/workflows/release.yml    — кросс-сборка и релиз по тегу v*
README.md, .gitignore
```

---

### Task 1: Каркас модуля и конфиг

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Config{Listen string; Upstreams []string; CacheSize int; MinTTL, MaxTTL time.Duration; LogLevel string}`, `config.Default() *Config`, `config.Parse(r io.Reader) (*Config, error)`, `config.Load(path string) (*Config, error)`.

- [ ] **Step 1: Инициализировать модуль и .gitignore**

```bash
cd /Library/DOQ
go mod init github.com/necronicle/keenetic-doq
```

`.gitignore`:
```
/doqd
/dist/
*.test
```

- [ ] **Step 2: Написать падающий тест**

`internal/config/config_test.go`:
```go
package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Listen != "127.0.0.1:5353" {
		t.Errorf("Listen = %q", c.Listen)
	}
	want := []string{"quic://unfiltered.adguard-dns.com", "quic://dns.adguard-dns.com"}
	if len(c.Upstreams) != 2 || c.Upstreams[0] != want[0] || c.Upstreams[1] != want[1] {
		t.Errorf("Upstreams = %v", c.Upstreams)
	}
	if c.CacheSize != 4096 || c.MinTTL != 60*time.Second || c.MaxTTL != 24*time.Hour || c.LogLevel != "info" {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestParseOverridesAndUpstreamReplacement(t *testing.T) {
	in := `
# comment
listen 0.0.0.0:5454
upstream quic://dns.example.com
upstream quic://dns2.example.com:8853
cache_size 100
min_ttl 10
max_ttl 3600
log debug
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:5454" || c.CacheSize != 100 || c.LogLevel != "debug" {
		t.Errorf("%+v", c)
	}
	if c.MinTTL != 10*time.Second || c.MaxTTL != 3600*time.Second {
		t.Errorf("ttl: %+v", c)
	}
	// первая же строка upstream ЗАМЕНЯЕТ дефолтные, а не дополняет
	if len(c.Upstreams) != 2 || c.Upstreams[0] != "quic://dns.example.com" {
		t.Errorf("Upstreams = %v", c.Upstreams)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"listen", "unknown_key 1", "cache_size abc", "min_ttl x"} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("Parse(%q): want error", bad)
		}
	}
}
```

- [ ] **Step 3: Запустить тест — убедиться, что падает**

Run: `go test ./internal/config/`
Expected: FAIL (undefined: Default, Parse)

- [ ] **Step 4: Минимальная реализация**

`internal/config/config.go`:
```go
// Package config parses doqd.conf — плоский файл "key value".
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen    string
	Upstreams []string
	CacheSize int
	MinTTL    time.Duration
	MaxTTL    time.Duration
	LogLevel  string
}

func Default() *Config {
	return &Config{
		Listen:    "127.0.0.1:5353",
		Upstreams: []string{"quic://unfiltered.adguard-dns.com", "quic://dns.adguard-dns.com"},
		CacheSize: 4096,
		MinTTL:    60 * time.Second,
		MaxTTL:    24 * time.Hour,
		LogLevel:  "info",
	}
}

func Parse(r io.Reader) (*Config, error) {
	cfg := Default()
	sawUpstream := false
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: %q needs a value", line, fields[0])
		}
		key, val := fields[0], fields[1]
		switch key {
		case "listen":
			cfg.Listen = val
		case "upstream":
			if !sawUpstream {
				cfg.Upstreams = nil
				sawUpstream = true
			}
			cfg.Upstreams = append(cfg.Upstreams, val)
		case "cache_size":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("line %d: bad cache_size %q", line, val)
			}
			cfg.CacheSize = n
		case "min_ttl", "max_ttl":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("line %d: bad %s %q", line, key, val)
			}
			if key == "min_ttl" {
				cfg.MinTTL = time.Duration(n) * time.Second
			} else {
				cfg.MaxTTL = time.Duration(n) * time.Second
			}
		case "log":
			cfg.LogLevel = val
		default:
			return nil, fmt.Errorf("line %d: unknown key %q", line, key)
		}
	}
	return cfg, sc.Err()
}

// Load читает конфиг из файла; отсутствие файла — ошибка (main сам решает
// использовать Default при отсутствии).
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}
```

- [ ] **Step 5: Запустить тест — убедиться, что проходит**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod .gitignore internal/config/
git commit -m "feat: module scaffold and doqd.conf parser

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Кеш ответов (TTL + LRU)

**Files:**
- Create: `internal/cache/cache.go`
- Test: `internal/cache/cache_test.go`

**Interfaces:**
- Consumes: `github.com/miekg/dns` (добавляется в go.mod здесь).
- Produces: `cache.New(maxEntries int, minTTL, maxTTL time.Duration) *Cache`; `(*Cache).Get(q dns.Question) *dns.Msg` (nil при промахе/истечении; возвращает КОПИЮ с уменьшенными TTL); `(*Cache).Put(q dns.Question, resp *dns.Msg)` (кеширует только NOERROR/NXDOMAIN); поле `now func() time.Time` для тестов (по умолчанию `time.Now`).

- [ ] **Step 1: Добавить зависимость miekg/dns**

```bash
go get github.com/miekg/dns@v1.1.62
```

- [ ] **Step 2: Написать падающий тест**

`internal/cache/cache_test.go`:
```go
package cache

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func q(name string, qtype uint16) dns.Question {
	return dns.Question{Name: dns.Fqdn(name), Qtype: qtype, Qclass: dns.ClassINET}
}

func respA(name string, ttl uint32, rcode int) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	m.Rcode = rcode
	if rcode == dns.RcodeSuccess {
		rr, _ := dns.NewRR(dns.Fqdn(name) + " " + " 0 IN A 1.2.3.4")
		rr.Header().Ttl = ttl
		m.Answer = append(m.Answer, rr)
	}
	return m
}

func newTestCache(size int) (*Cache, *time.Time) {
	c := New(size, 1*time.Second, 1*time.Hour)
	fake := time.Unix(1000000, 0)
	c.now = func() time.Time { return fake }
	return c, &fake
}

func TestHitAndTTLDecrement(t *testing.T) {
	c, clock := newTestCache(10)
	c.Put(q("example.com", dns.TypeA), respA("example.com", 300, dns.RcodeSuccess))
	*clock = clock.Add(100 * time.Second)
	got := c.Get(q("example.com", dns.TypeA))
	if got == nil {
		t.Fatal("want hit")
	}
	if ttl := got.Answer[0].Header().Ttl; ttl != 200 {
		t.Errorf("ttl = %d, want 200", ttl)
	}
}

func TestExpiry(t *testing.T) {
	c, clock := newTestCache(10)
	c.Put(q("example.com", dns.TypeA), respA("example.com", 300, dns.RcodeSuccess))
	*clock = clock.Add(301 * time.Second)
	if c.Get(q("example.com", dns.TypeA)) != nil {
		t.Error("want expired miss")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	c, _ := newTestCache(10)
	c.Put(q("example.com", dns.TypeA), respA("example.com", 300, dns.RcodeSuccess))
	a := c.Get(q("example.com", dns.TypeA))
	a.Answer[0].Header().Ttl = 1 // портим копию
	b := c.Get(q("example.com", dns.TypeA))
	if b.Answer[0].Header().Ttl != 300 {
		t.Error("Get must return an independent copy")
	}
}

func TestCaseInsensitiveKey(t *testing.T) {
	c, _ := newTestCache(10)
	c.Put(q("Example.COM", dns.TypeA), respA("example.com", 300, dns.RcodeSuccess))
	if c.Get(q("example.com", dns.TypeA)) == nil {
		t.Error("keys must be case-insensitive")
	}
}

func TestOnlyCacheableRcodes(t *testing.T) {
	c, _ := newTestCache(10)
	c.Put(q("srvfail.com", dns.TypeA), respA("srvfail.com", 300, dns.RcodeServerFailure))
	if c.Get(q("srvfail.com", dns.TypeA)) != nil {
		t.Error("SERVFAIL must not be cached")
	}
	c.Put(q("nx.com", dns.TypeA), respA("nx.com", 300, dns.RcodeNameError))
	if c.Get(q("nx.com", dns.TypeA)) == nil {
		t.Error("NXDOMAIN must be cached")
	}
}

func TestTTLClamp(t *testing.T) {
	c, clock := newTestCache(10)
	c.Put(q("low.com", dns.TypeA), respA("low.com", 0, dns.RcodeSuccess)) // ниже MinTTL=1s
	*clock = clock.Add(500 * time.Millisecond)
	if c.Get(q("low.com", dns.TypeA)) == nil {
		t.Error("TTL must be clamped up to MinTTL")
	}
}

func TestLRUEviction(t *testing.T) {
	c, _ := newTestCache(2)
	c.Put(q("a.com", dns.TypeA), respA("a.com", 300, dns.RcodeSuccess))
	c.Put(q("b.com", dns.TypeA), respA("b.com", 300, dns.RcodeSuccess))
	c.Get(q("a.com", dns.TypeA)) // a становится свежим
	c.Put(q("c.com", dns.TypeA), respA("c.com", 300, dns.RcodeSuccess))
	if c.Get(q("b.com", dns.TypeA)) != nil {
		t.Error("b must be evicted (LRU)")
	}
	if c.Get(q("a.com", dns.TypeA)) == nil || c.Get(q("c.com", dns.TypeA)) == nil {
		t.Error("a and c must survive")
	}
}
```

- [ ] **Step 3: Запустить — падает**

Run: `go test ./internal/cache/`
Expected: FAIL (undefined: New)

- [ ] **Step 4: Реализация**

`internal/cache/cache.go`:
```go
// Package cache — TTL+LRU кеш DNS-ответов для doqd.
package cache

import (
	"container/list"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const emptyAnswerTTL = 60 * time.Second // ответ без RR (напр. NXDOMAIN без SOA)

type entry struct {
	key      string
	msg      *dns.Msg
	storedAt time.Time
	ttl      time.Duration
}

type Cache struct {
	mu      sync.Mutex
	max     int
	minTTL  time.Duration
	maxTTL  time.Duration
	byKey   map[string]*list.Element
	lru     *list.List // front = самый свежий
	now     func() time.Time
}

func New(maxEntries int, minTTL, maxTTL time.Duration) *Cache {
	return &Cache{
		max:    maxEntries,
		minTTL: minTTL,
		maxTTL: maxTTL,
		byKey:  make(map[string]*list.Element),
		lru:    list.New(),
		now:    time.Now,
	}
}

func key(q dns.Question) string {
	return strings.ToLower(q.Name) + "|" + strconv.Itoa(int(q.Qtype)) + "|" + strconv.Itoa(int(q.Qclass))
}

// respTTL — минимальный TTL по всем RR (кроме OPT), clamped в [minTTL, maxTTL].
func (c *Cache) respTTL(m *dns.Msg) time.Duration {
	minSec := uint32(0)
	found := false
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if !found || rr.Header().Ttl < minSec {
				minSec = rr.Header().Ttl
				found = true
			}
		}
	}
	ttl := emptyAnswerTTL
	if found {
		ttl = time.Duration(minSec) * time.Second
	}
	if ttl < c.minTTL {
		ttl = c.minTTL
	}
	if ttl > c.maxTTL {
		ttl = c.maxTTL
	}
	return ttl
}

func (c *Cache) Put(q dns.Question, resp *dns.Msg) {
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		return
	}
	k := key(q)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[k]; ok {
		c.lru.Remove(el)
		delete(c.byKey, k)
	}
	e := &entry{key: k, msg: resp.Copy(), storedAt: c.now(), ttl: c.respTTL(resp)}
	c.byKey[k] = c.lru.PushFront(e)
	for c.lru.Len() > c.max {
		last := c.lru.Back()
		c.lru.Remove(last)
		delete(c.byKey, last.Value.(*entry).key)
	}
}

func (c *Cache) Get(q dns.Question) *dns.Msg {
	k := key(q)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[k]
	if !ok {
		return nil
	}
	e := el.Value.(*entry)
	elapsed := c.now().Sub(e.storedAt)
	if elapsed >= e.ttl {
		c.lru.Remove(el)
		delete(c.byKey, k)
		return nil
	}
	c.lru.MoveToFront(el)
	out := e.msg.Copy()
	dec := uint32(elapsed / time.Second)
	for _, sec := range [][]dns.RR{out.Answer, out.Ns, out.Extra} {
		for _, rr := range sec {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if rr.Header().Ttl > dec {
				rr.Header().Ttl -= dec
			} else {
				rr.Header().Ttl = 1
			}
		}
	}
	return out
}
```

- [ ] **Step 5: Запустить — проходит**

Run: `go test ./internal/cache/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/cache/
git commit -m "feat: TTL+LRU DNS response cache

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: DoQ-клиент (RFC 9250)

**Files:**
- Create: `internal/upstream/doq.go`
- Test: `internal/upstream/doq_test.go`

**Interfaces:**
- Consumes: `github.com/quic-go/quic-go v0.48.2` (добавляется здесь).
- Produces: `upstream.NewDoQ(rawURL string) (*DoQ, error)`; `(*DoQ).Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error)`; `(*DoQ).Address() string`; `(*DoQ).Close() error`; экспортируемое поле `(*DoQ).TLSConfig *tls.Config` (для тестов с self-signed сертом).

- [ ] **Step 1: Добавить quic-go**

```bash
go get github.com/quic-go/quic-go@v0.48.2
```

- [ ] **Step 2: Написать падающий тест (с in-process DoQ-сервером)**

`internal/upstream/doq_test.go`:
```go
package upstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

// startTestDoQServer поднимает минимальный DoQ-сервер на 127.0.0.1:0.
// Возвращает адрес и указатель на слайс DNS ID, увиденных на проводе.
func startTestDoQServer(t *testing.T, handler func(q *dns.Msg) *dns.Msg) (string, func() []uint16) {
	t.Helper()
	pkey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &pkey.PublicKey, pkey)
	if err != nil {
		t.Fatal(err)
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: pkey}},
		NextProtos:   []string{"doq"},
	}
	ln, err := quic.ListenAddr("127.0.0.1:0", tlsConf, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var ids []uint16
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func(conn quic.Connection) {
				for {
					stream, err := conn.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func(s quic.Stream) {
						defer s.Close()
						var lb [2]byte
						if _, err := io.ReadFull(s, lb[:]); err != nil {
							return
						}
						buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
						if _, err := io.ReadFull(s, buf); err != nil {
							return
						}
						var q dns.Msg
						if q.Unpack(buf) != nil {
							return
						}
						mu.Lock()
						ids = append(ids, q.Id)
						mu.Unlock()
						resp := handler(&q)
						out, _ := resp.Pack()
						var ob [2]byte
						binary.BigEndian.PutUint16(ob[:], uint16(len(out)))
						s.Write(ob[:])
						s.Write(out)
					}(stream)
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() []uint16 {
		mu.Lock()
		defer mu.Unlock()
		return append([]uint16(nil), ids...)
	}
}

func testHandler(q *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(q)
	rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp
}

func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"doq"}}
}

func TestExchange(t *testing.T) {
	addr, gotIDs := startTestDoQServer(t, testHandler)
	u, err := NewDoQ("quic://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	u.TLSConfig = insecureTLS()

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Id = 4242
	resp, err := u.Exchange(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Id != 4242 {
		t.Errorf("resp.Id = %d, want 4242 (restored)", resp.Id)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("Answer = %v", resp.Answer)
	}
	// RFC 9250 §4.2.1: на проводе ID обязан быть 0
	for _, id := range gotIDs() {
		if id != 0 {
			t.Errorf("wire ID = %d, want 0", id)
		}
	}
}

func TestConnectionReuse(t *testing.T) {
	addr, _ := startTestDoQServer(t, testHandler)
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	for i := 0; i < 3; i++ {
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		if _, err := u.Exchange(context.Background(), q); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
	}
	if u.dialCount.Load() != 1 {
		t.Errorf("dials = %d, want 1 (connection reuse)", u.dialCount.Load())
	}
}

func TestExchangeErrorWhenServerDown(t *testing.T) {
	u, _ := NewDoQ("quic://127.0.0.1:1") // никто не слушает
	defer u.Close()
	u.TLSConfig = insecureTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := u.Exchange(ctx, q); err == nil {
		t.Error("want error")
	}
}

func TestNewDoQParse(t *testing.T) {
	u, err := NewDoQ("quic://dns.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.Address() != "dns.example.com:853" {
		t.Errorf("Address = %q, want default port 853", u.Address())
	}
	if _, err := NewDoQ("tls://dns.example.com"); err == nil {
		t.Error("non-quic scheme must fail")
	}
}
```

- [ ] **Step 3: Запустить — падает**

Run: `go test ./internal/upstream/`
Expected: FAIL (undefined: NewDoQ)

- [ ] **Step 4: Реализация**

`internal/upstream/doq.go`:
```go
// Package upstream — DoQ-клиенты (RFC 9250) и выбор апстрима.
package upstream

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

const defaultDoQPort = "853"

type DoQ struct {
	addr      string // host:port для dial
	host      string // SNI
	TLSConfig *tls.Config

	mu        sync.Mutex
	conn      quic.Connection
	dialCount atomic.Int64
}

func NewDoQ(rawURL string) (*DoQ, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("upstream %q: %w", rawURL, err)
	}
	if u.Scheme != "quic" {
		return nil, fmt.Errorf("upstream %q: scheme must be quic://", rawURL)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return nil, fmt.Errorf("upstream %q: no host", rawURL)
	}
	if port == "" {
		port = defaultDoQPort
	}
	return &DoQ{
		addr:      net.JoinHostPort(host, port),
		host:      host,
		TLSConfig: &tls.Config{ServerName: host, NextProtos: []string{"doq"}},
	}, nil
}

func (u *DoQ) Address() string { return u.addr }

func (u *DoQ) getConn(ctx context.Context) (quic.Connection, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil && u.conn.Context().Err() == nil {
		return u.conn, nil
	}
	u.conn = nil
	conf := &quic.Config{
		MaxIdleTimeout:  90 * time.Second,
		KeepAlivePeriod: 20 * time.Second,
	}
	conn, err := quic.DialAddr(ctx, u.addr, u.TLSConfig, conf)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", u.addr, err)
	}
	u.dialCount.Add(1)
	u.conn = conn
	return conn, nil
}

func (u *DoQ) dropConn(conn quic.Connection) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn == conn {
		u.conn = nil
	}
	conn.CloseWithError(0, "")
}

func exchangeOnConn(ctx context.Context, conn quic.Connection, payload []byte) (*dns.Msg, error) {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		stream.SetDeadline(dl)
	}
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(payload)))
	if _, err := stream.Write(lb[:]); err != nil {
		return nil, err
	}
	if _, err := stream.Write(payload); err != nil {
		return nil, err
	}
	stream.Close() // FIN на запись — сигнал "запрос целиком отправлен"
	if _, err := io.ReadFull(stream, lb[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(stream, buf); err != nil {
		return nil, err
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(buf); err != nil {
		return nil, err
	}
	return resp, nil
}

func (u *DoQ) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	wire := m.Copy()
	wire.Id = 0 // RFC 9250 §4.2.1
	payload, err := wire.Pack()
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := u.getConn(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := exchangeOnConn(ctx, conn, payload)
		if err != nil {
			u.dropConn(conn)
			lastErr = err
			continue
		}
		resp.Id = m.Id
		return resp, nil
	}
	return nil, lastErr
}

func (u *DoQ) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil {
		u.conn.CloseWithError(0, "")
		u.conn = nil
	}
	return nil
}
```

- [ ] **Step 5: Запустить — проходит**

Run: `go test ./internal/upstream/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/upstream/
git commit -m "feat: DoQ upstream client per RFC 9250

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Picker — выбор апстрима, failover, health-check

**Files:**
- Create: `internal/upstream/picker.go`
- Test: `internal/upstream/picker_test.go`

**Interfaces:**
- Produces: `upstream.Exchanger interface { Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error); Address() string }` (тип `*DoQ` ему удовлетворяет); `upstream.NewPicker(ups []Exchanger) *Picker` (cooldown 30s); `(*Picker).Exchange(ctx, m) (*dns.Msg, error)`; `(*Picker).StartHealthCheck(ctx context.Context, interval time.Duration)`; поле `(*Picker).now func() time.Time` для тестов.

- [ ] **Step 1: Написать падающий тест**

`internal/upstream/picker_test.go`:
```go
package upstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeUp struct {
	name  string
	fail  bool
	delay time.Duration
	calls int
}

func (f *fakeUp) Address() string { return f.name }
func (f *fakeUp) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.calls++
	if f.fail {
		return nil, errors.New(f.name + " down")
	}
	time.Sleep(f.delay)
	resp := new(dns.Msg)
	resp.SetReply(m)
	return resp, nil
}

func query() *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	return m
}

func TestFailover(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	if _, err := p.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("calls a=%d b=%d, want 1/1", a.calls, b.calls)
	}
}

func TestDownCooldownSkips(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	fake := time.Unix(1000000, 0)
	p.now = func() time.Time { return fake }
	p.Exchange(context.Background(), query()) // a падает, уходит в down
	p.Exchange(context.Background(), query()) // a в cooldown — не трогаем
	if a.calls != 1 {
		t.Errorf("a.calls = %d, want 1 (in cooldown)", a.calls)
	}
	fake = fake.Add(31 * time.Second) // cooldown истёк
	a.fail = false
	p.Exchange(context.Background(), query())
	if a.calls != 2 {
		t.Errorf("a.calls = %d, want 2 (retried after cooldown)", a.calls)
	}
}

func TestAllDownStillTries(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	p := NewPicker([]Exchanger{a})
	p.Exchange(context.Background(), query())            // уходит в down
	if _, err := p.Exchange(context.Background(), query()); err == nil {
		t.Error("want error when all down")
	}
	if a.calls != 2 {
		t.Errorf("a.calls = %d, want 2 (down upstreams are last resort, not skipped)", a.calls)
	}
}

func TestFastestFirst(t *testing.T) {
	slow := &fakeUp{name: "slow", delay: 30 * time.Millisecond}
	fast := &fakeUp{name: "fast", delay: time.Millisecond}
	p := NewPicker([]Exchanger{slow, fast})
	// прогрев: обоим даём по замеру (порядок конфигурации: slow первый)
	p.Exchange(context.Background(), query())
	slowCalls := slow.calls
	// slow теперь имеет большой EWMA; следующие запросы должны идти в fast
	for i := 0; i < 3; i++ {
		p.Exchange(context.Background(), query())
	}
	if slow.calls != slowCalls {
		t.Errorf("slow.calls grew to %d — fastest must be tried first", slow.calls)
	}
	if fast.calls < 3 {
		t.Errorf("fast.calls = %d, want >=3", fast.calls)
	}
}

func TestHealthCheckRevives(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	p.Exchange(context.Background(), query()) // a в down
	a.fail = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthCheck(ctx, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !p.isDown(a) {
			return // ожил
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("health check did not revive upstream")
}
```

- [ ] **Step 2: Запустить — падает**

Run: `go test ./internal/upstream/ -run 'TestFailover|TestDown|TestAllDown|TestFastest|TestHealth'`
Expected: FAIL (undefined: NewPicker)

- [ ] **Step 3: Реализация**

`internal/upstream/picker.go`:
```go
package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type Exchanger interface {
	Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error)
	Address() string
}

const downCooldown = 30 * time.Second

type upstreamState struct {
	ex        Exchanger
	rtt       time.Duration // EWMA; 0 = замеров ещё не было
	downUntil time.Time
}

type Picker struct {
	mu  sync.Mutex
	ups []*upstreamState
	now func() time.Time
}

func NewPicker(ups []Exchanger) *Picker {
	p := &Picker{now: time.Now}
	for _, u := range ups {
		p.ups = append(p.ups, &upstreamState{ex: u})
	}
	return p
}

// ordered: живые по возрастанию EWMA RTT (без замера — в конфиг-порядке
// перед замеренными не лезем: rtt 0 трактуем как "неизвестно, пробуем рано"),
// затем упавшие — последней надеждой.
func (p *Picker) ordered() []*upstreamState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var live, down []*upstreamState
	for _, st := range p.ups {
		if st.downUntil.After(now) {
			down = append(down, st)
		} else {
			live = append(live, st)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].rtt < live[j].rtt })
	return append(live, down...)
}

func (p *Picker) markSuccess(st *upstreamState, rtt time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.downUntil = time.Time{}
	if st.rtt == 0 {
		st.rtt = rtt
	} else {
		st.rtt = (st.rtt*7 + rtt) / 8
	}
}

func (p *Picker) markDown(st *upstreamState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.downUntil = p.now().Add(downCooldown)
}

func (p *Picker) isDown(ex Exchanger) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.ups {
		if st.ex == ex {
			return st.downUntil.After(p.now())
		}
	}
	return false
}

func (p *Picker) downStates() []*upstreamState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var down []*upstreamState
	for _, st := range p.ups {
		if st.downUntil.After(now) {
			down = append(down, st)
		}
	}
	return down
}

func (p *Picker) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	var lastErr error
	for _, st := range p.ordered() {
		start := time.Now()
		resp, err := st.ex.Exchange(ctx, m)
		if err != nil {
			slog.Warn("upstream failed", "upstream", st.ex.Address(), "err", err)
			p.markDown(st)
			lastErr = err
			continue
		}
		p.markSuccess(st, time.Since(start))
		return resp, nil
	}
	return nil, fmt.Errorf("all upstreams failed, last: %w", lastErr)
}

// StartHealthCheck фоново пробует упавшие апстримы запросом ". NS" и
// возвращает ожившие в ротацию раньше их cooldown-а.
func (p *Picker) StartHealthCheck(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for _, st := range p.downStates() {
				probe := new(dns.Msg)
				probe.SetQuestion(".", dns.TypeNS)
				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := st.ex.Exchange(cctx, probe)
				cancel()
				if err == nil {
					slog.Info("upstream recovered", "upstream", st.ex.Address())
					p.mu.Lock()
					st.downUntil = time.Time{}
					p.mu.Unlock()
				}
			}
		}
	}()
}
```

- [ ] **Step 4: Запустить — проходит (весь пакет)**

Run: `go test ./internal/upstream/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/upstream/picker.go internal/upstream/picker_test.go
git commit -m "feat: upstream picker with EWMA ordering, cooldown failover and health checks

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Resolver (кеш + апстрим)

**Files:**
- Create: `internal/resolver/resolver.go`
- Test: `internal/resolver/resolver_test.go`

**Interfaces:**
- Consumes: `cache.New/Get/Put` (Task 2), `upstream.Exchanger` (Task 4).
- Produces: `resolver.New(c *cache.Cache, up upstream.Exchanger) *Resolver`; `(*Resolver).Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error)` — ответ всегда с `Id` запроса.

- [ ] **Step 1: Написать падающий тест**

`internal/resolver/resolver_test.go`:
```go
package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
)

type fakeUp struct{ calls int }

func (f *fakeUp) Address() string { return "fake" }
func (f *fakeUp) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.calls++
	resp := new(dns.Msg)
	resp.SetReply(m)
	rr, _ := dns.NewRR(m.Question[0].Name + " 300 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp, nil
}

func TestCacheMissThenHit(t *testing.T) {
	up := &fakeUp{}
	r := New(cache.New(16, time.Second, time.Hour), up)
	q1 := new(dns.Msg)
	q1.SetQuestion("example.com.", dns.TypeA)
	q1.Id = 1
	resp1, err := r.Resolve(context.Background(), q1)
	if err != nil || len(resp1.Answer) != 1 {
		t.Fatalf("resp1=%v err=%v", resp1, err)
	}
	q2 := new(dns.Msg)
	q2.SetQuestion("example.com.", dns.TypeA)
	q2.Id = 2
	resp2, err := r.Resolve(context.Background(), q2)
	if err != nil {
		t.Fatal(err)
	}
	if up.calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (second must be cache hit)", up.calls)
	}
	if resp2.Id != 2 {
		t.Errorf("resp2.Id = %d, want 2 (request ID)", resp2.Id)
	}
}
```

- [ ] **Step 2: Запустить — падает**

Run: `go test ./internal/resolver/`
Expected: FAIL (undefined: New)

- [ ] **Step 3: Реализация**

`internal/resolver/resolver.go`:
```go
// Package resolver соединяет кеш и пул апстримов.
package resolver

import (
	"context"

	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

type Resolver struct {
	cache *cache.Cache
	up    upstream.Exchanger
}

func New(c *cache.Cache, up upstream.Exchanger) *Resolver {
	return &Resolver{cache: c, up: up}
}

func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(req.Question) != 1 {
		return r.up.Exchange(ctx, req) // экзотику не кешируем
	}
	q := req.Question[0]
	if resp := r.cache.Get(q); resp != nil {
		resp.Id = req.Id
		return resp, nil
	}
	resp, err := r.up.Exchange(ctx, req)
	if err != nil {
		return nil, err
	}
	r.cache.Put(q, resp)
	return resp, nil
}
```

- [ ] **Step 4: Запустить — проходит**

Run: `go test ./internal/resolver/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/resolver/
git commit -m "feat: resolver wiring cache and upstream pool

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: DNS-сервер (UDP + TCP)

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: ничего своего — принимает интерфейс `server.Resolver { Resolve(ctx, *dns.Msg) (*dns.Msg, error) }` (ему удовлетворяет `*resolver.Resolver` из Task 5).
- Produces: `server.New(listen string, r Resolver) *Server`; `(*Server).Start() error` (биндит UDP и TCP, реальный адрес — `(*Server).Addr() string`); `(*Server).Shutdown()`.

- [ ] **Step 1: Написать падающий тест**

`internal/server/server_test.go`:
```go
package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeResolver struct{ err error }

func (f *fakeResolver) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.err != nil {
		return nil, f.err
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	rr, _ := dns.NewRR(req.Question[0].Name + " 300 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp, nil
}

func startServer(t *testing.T, r Resolver) *Server {
	t.Helper()
	s := New("127.0.0.1:0", r)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	return s
}

func TestUDPAndTCP(t *testing.T) {
	s := startServer(t, &fakeResolver{})
	for _, netw := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: netw, Timeout: 2 * time.Second}
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		resp, _, err := c.Exchange(q, s.Addr())
		if err != nil {
			t.Fatalf("%s: %v", netw, err)
		}
		if len(resp.Answer) != 1 {
			t.Errorf("%s: Answer = %v", netw, resp.Answer)
		}
	}
}

func TestServfailOnResolverError(t *testing.T) {
	s := startServer(t, &fakeResolver{err: errors.New("boom")})
	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	resp, _, err := c.Exchange(q, s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Errorf("Rcode = %d, want SERVFAIL", resp.Rcode)
	}
}
```

- [ ] **Step 2: Запустить — падает**

Run: `go test ./internal/server/`
Expected: FAIL (undefined: New)

- [ ] **Step 3: Реализация**

`internal/server/server.go`:
```go
// Package server — обычный DNS-слушатель (UDP+TCP) поверх Resolver.
package server

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/miekg/dns"
)

type Resolver interface {
	Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}

const queryTimeout = 10 * time.Second

type Server struct {
	listen   string
	resolver Resolver
	addr     string
	udp      *dns.Server
	tcp      *dns.Server
}

func New(listen string, r Resolver) *Server {
	return &Server{listen: listen, resolver: r}
}

func (s *Server) handle(w dns.ResponseWriter, req *dns.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	resp, err := s.resolver.Resolve(ctx, req)
	if err != nil {
		slog.Warn("resolve failed", "err", err)
		fail := new(dns.Msg)
		fail.SetRcode(req, dns.RcodeServerFailure)
		w.WriteMsg(fail)
		return
	}
	w.WriteMsg(resp)
}

func (s *Server) Start() error {
	pc, err := net.ListenPacket("udp", s.listen)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		pc.Close()
		return err
	}
	s.addr = pc.LocalAddr().String()
	h := dns.HandlerFunc(s.handle)
	s.udp = &dns.Server{PacketConn: pc, Handler: h}
	s.tcp = &dns.Server{Listener: l, Handler: h}
	go s.udp.ActivateAndServe()
	go s.tcp.ActivateAndServe()
	return nil
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) Shutdown() {
	if s.udp != nil {
		s.udp.Shutdown()
	}
	if s.tcp != nil {
		s.tcp.Shutdown()
	}
}
```

- [ ] **Step 4: Запустить — проходит**

Run: `go test ./internal/server/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit -m "feat: UDP+TCP DNS listener

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: main.go и смоук против реального DoQ

**Files:**
- Create: `cmd/doqd/main.go`

**Interfaces:**
- Consumes: всё из Task 1–6 ровно с теми сигнатурами, что описаны выше.
- Produces: бинарник `doqd` с флагом `-c <путь к конфигу>` (default `/opt/etc/doqd.conf`; отсутствие файла = дефолтный конфиг) и `-version`.

- [ ] **Step 1: Реализация**

`cmd/doqd/main.go`:
```go
// doqd — DNS-over-QUIC форвардер для Keenetic (слушает обычный DNS,
// резолвит через DoQ-апстримы). Регистрируется в KeeneticOS как
// name-server на 127.0.0.1:5353 рядом со штатными DoT/DoH.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/config"
	"github.com/necronicle/keenetic-doq/internal/resolver"
	"github.com/necronicle/keenetic-doq/internal/server"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

var version = "dev" // подставляется при сборке через -ldflags "-X main.version=..."

func main() {
	confPath := flag.String("c", "/opt/etc/doqd.conf", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("doqd", version)
		return
	}

	cfg, err := config.Load(*confPath)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("config not found, using defaults", "path", *confPath)
			cfg = config.Default()
		} else {
			slog.Error("bad config", "err", err)
			os.Exit(1)
		}
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	var ups []upstream.Exchanger
	for _, raw := range cfg.Upstreams {
		u, err := upstream.NewDoQ(raw)
		if err != nil {
			slog.Error("bad upstream", "err", err)
			os.Exit(1)
		}
		ups = append(ups, u)
	}
	picker := upstream.NewPicker(ups)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	picker.StartHealthCheck(ctx, 30*time.Second)

	res := resolver.New(cache.New(cfg.CacheSize, cfg.MinTTL, cfg.MaxTTL), picker)
	srv := server.New(cfg.Listen, res)
	if err := srv.Start(); err != nil {
		slog.Error("listen failed", "addr", cfg.Listen, "err", err)
		os.Exit(1)
	}
	slog.Info("doqd started", "version", version, "listen", srv.Addr(), "upstreams", cfg.Upstreams)

	<-ctx.Done()
	slog.Info("shutting down")
	srv.Shutdown()
}
```

- [ ] **Step 2: Полная проверка сборки и тестов**

Run: `go vet ./... && go test ./... && go build -o doqd ./cmd/doqd`
Expected: всё зелёное, бинарник собран.

- [ ] **Step 3: Смоук против реального AdGuard DoQ (с мака)**

```bash
./doqd -c /dev/null &
sleep 2
dig @127.0.0.1 -p 5353 example.com +short
dig @127.0.0.1 -p 5353 example.com +short   # второй — из кеша, мгновенный
dig @127.0.0.1 -p 5353 +tcp google.com +short
kill %1
```
Expected: оба запроса возвращают A-записи; в логе doqd виден старт с дефолтными апстримами. (`-c /dev/null`? Нет — `/dev/null` существует и распарсится как пустой конфиг → дефолты; это ок.)

- [ ] **Step 4: Commit**

```bash
git add cmd/
git commit -m "feat: doqd entrypoint

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: Файлы поставки (conf, init, install/uninstall)

**Files:**
- Create: `deploy/doqd.conf`, `deploy/S56doqd`, `deploy/install.sh`, `deploy/uninstall.sh`

**Interfaces:**
- Consumes: бинарник `doqd` (Task 7), флаг `-c /opt/etc/doqd.conf`.
- Produces: контракт путей: бинарник `/opt/sbin/doqd`, конфиг `/opt/etc/doqd.conf`, init `/opt/etc/init.d/S56doqd`. Регистрация: `ip name-server 127.0.0.1:5353`. Используется Task 10.

- [ ] **Step 1: Дефолтный конфиг**

`deploy/doqd.conf`:
```
# doqd — DNS-over-QUIC форвардер. https://github.com/necronicle/keenetic-doq
# Слушатель (менять не рекомендуется: install.sh регистрирует именно этот адрес)
listen 127.0.0.1:5353

# DoQ-апстримы, в порядке предпочтения (первая строка отменяет встроенные дефолты)
upstream quic://unfiltered.adguard-dns.com
upstream quic://dns.adguard-dns.com

# Кеш: записей / TTL-границы в секундах
cache_size 4096
min_ttl 60
max_ttl 86400

# Логи: debug | info | warn | error
log info
```

- [ ] **Step 2: Init-скрипт (Entware rc.func)**

`deploy/S56doqd`:
```sh
#!/bin/sh

ENABLED=yes
PROCS=doqd
ARGS="-c /opt/etc/doqd.conf"
PREARGS=""
DESC="DNS-over-QUIC forwarder"
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
```

- [ ] **Step 3: install.sh**

`deploy/install.sh`:
```sh
#!/bin/sh
# Установка doqd на Keenetic (Entware). Запускать НА РОУТЕРЕ.
# Использование:
#   ./install.sh --local ./doqd     # бинарник уже скопирован рядом
#   ./install.sh                    # скачать из GitHub Releases (нужен публичный
#                                   # репозиторий или GITHUB_TOKEN в окружении)
set -e

REPO="necronicle/keenetic-doq"
BIN=/opt/sbin/doqd
CONF=/opt/etc/doqd.conf
INIT=/opt/etc/init.d/S56doqd
NS="127.0.0.1:5353"

log() { echo "[keenetic-doq] $*"; }
die() { echo "[keenetic-doq] ОШИБКА: $*" >&2; exit 1; }

# Команда в CLI Keenetic: ndmc (новый) или ndmq (старый)
ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then
        ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then
        ndmq -p "$1"
    else
        die "не найден ndmc/ndmq — выполните в CLI роутера вручную: $1"
    fi
}

[ -f /opt/etc/init.d/rc.func ] || die "Entware не найден (/opt/etc/init.d/rc.func)"

arch=$(opkg print-architecture | awk '/^arch/ {print $2}' | grep -v all | head -1)
case "$arch" in
    aarch64*) goarch=arm64 ;;
    mipsel*)  goarch=mipsle ;;
    mips*)    goarch=mips ;;
    *)        die "неизвестная архитектура Entware: $arch" ;;
esac
log "архитектура: $arch → doqd-linux-$goarch"

if [ "$1" = "--local" ]; then
    [ -f "$2" ] || die "файл $2 не найден"
    cp "$2" "$BIN.new"
else
    url="https://github.com/$REPO/releases/latest/download/doqd-linux-$goarch"
    log "скачиваю $url"
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -o "$BIN.new" "$url" \
            || die "не скачалось (приватный репозиторий? используйте --local)"
    else
        curl -fsSL -o "$BIN.new" "$url" \
            || die "не скачалось (приватный репозиторий? используйте --local)"
    fi
fi
chmod 755 "$BIN.new"
"$BIN.new" -version >/dev/null || die "бинарник не запускается (не та архитектура?)"

[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
mv "$BIN.new" "$BIN"

script_dir=$(dirname "$0")
[ -f "$CONF" ] || cp "$script_dir/doqd.conf" "$CONF"
cp "$script_dir/S56doqd" "$INIT"
chmod 755 "$INIT"

"$INIT" start
sleep 1
"$INIT" check | grep -q alive || die "doqd не запустился, смотрите логи"

log "регистрирую name-server $NS в KeeneticOS"
ndm_cmd "ip name-server $NS"
ndm_cmd "system configuration save"

log "готово. Проверка: ndnproxy теперь видит doqd как апстрим (show ip name-server)."
```

- [ ] **Step 4: uninstall.sh**

`deploy/uninstall.sh`:
```sh
#!/bin/sh
# Полное удаление doqd с роутера. Запускать НА РОУТЕРЕ.
set -e
NS="127.0.0.1:5353"
log() { echo "[keenetic-doq] $*"; }

ndm_cmd() {
    if command -v ndmc >/dev/null 2>&1; then ndmc -c "$1"
    elif command -v ndmq >/dev/null 2>&1; then ndmq -p "$1"
    else echo "[keenetic-doq] выполните в CLI роутера вручную: $1"; fi
}

log "снимаю регистрацию name-server"
ndm_cmd "no ip name-server $NS"
ndm_cmd "system configuration save"

[ -x /opt/etc/init.d/S56doqd ] && /opt/etc/init.d/S56doqd stop || true
rm -f /opt/etc/init.d/S56doqd /opt/sbin/doqd
log "конфиг /opt/etc/doqd.conf оставлен (удалите вручную при желании)"
log "готово"
```

- [ ] **Step 5: Проверить шеллы линтером**

Run: `sh -n deploy/install.sh && sh -n deploy/uninstall.sh && sh -n deploy/S56doqd`
Expected: без ошибок синтаксиса.

- [ ] **Step 6: Commit**

```bash
git add deploy/
git commit -m "feat: Entware deploy scripts (conf, init, install, uninstall)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: CI и релизная сборка

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/release.yml`

**Interfaces:**
- Consumes: go.mod (Task 1), исходники.
- Produces: артефакты релиза `doqd-linux-arm64`, `doqd-linux-mipsle`, `doqd-linux-mips` — имена, которые ждёт `install.sh` (Task 8).

- [ ] **Step 1: ci.yml**

`.github/workflows/ci.yml`:
```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - run: go vet ./...
      - run: go test ./...
      - name: cross-build all router arches
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o /dev/null ./cmd/doqd
          CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags "-s -w" -o /dev/null ./cmd/doqd
          CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat go build -ldflags "-s -w" -o /dev/null ./cmd/doqd
```

- [ ] **Step 2: release.yml**

`.github/workflows/release.yml`:
```yaml
name: release
on:
  push:
    tags: ['v*']
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - run: go test ./...
      - name: build
        run: |
          mkdir dist
          V=${GITHUB_REF_NAME}
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=$V" -o dist/doqd-linux-arm64 ./cmd/doqd
          CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags "-s -w -X main.version=$V" -o dist/doqd-linux-mipsle ./cmd/doqd
          CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat go build -ldflags "-s -w -X main.version=$V" -o dist/doqd-linux-mips ./cmd/doqd
      - uses: softprops/action-gh-release@v2
        with:
          files: dist/*
```

- [ ] **Step 3: Push и проверить зелёный CI**

```bash
git add .github/
git commit -m "ci: test + cross-build + release workflows

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
git push
gh run watch --exit-status
```
Expected: workflow `ci` зелёный.

---

### Task 10: Живое тестирование на роутере 192.168.1.1

Роутер тестовый (aarch64), доступ по SSH порт 222 (`sshpass`, пароль в памяти проекта `-Library-Zapret2`, файл `reference_router_ssh.md`). Обозначим `RSSH="sshpass -p '<пароль>' ssh -p 222 root@192.168.1.1"` и `RSCP` аналогично для scp.

**Files:** нет новых (используются Task 7–8).

- [ ] **Step 1: Проверить, что loopback принимается как name-server (ключевая оговорка спеки)**

```bash
$RSSH "ndmc -c 'show ip name-server'"      # снимок ДО
```
Записать текущий список (он же понадобится для отката).

- [ ] **Step 2: Собрать arm64-бинарник и скопировать на роутер**

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=live-test" -o /private/tmp/claude-501/-Library-DOQ/*/scratchpad/doqd-linux-arm64 ./cmd/doqd
$RSSH "mkdir -p /opt/tmp/keenetic-doq"
$RSCP <scratchpad>/doqd-linux-arm64 deploy/doqd.conf deploy/S56doqd deploy/install.sh deploy/uninstall.sh root@192.168.1.1:/opt/tmp/keenetic-doq/
```

- [ ] **Step 3: Установить**

```bash
$RSSH "cd /opt/tmp/keenetic-doq && sh install.sh --local ./doqd-linux-arm64"
```
Expected: `[keenetic-doq] готово`, без die.

- [ ] **Step 4: Прямой запрос в doqd на роутере**

```bash
$RSSH "opkg update >/dev/null; opkg install drill >/dev/null 2>&1; drill @127.0.0.1 -p 5353 example.com A" 
```
Expected: `rcode: NOERROR`, есть A-записи. Повторный запрос — быстрее (кеш).

- [ ] **Step 5: Проверить регистрацию и сквозной путь через ndnproxy :53**

```bash
$RSSH "ndmc -c 'show ip name-server'"      # должен появиться 127.0.0.1:5353
dig @192.168.1.1 doq-live-test-$RANDOM.example.com A   # с мака, свежее имя мимо кешей
dig @192.168.1.1 keenetic.com A +short                  # с мака
```
Expected: в списке name-server есть `127.0.0.1` порт `5353`; dig с мака получает ответы через штатный :53.

- [ ] **Step 6: Подтвердить, что запросы реально уходят по QUIC (udp/853)**

```bash
$RSSH "opkg install tcpdump >/dev/null 2>&1; timeout 15 tcpdump -ni any 'udp port 853' -c 4" &
sleep 2
dig @192.168.1.1 quic-proof-$RANDOM.example.org A
wait
```
Expected: tcpdump ловит UDP-пакеты к 94.140.x.x:853 (AdGuard) в момент запроса.

- [ ] **Step 7: Убедиться, что порт 53 не тронут и Z2K жив**

```bash
$RSSH "netstat -lnp | grep ':53 '"
```
Expected: на :53 по-прежнему ndnproxy (не doqd); doqd слушает только 127.0.0.1:5353.

- [ ] **Step 8: Перезагрузка — выживание**

```bash
$RSSH "ndmc -c 'system reboot'" || true
sleep 90
until $RSSH "echo up" 2>/dev/null; do sleep 10; done
$RSSH "/opt/etc/init.d/S56doqd check && ndmc -c 'show ip name-server' && drill @127.0.0.1 -p 5353 example.com A | grep -c NOERROR"
dig @192.168.1.1 reboot-proof-$RANDOM.example.com A
```
Expected: doqd alive, регистрация на месте, резолв работает.

- [ ] **Step 9: Зафиксировать результаты**

Записать вывод шагов 1–8 в `docs/superpowers/live-test-2026-08-24.md` (фактические выводы команд), закоммитить:
```bash
git add docs/superpowers/live-test-2026-08-24.md
git commit -m "docs: live test results on Keenetic aarch64

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```
Если loopback НЕ принялся в Step 5 — переключить `listen` на LAN-адрес роутера в `doqd.conf` и `NS` в скриптах, повторить Step 3–8, отразить в спеке.

---

### Task 11: README и релиз v0.1.0

**Files:**
- Create: `README.md`

- [ ] **Step 1: README**

`README.md` — по-русски, разделы: что это (DoQ рядом со штатными DoT/DoH, порт 53 не трогается), как работает (схема из спеки), установка (готовый релиз + `--local`), конфигурация (все ключи doqd.conf с дефолтами), проверка (drill/dig, tcpdump), удаление, сборка из исходников, ограничения (клиентская сторона DoQ, без фильтрации). Точные команды копировать из deploy/install.sh и Task 10.

- [ ] **Step 2: Commit + push + релиз**

```bash
git add README.md
git commit -m "docs: README

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
git push
git tag v0.1.0 && git push origin v0.1.0
gh run watch --exit-status
gh release view v0.1.0
```
Expected: release workflow зелёный, в релизе три бинарника `doqd-linux-{arm64,mipsle,mips}`.

---

## Self-Review (выполнен)

- Покрытие спеки: прокси (Task 3–7), кеш (2), несколько апстримов/failover/health (4), интеграция `ip name-server` (8, 10), init+respawn через rc.func (8), install/uninstall (8), CI/кросс-сборка (9), живые тесты включая tcpdump и ребут (10), README (11). Оговорка про loopback — Task 10 Step 1/9.
- Плейсхолдеров нет; весь код приведён.
- Сигнатуры сквозные: `Exchanger` (Task 4) используется в Task 5/7; `server.Resolver` (Task 6) удовлетворяется `resolver.Resolver` (Task 5); имена артефактов релиза (Task 9) совпадают с install.sh (Task 8).
