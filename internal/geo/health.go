package geo

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	deadAfter         = 2           // неудач подряд — мёртвый
	slowAbs           = time.Second // медленный: медиана больше этого
	slowFactor        = 3           // …и больше лучшей в пуле во столько раз
	samplesKept       = 5           // замеров на адрес
	maxParallelChecks = 4
	maxTracked        = 64               // всего адресов в таблице; пул не вытесняется
	nonPoolTTL        = 10 * time.Minute // адрес не из пула, не виденный столько, забывается
)

// TLSProber меряет TLS-рукопожатие с прокси.
type TLSProber interface {
	Probe(ctx context.Context, addr netip.Addr, sni string) (time.Duration, error)
}

type tlsProber struct {
	timeout time.Duration
	port    uint16
}

// NewTLSProber — проба TCP+TLS на :443.
func NewTLSProber(timeout time.Duration) TLSProber { return tlsProber{timeout: timeout, port: 443} }

func (p tlsProber) Probe(ctx context.Context, addr netip.Addr, sni string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(addr, p.port).String())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	// Сертификат не проверяется: прокси вправе отдавать свой. Важно только,
	// что рукопожатие проходит и за сколько.
	tc := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: true,
		NextProtos: []string{"h2", "http/1.1"}})
	if err := tc.HandshakeContext(ctx); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

type ProxyState int

const (
	Healthy ProxyState = iota
	Slow
	Dead
)

func (s ProxyState) String() string {
	switch s {
	case Slow:
		return "slow"
	case Dead:
		return "dead"
	}
	return "healthy"
}

type proxy struct {
	sni     string
	pool    bool      // прокси пула закреплённого (рейтинг, ответы на пробные домены)
	seen    time.Time // когда адрес последний раз был в ответе
	fails   int
	samples []time.Duration
}

// Health — состояние адресов закреплённого сервера: его пула прокси и прочих
// адресов из его ответов (гео-имена, которые сервер не подменяет, — там
// настоящие адреса). Правило «все прокси мертвы», сброс кеша по плохим
// адресам и счётчики в CLI смотрят только на пул; прочие адреса только
// фильтруются в ответах и забываются через 10 минут без появления.
type Health struct {
	prober TLSProber
	now    func() time.Time
	sem    chan struct{} // общий предел проверок: цикл и проверки новых адресов
	mu     sync.Mutex
	addrs  map[netip.Addr]*proxy
}

func NewHealth(p TLSProber) *Health {
	return &Health{prober: p, now: time.Now, sem: make(chan struct{}, maxParallelChecks),
		addrs: map[netip.Addr]*proxy{}}
}

// Reset заменяет таблицу пулом нового закреплённого сервера; замеры адресов,
// которые есть и там и там, сохраняются.
func (h *Health) Reset(addrs map[netip.Addr]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	next := make(map[netip.Addr]*proxy, len(addrs))
	for a, sni := range addrs {
		p := h.addrs[a]
		if p == nil {
			p = &proxy{sni: sni}
		}
		p.pool, p.seen = true, now
		next[a] = p
	}
	h.addrs = next
}

// Track отмечает адрес из ответа; pool — ответ на пробный домен. true — адрес
// новый и его надо проверить. Таблица не растёт больше maxTracked: место
// освобождает самый давний адрес не из пула; если таких нет, новый адрес не
// из пула не отслеживается.
func (h *Health) Track(a netip.Addr, sni string, pool bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if p, ok := h.addrs[a]; ok {
		p.seen = now
		if pool && !p.pool {
			p.pool, p.sni = true, sni
		}
		return false
	}
	if len(h.addrs) >= maxTracked && !h.evictLocked() && !pool {
		return false
	}
	h.addrs[a] = &proxy{sni: sni, pool: pool, seen: now}
	return true
}

// evictLocked убирает самый давний адрес не из пула; false — таких нет.
func (h *Health) evictLocked() bool {
	var oldest netip.Addr
	var at time.Time
	for a, p := range h.addrs {
		if !p.pool && (!oldest.IsValid() || p.seen.Before(at)) {
			oldest, at = a, p.seen
		}
	}
	if !oldest.IsValid() {
		return false
	}
	delete(h.addrs, oldest)
	return true
}

// pruneLocked забывает адреса не из пула, давно не встречавшиеся в ответах.
func (h *Health) pruneLocked() {
	cut := h.now().Add(-nonPoolTTL)
	for a, p := range h.addrs {
		if !p.pool && p.seen.Before(cut) {
			delete(h.addrs, a)
		}
	}
}

// Check — одна проба адреса.
func (h *Health) Check(ctx context.Context, a netip.Addr) error {
	h.mu.Lock()
	p := h.addrs[a]
	sni := ""
	if p != nil {
		sni = p.sni
	}
	h.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%s is not tracked", a)
	}
	select {
	case h.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err() // не дождались очереди — это не неудача адреса
	}
	defer func() { <-h.sem }()
	rtt, err := h.prober.Probe(ctx, a, sni)
	h.record(a, rtt, err)
	return err
}

// CheckAll забывает давние адреса не из пула и пробует остальные; одновременно
// не больше maxParallelChecks проб (предел общий с Check).
func (h *Health) CheckAll(ctx context.Context) {
	h.mu.Lock()
	h.pruneLocked()
	addrs := make([]netip.Addr, 0, len(h.addrs))
	for a := range h.addrs {
		addrs = append(addrs, a)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Check(ctx, a)
		}()
	}
	wg.Wait()
}

