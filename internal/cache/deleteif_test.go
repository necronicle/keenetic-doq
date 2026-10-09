package cache

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func putA(c *Cache, name string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	r := new(dns.Msg)
	r.SetReply(q)
	r.Answer = append(r.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.ParseIP("192.0.2.1"),
	})
	c.Put(KeyOf(q), r)
	return q
}

func TestDeleteIf(t *testing.T) {
	c := New(10, time.Minute, time.Hour)
	a := putA(c, "a.example.")
	b := putA(c, "b.example.")
	putA(c, "c.example.")
	n := c.DeleteIf(func(m *dns.Msg) bool { return m.Question[0].Name != "b.example." })
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	if c.Get(KeyOf(b)) == nil {
		t.Fatal("b.example must stay")
	}
	if c.GetStale(KeyOf(a)) != nil {
		t.Fatal("a.example must be gone, stale copy included")
	}
}
