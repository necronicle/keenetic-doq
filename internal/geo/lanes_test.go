package geo

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type flushRec struct {
	mu    sync.Mutex
	calls int
	ps    []func(*dns.Msg) bool
}

func (r *flushRec) flush(pred func(*dns.Msg) bool) int {
	r.mu.Lock()
	r.calls++
	r.ps = append(r.ps, pred)
	r.mu.Unlock()
	return 0
}

func (r *flushRec) preds() []func(*dns.Msg) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]func(*dns.Msg) bool(nil), r.ps...)
}

func (r *flushRec) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

func newTestLanes(t *testing.T, fast *fakeEx, prober *fakeProber, geos ...*fakeEx) (*Lanes, *flushRec) {
	t.Helper()
	var servers []Server
	for _, g := range geos {
		servers = append(servers, Server{URL: g.addr, Ex: g})
	}
	if prober == nil {
		prober = &fakeProber{}
	}
	rec := &flushRec{}
	dir := t.TempDir()
	l := New(Config{Fast: fast, Geo: servers, Prober: prober,
		StatePath: filepath.Join(dir, "geo.state"), SnapshotPath: filepath.Join(dir, "snap.json"),
		Flush: rec.flush})
	l.interval = time.Hour
	return l, rec
}

func ask(t *testing.T, l *Lanes, name string, qt uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qt)
	resp, err := l.Exchange(context.Background(), m)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return resp
}

func maxTTL(m *dns.Msg) uint32 {
	var top uint32
	for _, rr := range m.Answer {
		top = max(top, rr.Header().Ttl)
	}
	return top
}

func TestNoGeoIsPassthrough(t *testing.T) {
	fast := newFake("fast", map[string][]string{"chatgpt.com.": {"9.9.9.9"}})
	l, _ := newTestLanes(t, fast, nil)
	if got := ipsOf(ask(t, l, "chatgpt.com", dns.TypeA)); !reflect.DeepEqual(got, []string{"9.9.9.9"}) {
		t.Fatalf("got %v", got)
	}
	if fast.count() != 1 {
		t.Fatal("without geo servers every query goes to the fast lane")
	}
}

func TestStaticGeoGoesToPinnedOnly(t *testing.T) {
	fast := newFake("fast", map[string][]string{"chatgpt.com.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1"}})
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	l, _ := newTestLanes(t, fast, nil, g1, g2)
	if got := ipsOf(ask(t, l, "chatgpt.com", dns.TypeA)); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("got %v", got)
	}
	if fast.count() != 0 || g2.count() != 0 {
		t.Fatal("a geo name must reach only the pinned server")
	}
}

