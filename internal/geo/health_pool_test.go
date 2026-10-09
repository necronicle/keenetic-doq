package geo

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func snapByAddr(h *Health) map[string]ProxyStatus {
	out := map[string]ProxyStatus{}
	for _, p := range h.Snapshot() {
		out[p.Addr] = p
	}
	return out
}

// Живой настоящий адрес гео-имени без подмены (x.ai у Cloudflare) не должен
// отключать правило «все прокси мертвы» и не считается прокси.
func TestHealthNonPoolExcludedFromAllDeadAndBad(t *testing.T) {
	ctx := context.Background()
	p := &fakeProber{}
	h := NewHealth(p)
	h.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "chatgpt.com"})
	h.Track(mustAddr("104.18.0.1"), "x.ai", false)
	h.Track(mustAddr("104.18.0.2"), "x.ai", false)
	p.setDead("1.1.1.1", true)
	p.setDead("104.18.0.2", true)
	h.CheckAll(ctx)
	h.CheckAll(ctx)
	if !h.AllDead() {
		t.Fatal("every pool address is dead; a live non-pool address must not mask it")
	}
	if bad := h.BadAddrs(); len(bad) != 1 || bad[0] != mustAddr("1.1.1.1") {
		t.Fatalf("only pool addresses are 'bad' for the cache flush, got %v", bad)
	}
	s := snapByAddr(h)
	if !s["1.1.1.1"].Pool || s["104.18.0.1"].Pool {
		t.Fatalf("pool flag wrong: %+v", s)
	}
}

func TestHealthPrunesStaleNonPool(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	h := NewHealth(&fakeProber{})
	h.now = func() time.Time { return now }
	h.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "chatgpt.com"})
	h.Track(mustAddr("104.18.0.1"), "x.ai", false)
	h.Track(mustAddr("104.18.0.2"), "x.ai", false)
	now = now.Add(5 * time.Minute)
	h.Track(mustAddr("104.18.0.2"), "x.ai", false) // снова в ответе
	now = now.Add(6 * time.Minute)
	h.CheckAll(context.Background())
	s := snapByAddr(h)
	if _, ok := s["104.18.0.1"]; ok {
		t.Fatal("a non-pool address unseen for 10 min must be pruned")
	}
	if _, ok := s["104.18.0.2"]; !ok {
		t.Fatal("a non-pool address seen 6 min ago must stay")
	}
	if _, ok := s["1.1.1.1"]; !ok {
		t.Fatal("pool addresses are never pruned")
	}
}

func TestHealthCapsTrackedAddresses(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	h := NewHealth(&fakeProber{})
	h.now = func() time.Time { return now }
	h.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "chatgpt.com"})
	for i := 0; i < 80; i++ {
		now = now.Add(time.Second)
		h.Track(netip.AddrFrom4([4]byte{10, 0, 0, byte(i)}), "x.ai", false)
	}
	s := snapByAddr(h)
	if len(s) != maxTracked {
		t.Fatalf("tracked = %d, want %d", len(s), maxTracked)
	}
	if _, ok := s["1.1.1.1"]; !ok {
		t.Fatal("the pool must not be evicted")
	}
	if _, ok := s["10.0.0.0"]; ok {
		t.Fatal("the oldest non-pool address goes first")
	}
	if _, ok := s["10.0.0.79"]; !ok {
		t.Fatal("the newest address must be tracked")
	}
}

// concProber считает одновременные пробы.
type concProber struct {
	mu       sync.Mutex
	cur, top int
}

func (p *concProber) Probe(ctx context.Context, addr netip.Addr, sni string) (time.Duration, error) {
	p.mu.Lock()
	p.cur++
	p.top = max(p.top, p.cur)
	p.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	p.mu.Lock()
	p.cur--
	p.mu.Unlock()
	return 50 * time.Millisecond, nil
}

func TestNewAddressChecksRespectParallelism(t *testing.T) {
	var ips []string
	for i := 0; i < 12; i++ {
		ips = append(ips, fmt.Sprintf("10.0.1.%d", i))
	}
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": ips})
	p := &concProber{}
	var servers = []Server{{URL: "g1", Ex: g1}}
	l := New(Config{Fast: newFake("fast", nil), Geo: servers, Prober: p})
	ask(t, l, "chatgpt.com", dns.TypeA)
	time.Sleep(300 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.top > maxParallelChecks {
		t.Fatalf("%d checks at once, limit %d", p.top, maxParallelChecks)
	}
}

func TestProbeDomainAnswersArePool(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"3.3.3.3"}, "x.ai.": {"104.18.0.1"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Covered: []string{"chatgpt.com"}}}
	ask(t, l, "chatgpt.com", dns.TypeA)
	ask(t, l, "x.ai", dns.TypeA)
	s := snapByAddr(l.health)
	if !s["3.3.3.3"].Pool || s["104.18.0.1"].Pool {
		t.Fatalf("probe-domain answers are pool, other geo names are not: %+v", s)
	}
}

// Все адреса типа плохие — фильтр отдаёт ответ как есть, и сброс из кеша дал
// бы только повторный запрос каждые 30 с (AAAA на роутере без IPv6).
func TestFlushBadLeavesAllBadAnswersAlone(t *testing.T) {
	p := &fakeProber{}
	g1 := newFake("g1", nil)
	l, rec := newTestLanes(t, newFake("fast", nil), p, g1)
	l.health.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "chatgpt.com", mustAddr("5.5.5.5"): "chatgpt.com",
		mustAddr("6.6.6.6"): "chatgpt.com"})
	p.setDead("1.1.1.1", true)
	p.setDead("5.5.5.5", true)
	ctx := context.Background()
	l.checkProxies(ctx)
	l.checkProxies(ctx)
	preds := rec.preds()
	if len(preds) == 0 {
		t.Fatal("dead pool addresses: expected a flush pass")
	}
	pred := preds[len(preds)-1]
	if pred(aMsg("chatgpt.com", "1.1.1.1", "5.5.5.5")) {
		t.Fatal("every address of the answer is dead: Filter serves it as is, flushing only churns")
	}
	if !pred(aMsg("chatgpt.com", "5.5.5.5", "6.6.6.6")) {
		t.Fatal("a dead address next to a healthy one must be flushed")
	}
}
