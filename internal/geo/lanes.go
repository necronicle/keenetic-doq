package geo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

const (
	geoAttemptTimeout    = 3 * time.Second
	geoMaxTTL            = 300 // фильтр прокси должен доходить и до кешей устройств
	degradedMaxTTL       = 60  // ответ запасного сервера или быстрой полосы вместо гео
	minClassifyWait      = 300 * time.Millisecond
	maxClassifyWait      = time.Second
	failsToFailover      = 3
	deadCyclesToFailover = 3
	checkInterval        = 30 * time.Second
	evaluationTimeout    = 15 * time.Second
)

// ErrGeoUnavailable: все серверы обхода молчат, а в кеше есть устаревший
// ответ — его отдаст resolver (RFC 8767).
var ErrGeoUnavailable = errors.New("every geo-unblocking server failed")

type Config struct {
	Fast         upstream.Exchanger // быстрая полоса: upstream + geo, быстрейший
	Geo          []Server           // серверы обхода в порядке конфига
	Reference    upstream.Exchanger // обычный резолвер для оценки; nil — без проверки подмены
	Static       *Matcher           // встроенный список + geo-domain
	Prober       TLSProber
	StatePath    string
	SnapshotPath string
	// Flush удаляет из кеша ответы, для которых pred вернул true.
	Flush func(pred func(*dns.Msg) bool) int
	// Stale — устаревший ответ из кеша на этот запрос или nil.
	Stale func(m *dns.Msg) *dns.Msg
}

// Lanes — upstream.Exchanger с двумя полосами: геоблокированные имена идут
// только в закреплённый сервер обхода, остальные — в быструю полосу.
type Lanes struct {
	cfg      Config
	class    *Classifier
	prints   *Fingerprints
	health   *Health
	ev       *Evaluator
	reselect chan string
	interval time.Duration
	now      func() time.Time

	mu          sync.Mutex
	pinned      string
	since       time.Time
	evaluatedAt time.Time
	evaluating  bool
	ranking     []Result
	fails       int
	deadCycles  int
	rtt         time.Duration // EWMA ответа закреплённого
}

func New(cfg Config) *Lanes {
	if cfg.Static == nil {
		cfg.Static = NewMatcher(BuiltinDomains()) // встроенный список по умолчанию
	}
	if cfg.Prober == nil {
		cfg.Prober = NewTLSProber(2 * time.Second)
	}
	if cfg.Flush == nil {
		cfg.Flush = func(func(*dns.Msg) bool) int { return 0 }
	}
	if cfg.Stale == nil {
		cfg.Stale = func(*dns.Msg) *dns.Msg { return nil }
	}
	l := &Lanes{
		cfg:      cfg,
		class:    NewClassifier(cfg.Static),
		prints:   NewFingerprints(),
		health:   NewHealth(cfg.Prober),
		ev:       &Evaluator{Reference: cfg.Reference, Prober: cfg.Prober, Probes: ProbeDomains, Attempts: 3, Timeout: evaluationTimeout},
		reselect: make(chan string, 1),
		interval: checkInterval,
		now:      time.Now,
	}
	if len(cfg.Geo) > 0 {
		l.pinned = cfg.Geo[0].URL // временно, до первой оценки
	}
	return l
}

func (l *Lanes) Address() string { return l.cfg.Fast.Address() }

func (l *Lanes) Pinned() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pinned
}

func (l *Lanes) server(url string) (Server, bool) {
	for _, s := range l.cfg.Geo {
		if s.URL == url {
			return s, true
		}
	}
	return Server{}, false
}

// geoOrder: закреплённый, затем по рейтингу, затем остальные по конфигу.
func (l *Lanes) geoOrder() []Server {
	l.mu.Lock()
	pinned, ranking := l.pinned, l.ranking
	l.mu.Unlock()
	seen := map[string]bool{}
	var out []Server
	add := func(u string) {
		if s, ok := l.server(u); ok && !seen[u] {
			seen[u] = true
			out = append(out, s)
		}
	}
	add(pinned)
	for _, r := range ranking {
		add(r.URL)
	}
	for _, s := range l.cfg.Geo {
		add(s.URL)
	}
	return out
}

type exResult struct {
	resp *dns.Msg
	rtt  time.Duration
	err  error
}

func (r exResult) ok() bool { return r.err == nil && r.resp != nil && !upstream.SoftFail(r.resp) }

func (l *Lanes) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	if len(l.cfg.Geo) == 0 || len(m.Question) != 1 {
		return l.cfg.Fast.Exchange(ctx, m)
	}
	q := m.Question[0]
	switch l.class.Lookup(q.Name) {
	case Geo:
		return l.geoExchange(ctx, m)
	case Plain:
		return l.cfg.Fast.Exchange(ctx, m)
	}
	if q.Qtype == dns.TypeA || q.Qtype == dns.TypeAAAA {
		return l.classifyExchange(ctx, m)
	}
	// HTTPS, SVCB и прочее: полосу определяет A-запись. Иначе Safari получил
	// бы из HTTPS-записи Cloudflare подсказки с настоящими адресами.
	probe := new(dns.Msg)
	probe.SetQuestion(q.Name, dns.TypeA)
	l.classifyExchange(ctx, probe)
	if l.class.Lookup(q.Name) == Geo {
		return l.geoExchange(ctx, m)
	}
	return l.cfg.Fast.Exchange(ctx, m)
}

