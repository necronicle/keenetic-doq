// Package smartdns applies exact local hosts overrides outside the DNS cache.
package smartdns

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"github.com/miekg/dns"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed hosts.txt
var builtin []byte

// Builtin returns an independent copy of the shipped snapshot.
func Builtin() []byte { return bytes.Clone(builtin) }

// Table is an immutable, validated hosts snapshot.
type Table struct{ entries map[string][]netip.Addr }

func (t *Table) Count() int { return len(t.entries) }

// Load replaces, rather than extends, the builtin list when path is a file.
// Empty files are valid and deliberately disable all local overrides.
func Load(path string) (*Table, error) {
	data := builtin
	if path != "builtin" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	t := &Table{entries: make(map[string][]netip.Addr)}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		s, _, _ := strings.Cut(sc.Text(), "#")
		fields := strings.Fields(s)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return nil, fmt.Errorf("hosts line %d: expected IP and name", line)
		}
		addr, err := netip.ParseAddr(fields[0])
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("hosts line %d: invalid IP %q", line, fields[0])
		}
		addr = addr.Unmap()
		for _, raw := range fields[1:] {
			name := normalize(raw)
			if !validName(name) {
				return nil, fmt.Errorf("hosts line %d: invalid name %q", line, raw)
			}
			found := false
			for _, a := range t.entries[name] {
				if a == addr {
					found = true
					break
				}
			}
			if !found {
				t.entries[name] = append(t.entries[name], addr)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("hosts line %d: %w", line+1, err)
	}
	return t, nil
}
func normalize(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".") }
func validName(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

type nextResolver interface {
	Resolve(context.Context, *dns.Msg) (*dns.Msg, error)
}

// Resolver owns an atomically published snapshot. Local answers never enter the
// ordinary cache; downstream answers are checked again after in-flight work.
type Resolver struct {
	path   string
	next   nextResolver
	table  atomic.Pointer[Table]
	reload sync.Mutex
}

func New(path string, next nextResolver) (*Resolver, error) {
	t, e := Load(path)
	if e != nil {
		return nil, e
	}
	r := &Resolver{path: path, next: next}
	r.table.Store(t)
	return r, nil
}
func (r *Resolver) Reload() error {
	r.reload.Lock()
	defer r.reload.Unlock()
	t, e := Load(r.path)
	if e != nil {
		return e
	}
	r.table.Store(t)
	return nil
}
func localType(typ uint16) bool {
	switch typ {
	case dns.TypeA, dns.TypeAAAA, dns.TypeHTTPS, dns.TypeSVCB, dns.TypeANY, dns.TypeCNAME:
		return true
	}
	return false
}
func (t *Table) records(name string, typ uint16) []dns.RR {
	var out []dns.RR
	for _, ip := range t.entries[normalize(name)] {
		h := dns.RR_Header{Name: dns.Fqdn(name), Class: dns.ClassINET, Ttl: 60}
		if ip.Is4() && (typ == dns.TypeA || typ == dns.TypeANY) {
			h.Rrtype = dns.TypeA
			out = append(out, &dns.A{Hdr: h, A: ip.AsSlice()})
		}
		if ip.Is6() && (typ == dns.TypeAAAA || typ == dns.TypeANY) {
			h.Rrtype = dns.TypeAAAA
			out = append(out, &dns.AAAA{Hdr: h, AAAA: ip.AsSlice()})
		}
	}
	return out
}
func (t *Table) local(req *dns.Msg) *dns.Msg {
	q := req.Question[0]
	if q.Qclass != dns.ClassINET || !localType(q.Qtype) {
		return nil
	}
	if _, ok := t.entries[normalize(q.Name)]; !ok {
		return nil
	}
	m := new(dns.Msg)
	m.SetReply(req)
	m.RecursionAvailable = true
	m.Answer = t.records(q.Name, q.Qtype)
	if opt := req.IsEdns0(); opt != nil {
		m.SetEdns0(1232, opt.Do())
	}
	return m
}
func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	// Ambiguous multi-question packets must not bypass local policy.
	if len(req.Question) != 1 {
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeFormatError)
		return m, nil
	}
	if m := r.table.Load().local(req); m != nil {
		return m, nil
	}
	m, e := r.next.Resolve(ctx, req)
	t := r.table.Load()
	if local := t.local(req); local != nil {
		return local, nil
	}
	if e != nil {
		return nil, e
	}
	if m == nil {
		return nil, fmt.Errorf("upstream returned no DNS response")
	}
	return t.sanitize(req, m), nil
}

// sanitize also handles an unlisted alias that points into the local list.
// Work on a copy so cached messages remain ordinary upstream data on removal.
func (t *Table) sanitize(req, resp *dns.Msg) *dns.Msg {
	m := resp.Copy()
	changed := false
	q := req.Question[0]
	if q.Qclass != dns.ClassINET {
		return m
	}
	if localType(q.Qtype) {
		name := normalize(q.Name)
		var chain []dns.RR
		seen := map[string]bool{}
		for !seen[name] {
			seen[name] = true
			if _, ok := t.entries[name]; ok {
				m.Answer = append(chain, t.records(dns.Fqdn(name), q.Qtype)...)
				m.Rcode = dns.RcodeSuccess
				// The original authority/glue describes the discarded direct chain.
				m.Ns = nil
				var extra []dns.RR
				for _, rr := range m.Extra {
					if rr.Header().Rrtype == dns.TypeOPT {
						extra = append(extra, rr)
					}
				}
				m.Extra = extra
				changed = true
				break
			}
			var cn *dns.CNAME
			for _, rr := range m.Answer {
				if c, ok := rr.(*dns.CNAME); ok && normalize(c.Hdr.Name) == name {
					cn = c
					break
				}
			}
			if cn == nil {
				break
			}
			chain = append(chain, cn)
			name = normalize(cn.Target)
		}
	}
	filter := func(in []dns.RR) []dns.RR {
		var out []dns.RR
		for _, rr := range in {
			_, protected := t.entries[normalize(rr.Header().Name)]
			if protected && localType(rr.Header().Rrtype) {
				changed = true
				continue
			}
			out = append(out, rr)
		}
		return out
	}
	// Additional and authority sections must not smuggle protected addresses or hints.
	m.Ns = filter(m.Ns)
	m.Extra = filter(m.Extra)
	if changed {
		m.AuthenticatedData = false
		strip := func(in []dns.RR) []dns.RR {
			var out []dns.RR
			for _, rr := range in {
				switch rr.Header().Rrtype {
				case dns.TypeRRSIG, dns.TypeNSEC, dns.TypeNSEC3:
					continue
				}
				out = append(out, rr)
			}
			return out
		}
		m.Answer = strip(m.Answer)
		m.Ns = strip(m.Ns)
		m.Extra = strip(m.Extra)
	}
	return m
}
