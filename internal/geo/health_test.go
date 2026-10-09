package geo

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestTLSProberHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	ap := netip.MustParseAddrPort(srv.Listener.Addr().String())
	p := tlsProber{timeout: 2 * time.Second, port: ap.Port()}
	if _, err := p.Probe(context.Background(), ap.Addr(), "chatgpt.com"); err != nil {
		t.Fatalf("handshake with a self-signed proxy must pass: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()
	p.port = closed.Port()
	if _, err := p.Probe(context.Background(), closed.Addr(), "chatgpt.com"); err == nil {
		t.Fatal("closed port must fail")
	}
}

func newHealthFixture() (*Health, *fakeProber) {
	p := &fakeProber{lat: map[string]time.Duration{
		"1.1.1.1": 165 * time.Millisecond,
		"1.1.1.2": 3100 * time.Millisecond,
		"1.1.1.3": 170 * time.Millisecond,
	}}
	h := NewHealth(p)
	h.Reset(map[netip.Addr]string{
		mustAddr("1.1.1.1"): "chatgpt.com",
		mustAddr("1.1.1.2"): "chatgpt.com",
		mustAddr("1.1.1.3"): "chatgpt.com",
	})
	return h, p
}

func TestHealthStates(t *testing.T) {
	ctx := context.Background()
	h, p := newHealthFixture()
	p.setDead("1.1.1.3", true)
	h.CheckAll(ctx)
	if h.State(mustAddr("1.1.1.3")) != Healthy {
		t.Fatal("one failure is not death")
	}
	h.CheckAll(ctx)
	if h.State(mustAddr("1.1.1.3")) != Dead {
		t.Fatal("two failures in a row = dead")
	}
	if h.State(mustAddr("1.1.1.2")) != Slow {
		t.Fatal("3.1 s against 165 ms = slow")
	}
	if h.State(mustAddr("1.1.1.1")) != Healthy {
		t.Fatal("165 ms = healthy")
	}
	if h.State(mustAddr("9.9.9.9")) != Healthy {
		t.Fatal("untracked address counts as healthy")
	}
	p.setDead("1.1.1.3", false)
	if err := h.Check(ctx, mustAddr("1.1.1.3")); err != nil {
		t.Fatal(err)
	}
	if h.State(mustAddr("1.1.1.3")) != Healthy {
		t.Fatal("one success revives")
	}
}

func TestHealthAllSlowIsNotSlow(t *testing.T) {
	p := &fakeProber{lat: map[string]time.Duration{"1.1.1.1": 1200 * time.Millisecond, "1.1.1.2": 1300 * time.Millisecond}}
	h := NewHealth(p)
	h.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "a", mustAddr("1.1.1.2"): "a"})
	h.CheckAll(context.Background())
	if h.State(mustAddr("1.1.1.2")) != Healthy {
		t.Fatal("slow only relative to the best of the pool")
	}
}

func TestHealthFilter(t *testing.T) {
	ctx := context.Background()
	h, p := newHealthFixture()
	p.setDead("1.1.1.3", true)
	h.CheckAll(ctx)
	h.CheckAll(ctx)
	got := ipsOf(h.Filter(aMsg("chatgpt.com.", "1.1.1.1", "1.1.1.2", "1.1.1.3", "9.9.9.9")))
	if !reflect.DeepEqual(got, []string{"1.1.1.1", "9.9.9.9"}) {
		t.Fatalf("filtered = %v", got)
	}
	got = ipsOf(h.Filter(aMsg("chatgpt.com.", "1.1.1.2", "1.1.1.3")))
	if !reflect.DeepEqual(got, []string{"1.1.1.2", "1.1.1.3"}) {
		t.Fatalf("all bad must pass unchanged, got %v", got)
	}
	m := aMsg("chatgpt.com.", "1.1.1.1", "1.1.1.3")
	m.Answer = append(m.Answer, &dns.CNAME{
		Hdr:    dns.RR_Header{Name: "chatgpt.com.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60},
		Target: "x.example.",
	})
	out := h.Filter(m)
	if len(out.Answer) != 2 {
		t.Fatalf("CNAME must stay, dead A must go: %v", out.Answer)
	}
	if len(m.Answer) != 3 {
		t.Fatal("Filter must not modify its input")
	}
}

func TestHealthAllDead(t *testing.T) {
	ctx := context.Background()
	p := &fakeProber{}
	h := NewHealth(p)
	if h.AllDead() {
		t.Fatal("empty table is not 'all dead'")
	}
	h.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "a"})
	p.setDead("1.1.1.1", true)
	h.CheckAll(ctx)
	h.CheckAll(ctx)
	if !h.AllDead() {
		t.Fatal("the only address is dead")
	}
	if h.Track(mustAddr("1.1.1.1"), "a", true) {
		t.Fatal("Track of a known address must return false")
	}
	if !h.Track(mustAddr("2.2.2.2"), "a", true) || h.AllDead() {
		t.Fatal("new address is untested, so not all dead")
	}
	snap := h.Snapshot()
	if len(snap) != 2 || snap[0].Addr != "1.1.1.1" || snap[0].State != "dead" || snap[0].Fails != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestHealthBadAddrs(t *testing.T) {
	ctx := context.Background()
	h, p := newHealthFixture()
	if got := h.BadAddrs(); len(got) != 0 {
		t.Fatalf("nothing checked yet, got %v", got)
	}
	p.setDead("1.1.1.3", true)
	h.CheckAll(ctx)
	h.CheckAll(ctx)
	got := map[netip.Addr]bool{}
	for _, a := range h.BadAddrs() {
		got[a] = true
	}
	if len(got) != 2 || !got[mustAddr("1.1.1.3")] || !got[mustAddr("1.1.1.2")] {
		t.Fatalf("want dead 1.1.1.3 and slow 1.1.1.2, got %v", got)
	}
}