// classifyWait — сколько ждать закреплённого для неизвестного имени.
func (l *Lanes) classifyWait() time.Duration {
	l.mu.Lock()
	rtt := l.rtt
	l.mu.Unlock()
	if rtt == 0 {
		return maxClassifyWait
	}
	return min(max(3*rtt, minClassifyWait), maxClassifyWait)
}

// classifyExchange — неизвестное имя: быстрая полоса и закреплённый сервер
// параллельно. Ответ любого из них из пула сервера обхода — имя гео.
func (l *Lanes) classifyExchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	name := m.Question[0].Name
	pinned := l.geoOrder()[0]
	fastCh := make(chan exResult, 1)
	geoCh := make(chan exResult, 1)
	go func() {
		resp, err := l.cfg.Fast.Exchange(ctx, m)
		fastCh <- exResult{resp: resp, err: err}
	}()
	go func() {
		actx, cancel := context.WithTimeout(ctx, geoAttemptTimeout)
		defer cancel()
		resp, rtt, err := upstream.ExchangeTimed(actx, pinned.Ex, m)
		geoCh <- exResult{resp: resp, rtt: rtt, err: err}
	}()
	timer := time.NewTimer(l.classifyWait())
	defer timer.Stop()
	capC := timer.C
	var fast, geo *exResult
	for {
		select {
		case r := <-fastCh:
			fast = &r
		case r := <-geoCh:
			geo = &r
			l.notePinned(pinned.URL, r)
		case <-capC:
			capC = nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		fastOK := fast != nil && fast.ok()
		geoOK := geo != nil && geo.ok()
		switch {
		case geoOK && l.prints.MatchAny(geo.resp):
			l.class.Learn(name, Geo)
			slog.Debug("geo: learned geo-blocked name", "name", name)
			return l.finishGeo(pinned.URL, geo.resp, name), nil
		case fastOK && l.prints.MatchAny(fast.resp):
			// Быструю полосу обогнал сервер обхода со своим пулом.
			if geo == nil {
				continue
			}
			l.class.Learn(name, Geo)
			slog.Debug("geo: learned geo-blocked name", "name", name)
			return l.geoExchange(ctx, m)
		case fast != nil && geo != nil:
			if fastOK && geoOK {
				l.class.Learn(name, Plain)
			}
			if fast.err == nil {
				return fast.resp, nil
			}
			if geo.err == nil {
				return geo.resp, nil
			}
			return nil, fmt.Errorf("fast lane: %v; geo server: %v", fast.err, geo.err)
		case fastOK && capC == nil:
			// Закреплённый не успел: ответ быстрой полосы, имя не выучено.
			return fast.resp, nil
		}
	}
}

// geoExchange — гео-имя: закреплённый, при его ошибке — запасные по
// рейтингу, при отказе всех — устаревший ответ или быстрая полоса.
func (l *Lanes) geoExchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	name := m.Question[0].Name
	var lastErr error
	for i, s := range l.geoOrder() {
		actx, cancel := context.WithTimeout(ctx, geoAttemptTimeout)
		resp, rtt, err := upstream.ExchangeTimed(actx, s.Ex, m)
		cancel()
		r := exResult{resp: resp, rtt: rtt, err: err}
		if i == 0 {
			l.notePinned(s.URL, r)
		}
		if r.ok() {
			if i == 0 {
				return l.finishGeo(s.URL, resp, name), nil
			}
			out := resp.Copy()
			upstream.CapTTL(out, degradedMaxTTL)
			return out, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("%s answered %s", s.URL, dns.RcodeToString[resp.Rcode])
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if l.cfg.Stale(m) != nil {
		return nil, fmt.Errorf("%w: %v", ErrGeoUnavailable, lastErr)
	}
	resp, err := l.cfg.Fast.Exchange(ctx, m)
	if err != nil {
		return nil, err
	}
	out := resp.Copy()
	upstream.CapTTL(out, degradedMaxTTL)
	return out, nil
}

// finishGeo — ответ закреплённого: пополнить отпечаток (только пробными
// доменами — иначе гео-имя без подмены затащило бы в пул чужие адреса),
// поставить новые адреса на проверку, выкинуть мёртвые и медленные.
func (l *Lanes) finishGeo(url string, resp *dns.Msg, name string) *dns.Msg {
	if isProbeDomain(name) {
		l.prints.AddTo(url, resp)
	}
	ips, _ := answerAddrs(resp)
	for _, a := range ips {
		if l.health.Track(a, normalize(name)) {
			go l.checkNew(a)
		}
	}
	out := l.health.Filter(resp)
	upstream.CapTTL(out, geoMaxTTL)
	return out
}

func isProbeDomain(name string) bool {
	n := normalize(name)
	for _, p := range ProbeDomains {
		if normalize(p) == n {
			return true
		}
	}
	return false
}

// checkNew проверяет адрес, впервые пришедший в ответе; мёртвый — долой из кеша.
func (l *Lanes) checkNew(a netip.Addr) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < deadAfter; i++ {
		if l.health.Check(ctx, a) == nil {
			return
		}
	}
	if l.health.State(a) == Dead {
		n := l.cfg.Flush(func(m *dns.Msg) bool {
			ips, _ := answerAddrs(m)
			for _, x := range ips {
				if x == a {
					return true
				}
			}
			return false
		})
		slog.Info("geo: new proxy address is dead, dropped from the cache", "addr", a, "entries", n)
	}
}
