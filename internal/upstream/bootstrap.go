package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// DefaultBootstrapServers — обычные DNS-серверы, через которые резолвятся имена
// DoQ-апстримов. Только IP-литералы: имя здесь потребовало бы резолва, а резолв
// именно то, чего bootstrap избегает. 77.88.8.8:1253 — тот же Яндекс на
// нестандартном порту: DPI, выбрасывающий запросы на 53-й, его не трогает.
var DefaultBootstrapServers = []string{"77.88.8.8:53", "77.88.8.8:1253", "8.8.8.8:53", "1.1.1.1:53"}

var (
	// bootstrapTimeout — на весь резолв одного имени, все серверы разом.
	bootstrapTimeout = 3 * time.Second
	// tcpFallbackDelay — сколько ждать ответа по UDP, прежде чем спросить тот
	// же сервер ещё и по TCP. Ответ по UDP при этом всё ещё принимается.
	tcpFallbackDelay = time.Second
)

// Границы, в которых держится в кеше ответ bootstrap-а.
const (
	minBootstrapTTL = time.Minute
	maxBootstrapTTL = time.Hour
)

// ErrBootstrapNoAnswer — ни один bootstrap-сервер не дал адреса ни по UDP, ни
// по TCP. Если все при этом молчат, запросы с этим именем, скорее всего, режет
// провайдер.
var ErrBootstrapNoAnswer = errors.New("no bootstrap server answered")

// Bootstrap резолвит имя DoQ-апстрима в IP.
type Bootstrap interface {
	LookupIP(ctx context.Context, host string) (net.IP, error)
}

// PlainDNS спрашивает обычным DNS явно заданные серверы — в обход системного
// резолвера. На Keenetic системный резолвер это 127.0.0.1:53 (ndnproxy), в
// списке серверов которого прописан сам doqd: пойти туда за адресом апстрима
// значит послать запрос самому себе и повесить DNS роутера.
type PlainDNS struct {
	Servers []string

	mu    sync.Mutex
	cache map[string]cachedIP
}

type cachedIP struct {
	ip    net.IP
	until time.Time
}

func NewBootstrap(servers []string) *PlainDNS {
	if len(servers) == 0 {
		servers = DefaultBootstrapServers
	}
	return &PlainDNS{Servers: servers}
}

// LookupIP отдаёт адрес из кеша, пока не истёк TTL, иначе спрашивает серверы.
// Если никто не ответил, а адрес уже был известен, возвращает его: подмена тут
// ничего не даёт — сертификат апстрима всё равно проверяется по имени.
func (b *PlainDNS) LookupIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	b.mu.Lock()
	c, known := b.cache[host]
	b.mu.Unlock()
	if known && time.Now().Before(c.until) {
		return c.ip, nil
	}
	ip, ttl, err := b.query(ctx, host)
	if err != nil {
		if known {
			return c.ip, nil
		}
		return nil, err
	}
	ttl = min(max(ttl, minBootstrapTTL), maxBootstrapTTL)
	b.mu.Lock()
	if b.cache == nil {
		b.cache = map[string]cachedIP{}
	}
	b.cache[host] = cachedIP{ip: ip, until: time.Now().Add(ttl)}
	b.mu.Unlock()
	return ip, nil
}

// expireCache помечает все адреса устаревшими (для тестов).
func (b *PlainDNS) expireCache() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for h, c := range b.cache {
		c.until = time.Time{}
		b.cache[h] = c
	}
}

type bootAnswer struct {
	server, proto string
	ip            net.IP
	ttl           time.Duration
	err           error
}

// query спрашивает все серверы параллельно — молчащий сервер не съедает время
// остальных — и берёт первый ответ с A-записью.
func (b *PlainDNS) query(ctx context.Context, host string) (net.IP, time.Duration, error) {
	// Тайминги читаются один раз: горутины переживают этот вызов.
	timeout, tcpDelay := bootstrapTimeout, tcpFallbackDelay
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	// У каждого сервера ровно два ответа (udp и tcp); буфер — чтобы
	// опоздавшие горутины не висели на отправке.
	out := make(chan bootAnswer, 2*len(b.Servers))
	for _, s := range b.Servers {
		go askServer(ctx, s, host, timeout, tcpDelay, out)
	}
	var failed []bootAnswer
	for range 2 * len(b.Servers) {
		a := <-out
		if a.err == nil {
			return a.ip, a.ttl, nil
		}
		failed = append(failed, a)
	}
	return nil, 0, bootstrapError(host, b.Servers, failed, time.Since(start))
}

// askServer спрашивает сервер по UDP, а если за tcpDelay ответа нет
// (или UDP уже отказал) — ещё и по TCP: DPI часто режет только UDP.
func askServer(ctx context.Context, server, host string, timeout, tcpDelay time.Duration, out chan<- bootAnswer) {
	udpFailed := make(chan struct{})
	go func() {
		a := exchangePlain(ctx, "udp", server, host, timeout)
		if a.err != nil {
			close(udpFailed)
		}
		out <- a
	}()
	t := time.NewTimer(tcpDelay)
	defer t.Stop()
	select {
	case <-udpFailed:
	case <-t.C:
	case <-ctx.Done(): // UDP ответил или время вышло — TCP вернётся сразу
	}
	out <- exchangePlain(ctx, "tcp", server, host, timeout)
}

func exchangePlain(ctx context.Context, proto, server, host string, timeout time.Duration) bootAnswer {
	a := bootAnswer{server: server, proto: proto}
	if a.err = ctx.Err(); a.err != nil {
		return a
	}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(host), dns.TypeA)
	c := &dns.Client{Net: proto, Timeout: timeout}
	resp, _, err := c.ExchangeContext(ctx, q, server)
	if err != nil {
		a.err = err
		return a
	}
	if resp.Rcode != dns.RcodeSuccess {
		a.err = errors.New(dns.RcodeToString[resp.Rcode])
		return a
	}
	for _, rr := range resp.Answer {
		if rec, ok := rr.(*dns.A); ok {
			a.ip, a.ttl = rec.A, time.Duration(rec.Hdr.Ttl)*time.Second
			return a
		}
	}
	a.err = errors.New("no A record")
	return a
}

// bootstrapError собирает ошибки всех серверов в одну строку. Таймаут на
// исчерпанном контексте ("dial udp ...: i/o timeout") — не причина, а
// следствие, поэтому все таймауты называются одинаково: "no answer".
func bootstrapError(host string, servers []string, failed []bootAnswer, took time.Duration) error {
	why := map[string]map[string]string{}
	silent := true
	for _, a := range failed {
		r := shortReason(a.err)
		if r != "no answer" {
			silent = false
		}
		if why[a.server] == nil {
			why[a.server] = map[string]string{}
		}
		why[a.server][a.proto] = r
	}
	took = took.Round(100 * time.Millisecond)
	if silent {
		return fmt.Errorf("bootstrap lookup %s: %w in %v over udp or tcp: %s",
			host, ErrBootstrapNoAnswer, took, strings.Join(servers, ", "))
	}
	parts := make([]string, 0, len(servers))
	for _, s := range servers {
		parts = append(parts, fmt.Sprintf("%s (udp %s, tcp %s)", s, why[s]["udp"], why[s]["tcp"]))
	}
	return fmt.Errorf("bootstrap lookup %s: %w: %s", host, ErrBootstrapNoAnswer, strings.Join(parts, "; "))
}

func shortReason(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		(errors.As(err, &ne) && ne.Timeout()) {
		return "no answer"
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}
