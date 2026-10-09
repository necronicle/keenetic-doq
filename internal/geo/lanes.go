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
	retryBase            = time.Minute
	retryMax             = 30 * time.Minute
	netCheckName         = "example.com." // проверка сети перед отказом закреплённого
	netCheckTimeout      = 3 * time.Second
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
	attempt  time.Duration // предел одной попытки к серверу обхода
	// Отсрочка повтора после оценки без охвата: retryBase, удваивается до retryMax.
	retryBase, retryMax time.Duration
	now                 func() time.Time
	loopDone            chan struct{} // закрывается при выходе фонового цикла

	mu           sync.Mutex
	pinned       string
	since        time.Time
	evaluatedAt  time.Time
	evaluating   bool
	attemptedAt  time.Time     // конец последней оценки, в том числе безрезультатной
	retryPartial bool          // повтор назначен из-за неполного охвата победителя
	partialDelay time.Duration // отсрочка повтора при неполном охвате
	retryDelay   time.Duration // >0 — последняя оценка безрезультатна, повтор через столько
	ranking      []Result
	saved        *State // что сейчас в geo.state; nil — ещё не писали и не читали
	fails        int
	deadCycles   int
	rtt          time.Duration // EWMA ответа закреплённого
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
		cfg:       cfg,
		class:     NewClassifier(cfg.Static),
		prints:    NewFingerprints(),
		health:    NewHealth(cfg.Prober),
		ev:        &Evaluator{Reference: cfg.Reference, Prober: cfg.Prober, Probes: ProbeDomains, Attempts: 3, Timeout: evaluationTimeout},
		reselect:  make(chan string, 1),
		interval:  checkInterval,
		attempt:   geoAttemptTimeout,
		retryBase: retryBase,
		retryMax:  retryMax,
		now:       time.Now,
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

// errNoResponse — апстрим не вернул ни ответа, ни ошибки.
var errNoResponse = errors.New("upstream returned no response")

// result собирает исход запроса; (nil, nil) превращается в ошибку, чтобы
// дальше ответ можно было разыменовывать без проверок.
func result(resp *dns.Msg, rtt time.Duration, err error) exResult {
	if err == nil && resp == nil {
		err = errNoResponse
	}
	return exResult{resp: resp, rtt: rtt, err: err}
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
	if q.Qtype == dns.TypeA {
		return l.classifyExchange(ctx, m)
	}
	// AAAA, HTTPS, SVCB и прочее: полосу определяет A-запись. Пулы строятся по
	// A, поэтому AAAA-ответ сервера обхода не совпал бы ни с одним пулом и имя
	// выучилось бы «обычным»; а из HTTPS-записи Cloudflare Safari получил бы
	// подсказки с настоящими адресами.
	probe := new(dns.Msg)
	probe.SetQuestion(q.Name, dns.TypeA)
	l.classifyExchange(ctx, probe)
	if l.class.Lookup(q.Name) == Geo {
		return l.geoExchange(ctx, m)
	}
	resp, err := l.cfg.Fast.Exchange(ctx, m)
	if err == nil && resp != nil && l.class.Lookup(q.Name) == Unknown {
		return unverified(resp), nil
	}
	return resp, err
}

// unverified — ответ быстрой полосы на неизвестное имя без вердикта закреплённого:
// копия с урезанным TTL, чтобы ответ с настоящим адресом не жил в кеше долго.
func unverified(resp *dns.Msg) *dns.Msg {
	out := resp.Copy()
	upstream.CapTTL(out, degradedMaxTTL)
	return out
}

// learnGeo запоминает имя как гео и выбрасывает из кеша его ответы (любого типа):
// ответ быстрой полосы, закешированный раньше, мог нести настоящий адрес
// (например ipv4hint в HTTPS). Вызывается без l.mu.
func (l *Lanes) learnGeo(name string) {
	l.class.Learn(name, Geo)
	slog.Debug("geo: learned geo-blocked name", "name", name)
	n := normalize(name)
	l.cfg.Flush(func(m *dns.Msg) bool {
		return len(m.Question) > 0 && normalize(m.Question[0].Name) == n
	})
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
	name, qtype := m.Question[0].Name, m.Question[0].Qtype
	pinned := l.geoOrder()[0]
	l.mu.Lock()
	failing := l.fails > 0
	l.mu.Unlock()
	fastCh := make(chan exResult, 1)
	geoCh := make(chan exResult, 1)
	go func() {
		resp, err := l.cfg.Fast.Exchange(ctx, m)
		fastCh <- result(resp, 0, err)
	}()
	// Запрос к закреплённому живёт дольше клиента: клиент получает ответ
	// быстрой полосы через 0,3–1 с, а исход (и таймаут) закреплённого должен
	// попасть в счёт неудач.
	go func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.attempt)
		defer cancel()
		r := result(upstream.ExchangeTimed(actx, pinned.Ex, m))
		l.notePinned(pinned.URL, qtype, r, false)
		geoCh <- r
	}()
	if failing {
		// Закреплённый уже ошибается: каждое новое имя ждало бы его до 1 с.
		// Ответ быстрой полосы сразу, имя не выучивается.
		return l.fastFirst(ctx, fastCh, geoCh)
	}
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
		case <-capC:
			capC = nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		fastOK := fast != nil && fast.ok()
		geoOK := geo != nil && geo.ok()
		switch {
		case geoOK && l.prints.MatchAny(geo.resp):
			l.learnGeo(name)
			return l.finishGeo(pinned.URL, geo.resp, name), nil
		case fastOK && l.prints.MatchAny(fast.resp):
			// Быструю полосу обогнал сервер обхода со своим пулом.
			if geo == nil {
				continue
			}
			l.learnGeo(name)
			return l.geoExchange(ctx, m)
		case fast != nil && geo != nil:
			if fastOK && geoOK {
				l.class.Learn(name, Plain)
			}
			if fast.err == nil {
				if fastOK && geoOK {
					return fast.resp, nil // имя «обычное»: вердикт есть
				}
				return unverified(fast.resp), nil
			}
			if geo.err == nil {
				return geo.resp, nil
			}
			return nil, fmt.Errorf("fast lane: %v; geo server: %v", fast.err, geo.err)
		case fastOK && capC == nil:
			// Закреплённый не успел: ответ быстрой полосы, имя не выучено.
			return unverified(fast.resp), nil
		}
	}
}