func (h *Health) record(a netip.Addr, rtt time.Duration, err error) {
	h.mu.Lock()
	p := h.addrs[a]
	if p == nil {
		h.mu.Unlock()
		return
	}
	wasDead := p.fails >= deadAfter
	if err != nil {
		p.fails++
	} else {
		p.fails = 0
		p.samples = append(p.samples, rtt)
		if len(p.samples) > samplesKept {
			p.samples = p.samples[len(p.samples)-samplesKept:]
		}
	}
	isDead := p.fails >= deadAfter
	h.mu.Unlock()
	switch {
	case !wasDead && isDead:
		slog.Info("geo: proxy is dead", "addr", a, "err", err)
	case wasDead && !isDead:
		slog.Info("geo: proxy is alive again", "addr", a)
	}
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// bestLocked — лучшая медиана среди живых адресов пула с замерами.
func (h *Health) bestLocked() time.Duration {
	var best time.Duration
	for _, p := range h.addrs {
		if !p.pool || p.fails >= deadAfter || len(p.samples) == 0 {
			continue
		}
		if m := median(p.samples); best == 0 || m < best {
			best = m
		}
	}
	return best
}

func (h *Health) stateLocked(p *proxy, best time.Duration) ProxyState {
	if p.fails >= deadAfter {
		return Dead
	}
	if m := median(p.samples); m > slowAbs && best > 0 && m > slowFactor*best {
		return Slow
	}
	return Healthy
}

// State: неизвестный адрес считается здоровым — его ещё не проверяли.
func (h *Health) State(a netip.Addr) ProxyState {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.addrs[a]
	if p == nil {
		return Healthy
	}
	return h.stateLocked(p, h.bestLocked())
}

// BadAddrs — адреса пула, которые сейчас мертвы или медленны (то же правило,
// что у State).
func (h *Health) BadAddrs() []netip.Addr {
	h.mu.Lock()
	defer h.mu.Unlock()
	best := h.bestLocked()
	var bad []netip.Addr
	for a, p := range h.addrs {
		if p.pool && h.stateLocked(p, best) != Healthy {
			bad = append(bad, a)
		}
	}
	return bad
}

// Refilter — предикат для сброса из кеша: ответ содержит адрес из target и
// рядом с ним хотя бы один здоровый адрес того же типа, то есть Filter сейчас
// его бы изменил. Ответ, где плохи все адреса типа, Filter отдаёт как есть —
// его сброс дал бы только лишний запрос. Состояния снимаются один раз.
func (h *Health) Refilter(target []netip.Addr) func(*dns.Msg) bool {
	tgt := make(map[netip.Addr]bool, len(target))
	for _, a := range target {
		tgt[a] = true
	}
	h.mu.Lock()
	best := h.bestLocked()
	unhealthy := map[netip.Addr]bool{}
	for a, p := range h.addrs {
		if h.stateLocked(p, best) != Healthy {
			unhealthy[a] = true
		}
	}
	h.mu.Unlock()
	return func(m *dns.Msg) bool {
		var hit, good [2]bool // [A, AAAA]
		for _, rr := range m.Answer {
			a, i := rrAddr(rr)
			if !a.IsValid() {
				continue
			}
			hit[i] = hit[i] || tgt[a]
			good[i] = good[i] || !unhealthy[a]
		}
		return (hit[0] && good[0]) || (hit[1] && good[1])
	}
}

// rrAddr — адрес A/AAAA-записи и индекс типа (0 — A, 1 — AAAA).
func rrAddr(rr dns.RR) (netip.Addr, int) {
	switch v := rr.(type) {
	case *dns.A:
		a, _ := netip.AddrFromSlice(v.A.To4())
		return a, 0
	case *dns.AAAA:
		a, _ := netip.AddrFromSlice(v.AAAA)
		return a.Unmap(), 1
	}
	return netip.Addr{}, 0
}

// AllDead: в пуле есть адреса, и все они мертвы.
func (h *Health) AllDead() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.addrs {
		if !p.pool {
			continue
		}
		n++
		if p.fails < deadAfter {
			return false
		}
	}
	return n > 0
}

// Filter убирает из копии ответа A/AAAA мёртвых и медленных прокси, если
// того же типа остаётся хоть один здоровый. Остальные записи не трогает.
func (h *Health) Filter(m *dns.Msg) *dns.Msg {
	out := m.Copy()
	h.mu.Lock()
	defer h.mu.Unlock()
	best := h.bestLocked()
	healthy := func(rr dns.RR) bool {
		a, _ := rrAddr(rr)
		if !a.IsValid() {
			return true
		}
		p := h.addrs[a]
		return p == nil || h.stateLocked(p, best) == Healthy
	}
	for _, t := range []uint16{dns.TypeA, dns.TypeAAAA} {
		keep, total := 0, 0
		for _, rr := range out.Answer {
			if rr.Header().Rrtype == t {
				total++
				if healthy(rr) {
					keep++
				}
			}
		}
		if keep == 0 || keep == total {
			continue
		}
		var filtered []dns.RR
		for _, rr := range out.Answer {
			if rr.Header().Rrtype == t && !healthy(rr) {
				continue
			}
			filtered = append(filtered, rr)
		}
		out.Answer = filtered
	}
	return out
}

type ProxyStatus struct {
	Addr     string `json:"addr"`
	State    string `json:"state"`
	MedianMs int64  `json:"median_ms"`
	Fails    int    `json:"fails"`
	Pool     bool   `json:"pool"`
}

func (h *Health) Snapshot() []ProxyStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	best := h.bestLocked()
	out := make([]ProxyStatus, 0, len(h.addrs))
	for a, p := range h.addrs {
		out = append(out, ProxyStatus{Addr: a.String(), State: h.stateLocked(p, best).String(),
			MedianMs: median(p.samples).Milliseconds(), Fails: p.fails, Pool: p.pool})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}
