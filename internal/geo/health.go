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
	fails   int
	samples []time.Duration
}

// Health — состояние адресов прокси закреплённого сервера.
type Health struct {
	prober TLSProber
	mu     sync.Mutex
	addrs  map[netip.Addr]*proxy
}

func NewHealth(p TLSProber) *Health { return &Health{prober: p, addrs: map[netip.Addr]*proxy{}} }

// Reset заменяет таблицу пулом нового закреплённого сервера; замеры адресов,
// которые есть и там и там, сохраняются.
func (h *Health) Reset(addrs map[netip.Addr]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := make(map[netip.Addr]*proxy, len(addrs))
	for a, sni := range addrs {
		if p := h.addrs[a]; p != nil {
			next[a] = p
		} else {
			next[a] = &proxy{sni: sni}
		}
	}
	h.addrs = next
}

// Track добавляет адрес; true — адрес новый.
func (h *Health) Track(a netip.Addr, sni string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.addrs[a]; ok {
		return false
	}
	h.addrs[a] = &proxy{sni: sni}
	return true
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
	rtt, err := h.prober.Probe(ctx, a, sni)
	h.record(a, rtt, err)
	return err
}

// CheckAll пробует все адреса, не больше maxParallelChecks одновременно.
func (h *Health) CheckAll(ctx context.Context) {
	h.mu.Lock()
	addrs := make([]netip.Addr, 0, len(h.addrs))
	for a := range h.addrs {
		addrs = append(addrs, a)
	}
	h.mu.Unlock()
	sem := make(chan struct{}, maxParallelChecks)
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
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

// bestLocked — лучшая медиана среди живых адресов с замерами.
func (h *Health) bestLocked() time.Duration {
	var best time.Duration
	for _, p := range h.addrs {
		if p.fails >= deadAfter || len(p.samples) == 0 {
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

// AllDead: таблица не пуста и все адреса в ней мертвы.
func (h *Health) AllDead() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.addrs) == 0 {
		return false
	}
	for _, p := range h.addrs {
		if p.fails < deadAfter {
			return false
		}
	}
	return true
}

// Filter убирает из копии ответа A/AAAA мёртвых и медленных прокси, если
// того же типа остаётся хоть один здоровый. Остальные записи не трогает.
func (h *Health) Filter(m *dns.Msg) *dns.Msg {
	out := m.Copy()
	h.mu.Lock()
	defer h.mu.Unlock()
	best := h.bestLocked()
	healthy := func(rr dns.RR) bool {
		var a netip.Addr
		switch v := rr.(type) {
		case *dns.A:
			a, _ = netip.AddrFromSlice(v.A.To4())
		case *dns.AAAA:
			a, _ = netip.AddrFromSlice(v.AAAA)
			a = a.Unmap()
		default:
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
}

func (h *Health) Snapshot() []ProxyStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	best := h.bestLocked()
	out := make([]ProxyStatus, 0, len(h.addrs))
	for a, p := range h.addrs {
		out = append(out, ProxyStatus{Addr: a.String(), State: h.stateLocked(p, best).String(),
			MedianMs: median(p.samples).Milliseconds(), Fails: p.fails})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}