// fastFirst — ответ быстрой полосы, а при её ошибке — закреплённого.
func (l *Lanes) fastFirst(ctx context.Context, fastCh, geoCh <-chan exResult) (*dns.Msg, error) {
	var fast exResult
	select {
	case fast = <-fastCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if fast.err == nil {
		return unverified(fast.resp), nil
	}
	select {
	case g := <-geoCh:
		if g.err == nil {
			return g.resp, nil
		}
		return nil, fmt.Errorf("fast lane: %v; geo server: %v", fast.err, g.err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// geoExchange — гео-имя: закреплённый, при его ошибке — запасные по
// рейтингу, при отказе всех — устаревший ответ или быстрая полоса.
// HTTPS/SVCB в быструю полосу не уходят никогда: её ответ несёт в
// ipv4hint/ipv6hint настоящий адрес геоблокированного сервиса.
func (l *Lanes) geoExchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	q := m.Question[0]
	var lastErr error
	for i, s := range l.geoOrder() {
		actx, cancel := context.WithTimeout(ctx, l.attempt)
		r := result(upstream.ExchangeTimed(actx, s.Ex, m))
		cancel()
		resp := r.resp
		if i == 0 {
			l.notePinned(s.URL, q.Qtype, r, true)
		}
		if r.ok() {
			var out *dns.Msg
			if i == 0 {
				out = l.finishGeo(s.URL, resp, q.Name)
			} else {
				out = resp.Copy()
				upstream.CapTTL(out, degradedMaxTTL)
			}
			if carriesAddrHints(q.Qtype) {
				stripAddrHints(out)
			}
			return out, nil
		}
		if r.err != nil {
			lastErr = r.err
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
	if carriesAddrHints(q.Qtype) {
		// NODATA: клиент возьмёт адреса из A/AAAA, а они идут через прокси.
		// Без SOA кеш держит пустой ответ 60 с.
		out := new(dns.Msg)
		out.SetReply(m)
		out.RecursionAvailable = true
		return out, nil
	}
	resp, err := l.cfg.Fast.Exchange(ctx, m)
	r := result(resp, 0, err)
	if r.err != nil {
		return nil, r.err
	}
	out := r.resp.Copy()
	upstream.CapTTL(out, degradedMaxTTL)
	return out, nil
}

// carriesAddrHints — типы, в ответах на которые бывают ipv4hint/ipv6hint.
func carriesAddrHints(qtype uint16) bool { return qtype == dns.TypeHTTPS || qtype == dns.TypeSVCB }

// stripAddrHints убирает ipv4hint/ipv6hint из HTTPS/SVCB-записей ответа
// (m — уже копия). Подсказка может нести настоящий адрес сервиса, и браузер
// пошёл бы по нему мимо прокси; без подсказок он берёт A/AAAA.
func stripAddrHints(m *dns.Msg) {
	strip := func(kv []dns.SVCBKeyValue) []dns.SVCBKeyValue {
		out := make([]dns.SVCBKeyValue, 0, len(kv))
		for _, x := range kv {
			if k := x.Key(); k != dns.SVCB_IPV4HINT && k != dns.SVCB_IPV6HINT {
				out = append(out, x)
			}
		}
		return out
	}
	for _, sec := range [][]dns.RR{m.Answer, m.Extra} {
		for _, rr := range sec {
			switch v := rr.(type) {
			case *dns.HTTPS:
				v.Value = strip(v.Value)
			case *dns.SVCB:
				v.Value = strip(v.Value)
			}
		}
	}
}

// finishGeo — ответ закреплённого: пополнить отпечаток (только пробными
// доменами — иначе гео-имя без подмены затащило бы в пул чужие адреса),
// поставить новые адреса на проверку, выкинуть мёртвые и медленные.
func (l *Lanes) finishGeo(url string, resp *dns.Msg, name string) *dns.Msg {
	covered := l.isCovered(url, name)
	if covered {
		l.prints.AddTo(url, resp)
	}
	ips, _ := answerAddrs(resp)
	for _, a := range ips {
		if l.health.Track(a, normalize(name), covered) {
			go l.checkNew(a)
		}
	}
	out := l.health.Filter(resp)
	upstream.CapTTL(out, geoMaxTTL)
	return out
}

// isCovered: имя — пробный домен, который сервер по последней оценке подменяет
// живым прокси. Только такие ответы пополняют пул: настоящий адрес хоста,
// который сервер не подменяет, прокси не является.
func (l *Lanes) isCovered(url, name string) bool {
	n := normalize(name)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.ranking {
		if r.URL != url {
			continue
		}
		for _, c := range r.Covered {
			if c == n {
				return true
			}
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
		n := l.cfg.Flush(l.health.Refilter([]netip.Addr{a}))
		slog.Info("geo: new proxy address is dead, dropped from the cache", "addr", a, "entries", n)
	}
}
