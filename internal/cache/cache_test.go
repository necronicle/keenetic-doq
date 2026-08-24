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
		rr, _ := dns.NewRR(dns.Fqdn(name) + " 0 IN A 1.2.3.4")
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
