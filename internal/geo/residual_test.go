package geo

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// SERVFAIL на пути классификации (A/AAAA/HTTPS одновременно) — не отказ закреплённого.
func TestServfailOnClassifyPathDoesNotFailOver(t *testing.T) {
	fast := newFake("fast", nil)
	fast.rcode["broken.example."] = dns.RcodeServerFailure
	g1 := newFake("g1", nil)
	g1.rcode["broken.example."] = dns.RcodeServerFailure
	g2 := newFake("g2", nil)
	l, _ := newTestLanes(t, fast, nil, g1, g2)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Alive: 1}, {URL: "g2", Coverage: 1, Alive: 1}}
	for _, qt := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeHTTPS} {
		m := new(dns.Msg)
		m.SetQuestion("broken.example.", qt)
		if _, err := l.Exchange(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if l.Pinned() != "g1" || l.Snapshot().Fails != 0 {
		t.Fatalf("SERVFAIL while classifying must not count: pinned %s, fails %d", l.Pinned(), l.Snapshot().Fails)
	}
}

func TestServfailOnGeoPathStillFailsOver(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.rcode["chatgpt.com."] = dns.RcodeServerFailure
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Alive: 1}, {URL: "g2", Coverage: 1, Alive: 1}}
	for i := 0; i < 3; i++ {
		ask(t, l, "chatgpt.com", dns.TypeA)
	}
	waitFor(t, func() bool { return l.Pinned() == "g2" }, 2*time.Second)
}

func TestUnverifiedFastAnswerTTLCapped(t *testing.T) {
	ips := map[string][]string{"slow.example.": {"7.7.7.7"}}
	// закреплённый молчит дольше ожидания
	g1 := newFake("g1", ips)
	g1.setDelay(2 * time.Second)
	l, _ := newTestLanes(t, newFake("fast", ips), nil, g1)
	l.rtt = 50 * time.Millisecond
	if ttl := maxTTL(ask(t, l, "slow.example", dns.TypeA)); ttl > degradedMaxTTL || ttl == 0 {
		t.Fatalf("ttl = %d, want 1..%d", ttl, degradedMaxTTL)
	}
	// закреплённый уже ошибается (fails > 0): ответ быстрой полосы сразу
	l.mu.Lock()
	l.fails = 1
	l.mu.Unlock()
	if ttl := maxTTL(ask(t, l, "slow.example", dns.TypeA)); ttl > degradedMaxTTL || ttl == 0 {
		t.Fatalf("failing: ttl = %d, want 1..%d", ttl, degradedMaxTTL)
	}
}

func TestLearningGeoFlushesThatNameOnly(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1", "5.5.5.5"}})
	p := &fakeProber{}
	l, rec := newTestLanes(t, newFake("fast", map[string][]string{"x.example.": {"5.5.5.5"}}), p, g1)
	l.prints.AddTo("g1", aMsg("probe.", "1.1.1.1", "5.5.5.5"))
	m := new(dns.Msg)
	m.SetQuestion("X.Example.", dns.TypeA)
	g1.ips["x.example."] = []string{"5.5.5.5"}
	if _, err := l.Exchange(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if l.class.Lookup("x.example.") != Geo {
		t.Fatal("name must be learned as geo")
	}
	preds := rec.preds()
	if len(preds) == 0 {
		t.Fatal("learning Geo must flush the cache")
	}
	mk := func(n string, qt uint16) *dns.Msg { r := new(dns.Msg); r.SetQuestion(n, qt); return r }
	p0 := preds[len(preds)-1]
	if !p0(mk("x.example.", dns.TypeHTTPS)) || !p0(mk("X.EXAMPLE.", dns.TypeA)) || p0(mk("y.example.", dns.TypeA)) {
		t.Fatal("predicate must match that name (any qtype, any case) and nothing else")
	}
}
