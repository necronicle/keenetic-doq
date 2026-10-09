package geo

import (
	"net/netip"
	"sort"
	"sync"

	"github.com/miekg/dns"
)

// answerAddrs — адреса A/AAAA и цели CNAME из секции ответа.
func answerAddrs(m *dns.Msg) (ips []netip.Addr, cnames []string) {
	if m == nil {
		return nil, nil
	}
	for _, rr := range m.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if a, ok := netip.AddrFromSlice(v.A.To4()); ok {
				ips = append(ips, a)
			}
		case *dns.AAAA:
			if a, ok := netip.AddrFromSlice(v.AAAA); ok {
				ips = append(ips, a.Unmap())
			}
		case *dns.CNAME:
			cnames = append(cnames, normalize(v.Target))
		}
	}
	return ips, cnames
}

// Pool — отпечаток пула прокси одного сервера обхода: адреса и цели CNAME,
// которые он отдаёт вместо настоящих адресов геоблокированных сервисов.
type Pool struct {
	IPs    map[netip.Addr]struct{}
	CNAMEs map[string]struct{}
}

func NewPool() *Pool {
	return &Pool{IPs: map[netip.Addr]struct{}{}, CNAMEs: map[string]struct{}{}}
}

func (p *Pool) Add(m *dns.Msg) {
	ips, cnames := answerAddrs(m)
	for _, a := range ips {
		p.IPs[a] = struct{}{}
	}
	for _, c := range cnames {
		p.CNAMEs[c] = struct{}{}
	}
}

// Matches: в ответе есть адрес или цель CNAME из пула.
func (p *Pool) Matches(m *dns.Msg) bool {
	ips, cnames := answerAddrs(m)
	for _, a := range ips {
		if _, ok := p.IPs[a]; ok {
			return true
		}
	}
	for _, c := range cnames {
		if _, ok := p.CNAMEs[c]; ok {
			return true
		}
	}
	return false
}

func (p *Pool) lists() (ips, cnames []string) {
	for a := range p.IPs {
		ips = append(ips, a.String())
	}
	for c := range p.CNAMEs {
		cnames = append(cnames, c)
	}
	sort.Strings(ips)
	sort.Strings(cnames)
	return ips, cnames
}

// Fingerprints — отпечатки пулов всех серверов обхода по их URL.
type Fingerprints struct {
	mu    sync.RWMutex
	pools map[string]*Pool
}

func NewFingerprints() *Fingerprints { return &Fingerprints{pools: map[string]*Pool{}} }

func (f *Fingerprints) Set(server string, p *Pool) {
	f.mu.Lock()
	f.pools[server] = p
	f.mu.Unlock()
}

func (f *Fingerprints) AddTo(server string, m *dns.Msg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pools[server]
	if p == nil {
		p = NewPool()
		f.pools[server] = p
	}
	p.Add(m)
}

// MatchAny: ответ попал в пул хоть одного сервера обхода.
func (f *Fingerprints) MatchAny(m *dns.Msg) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, p := range f.pools {
		if p.Matches(m) {
			return true
		}
	}
	return false
}
