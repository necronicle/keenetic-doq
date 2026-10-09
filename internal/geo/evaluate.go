package geo

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

// Server — сервер обхода из конфига.
type Server struct {
	URL string
	Ex  upstream.Exchanger
}

// Result — итог оценки одного сервера обхода.
type Result struct {
	URL         string            `json:"url"`
	Coverage    int               `json:"coverage"` // пробных доменов с подменой и живым прокси
	MedianTLSMs int64             `json:"median_tls_ms"`
	Alive       int               `json:"alive"`
	Total       int               `json:"total"`
	PoolIPs     []string          `json:"pool_ips"`
	PoolCNAMEs  []string          `json:"pool_cnames"`
	SNI         map[string]string `json:"sni"`     // адрес → пробный домен, для проверок здоровья
	Covered     []string          `json:"covered"` // пробные домены с подменой и живым прокси
	Err         string            `json:"err,omitempty"`
}

// Pool восстанавливает отпечаток пула из сохранённого результата.
func (r Result) Pool() *Pool {
	p := NewPool()
	for _, s := range r.PoolIPs {
		if a, err := netip.ParseAddr(s); err == nil {
			p.IPs[a] = struct{}{}
		}
	}
	for _, c := range r.PoolCNAMEs {
		p.CNAMEs[c] = struct{}{}
	}
	return p
}

// Evaluator оценивает серверы обхода по живости и скорости их прокси.
type Evaluator struct {
	Reference upstream.Exchanger // обычный резолвер для проверки подмены; nil — без проверки
	Prober    TLSProber
	Probes    []string
	Attempts  int
	Timeout   time.Duration
}

func (e *Evaluator) Run(ctx context.Context, servers []Server) []Result {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	refs := e.references(ctx)
	results := make([]Result, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.evalOne(ctx, s, refs)
		}()
	}
	wg.Wait()
	order := make([]string, len(servers))
	for i, s := range servers {
		order[i] = s.URL
	}
	return Rank(results, order)
}

func queryA(ctx context.Context, ex upstream.Exchanger, name string) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := ex.Exchange(cctx, m)
	if err == nil && resp == nil {
		err = fmt.Errorf("%s: no response", name)
	}
	if err == nil && (resp.Rcode == dns.RcodeServerFailure || resp.Rcode == dns.RcodeRefused) {
		err = fmt.Errorf("%s answered %s", name, dns.RcodeToString[resp.Rcode])
	}
	return resp, err
}

