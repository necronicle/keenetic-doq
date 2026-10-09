package geo

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// aMsg — ответ с A-записями (TTL 600) на вопрос name A.
func aMsg(name string, ips ...string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), dns.TypeA)
	r := new(dns.Msg)
	r.SetReply(q)
	for _, ip := range ips {
		r.Answer = append(r.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 600},
			A:   net.ParseIP(ip),
		})
	}
	return r
}

// ipsOf — адреса A-записей ответа, отсортированные.
func ipsOf(m *dns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			out = append(out, a.A.String())
		}
	}
	sort.Strings(out)
	return out
}

func poolOf(ips ...string) *Pool {
	p := NewPool()
	p.Add(aMsg("pool.", ips...))
	return p
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func waitFor(t *testing.T, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// fakeEx — апстрим с заранее заданными ответами.
type fakeEx struct {
	addr  string
	mu    sync.Mutex
	ips   map[string][]string // FQDN в нижнем регистре → адреса A
	rcode map[string]int
	err   error
	// typeErr — ошибка только для этого типа запроса (geohide сбрасывает
	// поток на HTTPS/SVCB, а на A отвечает).
	typeErr map[uint16]error
	delay   time.Duration
	calls   []dns.Question
}

func newFake(addr string, ips map[string][]string) *fakeEx {
	return &fakeEx{addr: addr, ips: ips, rcode: map[string]int{}}
}

func (f *fakeEx) Address() string { return f.addr }

func (f *fakeEx) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

func (f *fakeEx) setDelay(d time.Duration) { f.mu.Lock(); f.delay = d; f.mu.Unlock() }

func (f *fakeEx) setTypeErr(t uint16, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.typeErr == nil {
		f.typeErr = map[uint16]error{}
	}
	f.typeErr[t] = err
}

func (f *fakeEx) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeEx) countType(t uint16) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.calls {
		if q.Qtype == t {
			n++
		}
	}
	return n
}

func (f *fakeEx) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.mu.Lock()
	f.calls = append(f.calls, m.Question[0])
	err, delay := f.err, f.delay
	if e, ok := f.typeErr[m.Question[0].Qtype]; ok && err == nil {
		err = e
	}
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	q := m.Question[0]
	name := strings.ToLower(q.Name)
	resp := new(dns.Msg)
	resp.SetReply(m)
	f.mu.Lock()
	defer f.mu.Unlock()
	if rc, ok := f.rcode[name]; ok {
		resp.Rcode = rc
		return resp, nil
	}
	switch q.Qtype {
	case dns.TypeA:
		for _, ip := range f.ips[name] {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 600},
				A:   net.ParseIP(ip),
			})
		}
	case dns.TypeHTTPS:
		resp.Answer = append(resp.Answer, &dns.HTTPS{SVCB: dns.SVCB{
			Hdr:      dns.RR_Header{Name: q.Name, Rrtype: dns.TypeHTTPS, Class: dns.ClassINET, Ttl: 600},
			Priority: 1, Target: ".",
			Value: []dns.SVCBKeyValue{
				&dns.SVCBAlpn{Alpn: []string{"h2"}},
				&dns.SVCBIPv4Hint{Hint: []net.IP{net.ParseIP("104.18.32.47").To4()}},
				&dns.SVCBIPv6Hint{Hint: []net.IP{net.ParseIP("2606:4700::6812:202f")}},
			},
		}})
	}
	return resp, nil
}

// fakeProber — TLS-проба без сети: задержка из lat (по умолчанию 100 мс),
// адреса из dead не отвечают.
type fakeProber struct {
	mu   sync.Mutex
	lat  map[string]time.Duration
	dead map[string]bool
}

func (p *fakeProber) setDead(addr string, dead bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead == nil {
		p.dead = map[string]bool{}
	}
	p.dead[addr] = dead
}

func (p *fakeProber) Probe(ctx context.Context, addr netip.Addr, sni string) (time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead[addr.String()] {
		return 0, context.DeadlineExceeded
	}
	if d, ok := p.lat[addr.String()]; ok {
		return d, nil
	}
	return 100 * time.Millisecond, nil
}
