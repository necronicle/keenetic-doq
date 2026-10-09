package geo

import (
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// addrHints — ipv4hint/ipv6hint во всех HTTPS/SVCB-записях ответа.
func addrHints(m *dns.Msg) int {
	n := 0
	for _, rr := range append(append([]dns.RR(nil), m.Answer...), m.Extra...) {
		var kv []dns.SVCBKeyValue
		switch v := rr.(type) {
		case *dns.HTTPS:
			kv = v.Value
		case *dns.SVCB:
			kv = v.Value
		}
		for _, x := range kv {
			if x.Key() == dns.SVCB_IPV4HINT || x.Key() == dns.SVCB_IPV6HINT {
				n++
			}
		}
	}
	return n
}

func TestHTTPSResetsDoNotFailOver(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1"}})
	g1.setTypeErr(dns.TypeHTTPS, errors.New("stream reset, error code 5"))
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Alive: 1}, {URL: "g2", Coverage: 1, Alive: 1}}
	for i := 0; i < 3; i++ {
		ask(t, l, "chatgpt.com", dns.TypeHTTPS)
	}
	time.Sleep(100 * time.Millisecond)
	if l.Pinned() != "g1" || l.Snapshot().Fails != 0 {
		t.Fatalf("HTTPS resets must not count toward failover: pinned %s, fails %d", l.Pinned(), l.Snapshot().Fails)
	}
}

func TestGeoHTTPSHintsStripped(t *testing.T) {
	g1 := newFake("g1", nil)
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1)
	resp := ask(t, l, "chatgpt.com", dns.TypeHTTPS)
	if len(resp.Answer) != 1 || addrHints(resp) != 0 {
		t.Fatalf("HTTPS for a geo name must lose ipv4hint/ipv6hint: %v", resp.Answer)
	}
	if v := resp.Answer[0].(*dns.HTTPS).Value; len(v) != 1 || v[0].Key() != dns.SVCB_ALPN {
		t.Fatalf("other SvcParams must stay: %v", v)
	}
}

func TestGeoHTTPSFromSpareHintsStripped(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.setTypeErr(dns.TypeHTTPS, errors.New("reset"))
	g2 := newFake("g2", nil)
	l, _ := newTestLanes(t, newFake("fast", nil), nil, g1, g2)
	resp := ask(t, l, "chatgpt.com", dns.TypeHTTPS)
	if len(resp.Answer) != 1 || addrHints(resp) != 0 || g2.countType(dns.TypeHTTPS) != 1 {
		t.Fatalf("spare HTTPS answer must lose address hints: %v", resp.Answer)
	}
}

func TestGeoHTTPSAllGeoFailIsNoData(t *testing.T) {
	fast := newFake("fast", nil)
	g1 := newFake("g1", nil)
	g1.setTypeErr(dns.TypeHTTPS, errors.New("reset"))
	g2 := newFake("g2", nil)
	g2.setTypeErr(dns.TypeHTTPS, errors.New("reset"))
	l, _ := newTestLanes(t, fast, nil, g1, g2)
	resp := ask(t, l, "chatgpt.com", dns.TypeHTTPS)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("NODATA expected, got rcode %d answer %v", resp.Rcode, resp.Answer)
	}
	if fast.count() != 0 {
		t.Fatal("HTTPS for a geo name must never reach the fast lane: its hints carry the real address")
	}
}

func TestSoftFailOnAFailsOver(t *testing.T) {
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