// references — настоящие адреса пробных доменов по мнению обычного резолвера.
// Домен, по которому резолвер не ответил, в карте отсутствует.
func (e *Evaluator) references(ctx context.Context) map[string][]netip.Addr {
	out := map[string][]netip.Addr{}
	if e.Reference == nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range e.Probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := queryA(ctx, e.Reference, p)
			if err != nil {
				return
			}
			ips, _ := answerAddrs(resp)
			if len(ips) == 0 {
				return // пустой ответ ничего не доказывает
			}
			mu.Lock()
			out[p] = ips
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func overlaps(a, b []netip.Addr) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (e *Evaluator) evalOne(ctx context.Context, s Server, refs map[string][]netip.Addr) Result {
	r := Result{URL: s.URL, SNI: map[string]string{}}
	pool := NewPool()
	alive := map[netip.Addr]time.Duration{}
	tested := map[netip.Addr]bool{}
	var lastErr error
	// Пробные домены спрашиваются параллельно (их больше трёх, а предел
	// оценки общий); обрабатываются затем по порядку — результат детерминирован.
	type answer struct {
		resp *dns.Msg
		err  error
	}
	answers := make([]answer, len(e.Probes))
	var qwg sync.WaitGroup
	for i, probe := range e.Probes {
		qwg.Add(1)
		go func() {
			defer qwg.Done()
			answers[i].resp, answers[i].err = queryA(ctx, s.Ex, probe)
		}()
	}
	qwg.Wait()
	for pi, probe := range e.Probes {
		resp, err := answers[pi].resp, answers[pi].err
		if err != nil {
			lastErr = err
			continue
		}
		ips, _ := answerAddrs(resp)
		// Резолвер задан, но по этому домену не ответил: подмену не проверить,
		// домен не засчитывается никому (иначе настоящий адрес сошёл бы за прокси).
		if e.Reference != nil {
			if _, ok := refs[probe]; !ok {
				continue
			}
		}
		// Совпало с обычным резолвером — сервер этот сервис не обходит.
		if len(ips) == 0 || overlaps(ips, refs[probe]) {
			continue
		}
		pool.Add(resp)
		sni := normalize(probe)
		var fresh []netip.Addr
		for _, a := range ips {
			if !tested[a] {
				tested[a] = true
				r.SNI[a.String()] = sni
				fresh = append(fresh, a)
			}
		}
		for a, d := range e.measure(ctx, fresh, sni) {
			alive[a] = d
		}
		for _, a := range ips {
			if _, ok := alive[a]; ok {
				r.Coverage++
				r.Covered = append(r.Covered, sni)
				break
			}
		}
	}
	r.Total, r.Alive = len(tested), len(alive)
	if len(alive) > 0 {
		ds := make([]time.Duration, 0, len(alive))
		for _, d := range alive {
			ds = append(ds, d)
		}
		r.MedianTLSMs = max(median(ds).Milliseconds(), 1)
	}
	r.PoolIPs, r.PoolCNAMEs = pool.lists()
	if r.Total == 0 && lastErr != nil {
		r.Err = lastErr.Error()
	}
	return r
}

// measure — медиана TLS по Attempts попыткам для каждого адреса; адреса
// пробуются параллельно, чтобы мёртвые не съели общий предел оценки.
func (e *Evaluator) measure(ctx context.Context, addrs []netip.Addr, sni string) map[netip.Addr]time.Duration {
	out := map[netip.Addr]time.Duration{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var ok []time.Duration
			for i := 0; i < e.Attempts; i++ {
				if d, err := e.Prober.Probe(ctx, a, sni); err == nil {
					ok = append(ok, d)
				}
			}
			if len(ok) > 0 {
				mu.Lock()
				out[a] = median(ok)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

// Rank: сначала охват, затем медиана TLS; серверы в пределах 20 % от лучшей
// медианы своей группы считаются равными и идут в порядке конфига.
func Rank(rs []Result, configOrder []string) []Result {
	idx := map[string]int{}
	for i, u := range configOrder {
		idx[u] = i
	}
	out := append([]Result(nil), rs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Coverage != out[j].Coverage {
			return out[i].Coverage > out[j].Coverage
		}
		return idx[out[i].URL] < idx[out[j].URL]
	})
	for start := 0; start < len(out); {
		end := start
		for end < len(out) && out[end].Coverage == out[start].Coverage {
			end++
		}
		rankGroup(out[start:end], idx)
		start = end
	}
	return out
}

func rankGroup(g []Result, idx map[string]int) {
	var best int64
	for _, r := range g {
		if r.MedianTLSMs > 0 && (best == 0 || r.MedianTLSMs < best) {
			best = r.MedianTLSMs
		}
	}
	tie := func(r Result) bool { return best > 0 && r.MedianTLSMs > 0 && r.MedianTLSMs*5 <= best*6 }
	sort.SliceStable(g, func(i, j int) bool {
		a, b := g[i], g[j]
		if ta, tb := tie(a), tie(b); ta != tb {
			return ta
		} else if ta {
			return idx[a.URL] < idx[b.URL]
		}
		if (a.MedianTLSMs > 0) != (b.MedianTLSMs > 0) {
			return a.MedianTLSMs > 0
		}
		if a.MedianTLSMs != b.MedianTLSMs {
			return a.MedianTLSMs < b.MedianTLSMs
		}
		return idx[a.URL] < idx[b.URL]
	})
}