func TestUnknownNameInPoolLearnsGeo(t *testing.T) {
	fast := newFake("fast", map[string][]string{"new.example.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"new.example.": {"1.1.1.1"}})
	l, _ := newTestLanes(t, fast, nil, g1)
	l.prints.Set("g1", poolOf("1.1.1.1"))
	if got := ipsOf(ask(t, l, "new.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("got %v", got)
	}
	before := fast.count()
	ask(t, l, "new.example", dns.TypeA)
	if fast.count() != before {
		t.Fatal("a learned geo name must skip the fast lane")
	}
}

func TestUnknownPlainNameLearnsPlain(t *testing.T) {
	fast := newFake("fast", map[string][]string{"plain.example.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"plain.example.": {"7.7.7.7"}})
	l, _ := newTestLanes(t, fast, nil, g1)
	l.prints.Set("g1", poolOf("1.1.1.1"))
	if got := ipsOf(ask(t, l, "plain.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"7.7.7.7"}) {
		t.Fatalf("got %v", got)
	}
	before := g1.count()
	ask(t, l, "plain.example", dns.TypeA)
	if g1.count() != before {
		t.Fatal("a learned plain name must skip the geo server")
	}
}

func TestFastAnswerFromOtherPoolWaitsForPinned(t *testing.T) {
	fast := newFake("fast", map[string][]string{"x.example.": {"2.2.2.2"}})
	g1 := newFake("g1", map[string][]string{"x.example.": {"1.1.1.1"}})
	g1.setDelay(200 * time.Millisecond)
	l, _ := newTestLanes(t, fast, nil, g1)
	l.prints.Set("g1", poolOf("1.1.1.1"))
	l.prints.Set("g2", poolOf("2.2.2.2"))
	if got := ipsOf(ask(t, l, "x.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("another server's proxy must not leak through the fast lane, got %v", got)
	}
	if l.class.Lookup("x.example.") != Geo {
		t.Fatal("must be learned as geo")
	}
}

func TestSlowPinnedFallsBackToFastUnlearned(t *testing.T) {
	fast := newFake("fast", map[string][]string{"slow.example.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"slow.example.": {"7.7.7.7"}})
	g1.setDelay(2 * time.Second)
	l, _ := newTestLanes(t, fast, nil, g1)
	l.rtt = 50 * time.Millisecond // ожидание = 300 мс
	start := time.Now()
	if got := ipsOf(ask(t, l, "slow.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"7.7.7.7"}) {
		t.Fatalf("got %v", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the wait for the pinned server must be capped")
	}
	if l.class.Lookup("slow.example.") != Unknown {
		t.Fatal("an unchecked name must not be learned")
	}
}

func TestHTTPSForUnknownNameClassifiedByA(t *testing.T) {
	fast := newFake("fast", map[string][]string{"svc.example.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"svc.example.": {"1.1.1.1"}})
	l, _ := newTestLanes(t, fast, nil, g1)
	l.prints.Set("g1", poolOf("1.1.1.1"))
	resp := ask(t, l, "svc.example", dns.TypeHTTPS)
	if len(resp.Answer) != 1 || g1.countType(dns.TypeHTTPS) != 1 || fast.countType(dns.TypeHTTPS) != 0 {
		t.Fatal("HTTPS for a name that turned out geo must go to the pinned server only")
	}
}

func TestGeoHTTPSAnswerUntouched(t *testing.T) {
	g1 := newFake("g1", nil)
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	if resp := ask(t, l, "chatgpt.com", dns.TypeHTTPS); len(resp.Answer) != 1 {
		t.Fatalf("HTTPS answer must pass the proxy filter untouched: %v", resp.Answer)
	}
}

func TestPerQueryFailoverCapsTTL(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.setErr(errors.New("down"))
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	resp := ask(t, l, "chatgpt.com", dns.TypeA)
	if !reflect.DeepEqual(ipsOf(resp), []string{"2.2.2.2"}) || maxTTL(resp) > 60 {
		t.Fatalf("spare server answer with TTL<=60 expected, got %v ttl %d", ipsOf(resp), maxTTL(resp))
	}
}

func TestAllGeoDownNoStaleUsesFastShortTTL(t *testing.T) {
	fast := newFake("fast", map[string][]string{"chatgpt.com.": {"7.7.7.7"}})
	g1 := newFake("g1", nil)
	g1.setErr(errors.New("down"))
	l, _ := newTestLanes(t, fast, nil, g1)
	resp := ask(t, l, "chatgpt.com", dns.TypeA)
	if !reflect.DeepEqual(ipsOf(resp), []string{"7.7.7.7"}) || maxTTL(resp) > 60 {
		t.Fatalf("got %v ttl %d", ipsOf(resp), maxTTL(resp))
	}
}

func TestAllGeoDownWithStaleReturnsError(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.setErr(errors.New("down"))
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	l.cfg.Stale = func(*dns.Msg) *dns.Msg { return aMsg("chatgpt.com.", "1.1.1.1") }
	m := new(dns.Msg)
	m.SetQuestion("chatgpt.com.", dns.TypeA)
	if _, err := l.Exchange(context.Background(), m); !errors.Is(err, ErrGeoUnavailable) {
		t.Fatalf("stale answer exists — the resolver must serve it, got err %v", err)
	}
}

func TestDeadProxyFiltered(t *testing.T) {
	p := &fakeProber{}
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1", "1.1.1.2"}})
	l, _ := newTestLanes(t, newFake("fast", nil), p, g1)
	l.health.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "chatgpt.com", mustAddr("1.1.1.2"): "chatgpt.com"})
	p.setDead("1.1.1.2", true)
	l.health.CheckAll(context.Background())
	l.health.CheckAll(context.Background())
	if got := ipsOf(ask(t, l, "chatgpt.com", dns.TypeA)); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestGeoNXDOMAINIsAnAnswer(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.rcode["gone.openai.com."] = dns.RcodeNameError
	g2 := newFake("g2", nil)
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	if resp := ask(t, l, "gone.openai.com", dns.TypeA); resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d", resp.Rcode)
	}
	if g2.count() != 0 || l.Snapshot().Fails != 0 {
		t.Fatal("NXDOMAIN is an answer, not a failure")
	}
}

func TestGeoTTLCappedAt300(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	if ttl := maxTTL(ask(t, l, "chatgpt.com", dns.TypeA)); ttl > 300 {
		t.Fatalf("ttl = %d", ttl)
	}
}

func TestPoolGrowsOnlyFromProbeDomains(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"3.3.3.3"}, "x.ai.": {"104.18.0.1"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	ask(t, l, "chatgpt.com", dns.TypeA)
	ask(t, l, "x.ai", dns.TypeA)
	if !l.prints.MatchAny(aMsg("a.", "3.3.3.3")) {
		t.Fatal("a probe domain answer must extend the pinned pool (rotation)")
	}
	if l.prints.MatchAny(aMsg("a.", "104.18.0.1")) {
		t.Fatal("a non-probe geo name must not pull its addresses into the pool")
	}
}

func TestNewDeadAddressFlushed(t *testing.T) {
	p := &fakeProber{}
	p.setDead("5.5.5.5", true)
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1", "5.5.5.5"}})
	l, rec := newTestLanes(t, newFake("fast", nil), p, g1)
	ask(t, l, "chatgpt.com", dns.TypeA)
	waitFor(t, func() bool { return rec.count() > 0 }, 2*time.Second)
}

func TestThreeFailuresFailOver(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.setErr(errors.New("down"))
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	l, rec := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Alive: 1}, {URL: "g2", Coverage: 1, Alive: 1}}
	for i := 0; i < 3; i++ {
		ask(t, l, "chatgpt.com", dns.TypeA)
	}
	waitFor(t, func() bool { return l.Pinned() == "g2" }, 2*time.Second)
	st, err := LoadState(l.cfg.StatePath)
	if err != nil || st.Pinned != "g2" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if rec.count() == 0 {
		t.Fatal("geo answers must be flushed from the cache on a pin change")
	}
}

func TestFailureCountResetsOnSuccess(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1"}})
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Alive: 1}, {URL: "g2", Coverage: 1, Alive: 1}}
	for _, down := range []bool{true, true, false, true, true} {
		if down {
			g1.setErr(errors.New("down"))
		} else {
			g1.setErr(nil)
		}
		ask(t, l, "chatgpt.com", dns.TypeA)
	}
	time.Sleep(100 * time.Millisecond)
	if l.Pinned() != "g1" {
		t.Fatal("only three failures IN A ROW switch the server")
	}
}

func TestSnapshotWritten(t *testing.T) {
	g1 := newFake("g1", nil)
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	l.writeSnapshot()
	s, err := ReadSnapshot(l.cfg.SnapshotPath)
	if err != nil || s.Pinned != "g1" {
		t.Fatalf("snapshot = %+v, %v", s, err)
	}
}

func TestAAAAFirstThenAStillGeo(t *testing.T) {
	fast := newFake("fast", map[string][]string{"new.example.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"new.example.": {"1.1.1.1"}})
	l, _ := newTestLanes(t, fast, nil, g1)
	l.prints.Set("g1", poolOf("1.1.1.1"))
	ask(t, l, "new.example", dns.TypeAAAA)
	if got := ipsOf(ask(t, l, "new.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("A after AAAA must come from the pinned pool, got %v", got)
	}
	if l.class.Lookup("new.example.") != Geo {
		t.Fatal("must be geo")
	}
}

func TestParallelAAndAAAAEndGeo(t *testing.T) {
	fast := newFake("fast", map[string][]string{"new.example.": {"7.7.7.7"}})
	g1 := newFake("g1", map[string][]string{"new.example.": {"1.1.1.1"}})
	l, _ := newTestLanes(t, fast, nil, g1)
	l.prints.Set("g1", poolOf("1.1.1.1"))
	var wg sync.WaitGroup
	for _, qt := range []uint16{dns.TypeA, dns.TypeAAAA} {
		wg.Add(1)
		go func() { defer wg.Done(); ask(t, l, "new.example", qt) }()
	}
	wg.Wait()
	if l.class.Lookup("new.example.") != Geo {
		t.Fatal("must be geo")
	}
	if got := ipsOf(ask(t, l, "new.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("got %v", got)
	}
}
