package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type Exchanger interface {
	Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error)
	Address() string
}

const downCooldown = 30 * time.Second

// fallbackMaxTTL — потолок TTL ответа резервного апстрима. Пока основные
// лежат, имена, которые нужно резолвить через сервер обхода геоблокировок,
// получают от резерва настоящие адреса; короткий TTL не даёт им застрять в
// кешах после того, как основной вернулся.
const fallbackMaxTTL = 60

var (
	// attemptTimeout — предел одной попытки. Без него зависший апстрим съедал
	// весь бюджет запроса, и до живого очередь не доходила.
	attemptTimeout = 3 * time.Second
	// unmeasuredHedge — через сколько подстраховаться следующим апстримом,
	// если у текущего ещё нет замеров. С замерами — 3×RTT в пределах
	// [minHedge, maxHedge].
	unmeasuredHedge = time.Second
)

const (
	minHedge = 200 * time.Millisecond
	maxHedge = 1500 * time.Millisecond
)

type upstreamState struct {
	ex        Exchanger
	fallback  bool          // резервный: спрашивается, только когда основные отказали
	rtt       time.Duration // EWMA; 0 = замеров ещё не было
	downUntil time.Time
	// failed — последний исход был отказом. Такой апстрим стоит за здоровыми
	// и после cooldown: вернуть его вперёд может только удачный ответ.
	failed bool
}

type Picker struct {
	mu  sync.Mutex
	ups []*upstreamState
	now func() time.Time

	// Тайминги копируются при создании: горутины попыток переживают вызов.
	attemptTimeout, unmeasuredHedge time.Duration
}

// NewPicker собирает пул из основных апстримов ups и резервных fallbacks.
func NewPicker(ups []Exchanger, fallbacks ...Exchanger) *Picker {
	p := &Picker{now: time.Now, attemptTimeout: attemptTimeout, unmeasuredHedge: unmeasuredHedge}
	for _, u := range ups {
		p.ups = append(p.ups, &upstreamState{ex: u})
	}
	for _, u := range fallbacks {
		p.ups = append(p.ups, &upstreamState{ex: u, fallback: true})
	}
	return p
}

// ordered: здоровые, затем отказавшие, чей cooldown истёк, затем те, что в
// cooldown, — последней надеждой. Внутри каждой группы основные идут раньше
// резервных (в p.ups они и так идут первыми), здоровые — по возрастанию
// EWMA RTT (незамеренные, rtt=0, пробуются рано). Здоровый резерв стоит раньше отказавшего основного: тот
// вернётся вперёд, как только ответит фоновой проверке.
func (p *Picker) ordered() []*upstreamState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var healthy, suspect, down []*upstreamState
	for _, st := range p.ups {
		switch {
		case st.downUntil.After(now):
			down = append(down, st)
		case st.failed:
			suspect = append(suspect, st)
		default:
			healthy = append(healthy, st)
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		if healthy[i].fallback != healthy[j].fallback {
			return !healthy[i].fallback
		}
		return healthy[i].rtt < healthy[j].rtt
	})
	return append(append(healthy, suspect...), down...)
}

func (p *Picker) hedgeDelay(st *upstreamState) time.Duration {
	p.mu.Lock()
	rtt := st.rtt
	p.mu.Unlock()
	if rtt == 0 {
		return p.unmeasuredHedge
	}
	return min(max(3*rtt, minHedge), maxHedge)
}

func (p *Picker) markSuccess(st *upstreamState, rtt time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.downUntil = time.Time{}
	st.failed = false
	if st.rtt == 0 {
		st.rtt = rtt
	} else {
		st.rtt = (st.rtt*7 + rtt) / 8
	}
}

// markSlow: апстрим не успел ответить за время, пока ответил другой, — его RTT
// не меньше прошедшего. Незамеренному (rtt=0) это и есть первый замер: иначе
// зависший апстрим навсегда оставался бы первым, и каждый запрос ждал бы
// подстраховки. Замеренному проигрыш подмешивается в среднее: одна потеря
// пакета не должна навсегда отодвинуть лучший апстрим.
func (p *Picker) markSlow(st *upstreamState, atLeast time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st.rtt == 0 {
		st.rtt = atLeast
	} else {
		st.rtt = max(st.rtt, (st.rtt*7+atLeast)/8)
	}
}

func (p *Picker) markDown(st *upstreamState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.downUntil = p.now().Add(downCooldown)
	st.failed = true
}

func (p *Picker) isDown(ex Exchanger) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.ups {
		if st.ex == ex {
			return st.failed || st.downUntil.After(p.now())
		}
	}
	return false
}

// failedStates — апстримы, чей последний исход был отказом: их проверяет
// health check, в том числе после истечения cooldown.
func (p *Picker) failedStates() []*upstreamState {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*upstreamState
	for _, st := range p.ups {
		if st.failed {
			out = append(out, st)
		}
	}
	return out
}

// Address позволяет использовать Picker всюду, где ждут одиночный Exchanger.
func (p *Picker) Address() string {
	parts := make([]string, 0, len(p.ups))
	for _, st := range p.ups {
		parts = append(parts, st.ex.Address())
	}
	return strings.Join(parts, ",")
}

// timedExchanger — апстрим, который отдаёт время самого обмена без дозвона.
// Иначе в замер скорости попадали бы bootstrap и рукопожатие.
type timedExchanger interface {
	ExchangeTimed(ctx context.Context, m *dns.Msg) (*dns.Msg, time.Duration, error)
}

func exchangeTimed(ctx context.Context, ex Exchanger, m *dns.Msg) (*dns.Msg, time.Duration, error) {
	if t, ok := ex.(timedExchanger); ok {
		return t.ExchangeTimed(ctx, m)
	}
	start := time.Now()
	resp, err := ex.Exchange(ctx, m)
	return resp, time.Since(start), err
}

// softFail — ответ есть, но бесполезный: так отвечает сломанный рекурсор, и
// отвечает быстро. Такой ответ не должен выигрывать у здорового апстрима, но
// отдаётся, если лучшего не нашлось (бывает, что домен сломан у всех).
func softFail(resp *dns.Msg) bool {
	return resp.Rcode == dns.RcodeServerFailure || resp.Rcode == dns.RcodeRefused
}

type attempt struct {
	st    *upstreamState
	start time.Time
	resp  *dns.Msg
	rtt   time.Duration
	err   error
}

// Exchange спрашивает апстримы по очереди с подстраховкой: если текущий не
// ответил за hedgeDelay (или уже отказал), тот же запрос уходит следующему,
// а первый ещё может успеть. Резервный апстрим подстраховкой не запускается,
// пока ждётся ответ основного: обогнав медленный сервер обхода геоблокировок,
// он вернул бы для заблокированного сервиса настоящий адрес. Побеждает первый полезный ответ, остальные
// отменяются. У каждой попытки свой предел attemptTimeout. Если в круге кто-то
// не дождался ответа, а время у запроса есть, делается второй круг: мёртвое
// соединение к этому моменту уже заменено.
func (p *Picker) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	if len(p.ups) == 0 {
		return nil, fmt.Errorf("no upstreams")
	}
	var soft *dns.Msg
	var lastErr error
	for round := 0; round < 2; round++ {
		resp, timedOut, err := p.round(ctx, m, &soft)
		if resp != nil {
			return resp, nil
		}
		lastErr = err
		if !timedOut || soft != nil || ctx.Err() != nil {
			break
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < p.attemptTimeout/2 {
			break
		}
	}
	if soft != nil {
		return soft, nil
	}
	return nil, fmt.Errorf("all upstreams failed, last: %w", lastErr)
}

// round — один проход по апстримам. timedOut — хотя бы одна попытка упёрлась
// в свой предел.
func (p *Picker) round(parent context.Context, m *dns.Msg, soft **dns.Msg) (resp *dns.Msg, timedOut bool, lastErr error) {
	order := p.ordered()
	ctx, cancel := context.WithCancel(parent)
	defer cancel() // отменяет проигравших
	out := make(chan attempt, len(order))
	running := map[*upstreamState]time.Time{} // ещё не ответившие попытки
	softs := map[*upstreamState]time.Time{}   // ответившие SERVFAIL/REFUSED
	launch := func(st *upstreamState) {
		a := attempt{st: st, start: time.Now()}
		running[st] = a.start
		go func() {
			actx, acancel := context.WithTimeout(ctx, p.attemptTimeout)
			defer acancel()
			a.resp, a.rtt, a.err = exchangeTimed(actx, st.ex, m)
			out <- a
		}()
	}
	// mayLaunch: резерв ждёт, пока не кончатся попытки основных.
	mayLaunch := func(st *upstreamState) bool {
		if !st.fallback {
			return true
		}
		for r := range running {
			if !r.fallback {
				return false
			}
		}
		return true
	}
	launch(order[0])
	next, pending := 1, 1
	timer := time.NewTimer(p.hedgeDelay(order[0]))
	defer timer.Stop()
	for {
		var tick <-chan time.Time
		if next < len(order) && mayLaunch(order[next]) {
			tick = timer.C
		}
		select {
		case a := <-out:
			pending--
			delete(running, a.st)
			switch {
			case a.err == nil && !softFail(a.resp):
				p.markSuccess(a.st, a.rtt)
				for st, start := range running {
					if start.Before(a.start) {
						p.markSlow(st, time.Since(start))
					}
				}
				// Быстрый SERVFAIL проиграл полезному ответу — это сломанный
				// рекурсор, а не сломанный домен: отодвинуть, как проигравшего.
				for st, start := range softs {
					p.markSlow(st, time.Since(start))
				}
				if a.st.fallback && p.hasPrimary() {
					capTTL(a.resp, fallbackMaxTTL)
				}
				return a.resp, false, nil
			case a.err == nil: // SERVFAIL/REFUSED: запомнить и спросить следующего
				if *soft == nil {
					*soft = a.resp
				}
				softs[a.st] = a.start
				lastErr = fmt.Errorf("%s answered %s", a.st.ex.Address(), dns.RcodeToString[a.resp.Rcode])
			case parent.Err() != nil:
				// Кончилось время самого запроса — апстрим не виноват.
				return nil, timedOut, a.err
			default:
				lastErr = a.err
				timedOut = timedOut || errors.Is(a.err, context.DeadlineExceeded)
				slog.Warn("upstream failed", "upstream", a.st.ex.Address(), "err", a.err)
				p.markDown(a.st)
			}
			if next < len(order) && mayLaunch(order[next]) {
				launch(order[next])
				timer.Reset(p.hedgeDelay(order[next]))
				next++
				pending++
			} else if pending == 0 {
				return nil, timedOut, lastErr
			}
		case <-tick:
			launch(order[next])
			timer.Reset(p.hedgeDelay(order[next]))
			next++
			pending++
		case <-parent.Done():
			if lastErr == nil {
				lastErr = parent.Err()
			}
			return nil, timedOut, lastErr
		}
	}
}

// hasPrimary: есть ли основные апстримы — без них резервный и есть основной.
func (p *Picker) hasPrimary() bool {
	for _, st := range p.ups {
		if !st.fallback {
			return true
		}
	}
	return false
}

// capTTL ограничивает TTL всех записей ответа сверху.
func capTTL(m *dns.Msg, maxTTL uint32) {
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			if h := rr.Header(); h.Rrtype != dns.TypeOPT && h.Ttl > maxTTL {
				h.Ttl = maxTTL
			}
		}
	}
}

// StartHealthCheck фоново пробует отказавшие апстримы запросом ". NS" и
// возвращает ожившие в ротацию.
func (p *Picker) StartHealthCheck(ctx context.Context, interval time.Duration) {
	p.startupProbe(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for _, st := range p.failedStates() {
				p.probe(ctx, st, false)
			}
		}
	}()
}

// startupProbe пробует все апстримы сразу при старте: мёртвый должен
// оказаться в конце очереди до первого клиентского запроса, а не за его счёт.
// Как только кто-то ответил, ещё молчащие получают нижнюю оценку RTT (как
// проигравшие подстраховке) — ждать их таймаута незачем.
func (p *Picker) startupProbe(ctx context.Context) {
	start := time.Now()
	var mu sync.Mutex
	pending := map[*upstreamState]bool{}
	for _, st := range p.ups {
		pending[st] = true
	}
	for _, st := range p.ups {
		go func() {
			ok := p.probe(ctx, st, true)
			mu.Lock()
			delete(pending, st)
			var slow []*upstreamState
			if ok {
				for other := range pending {
					slow = append(slow, other)
				}
			}
			mu.Unlock()
			for _, other := range slow {
				p.markSlow(other, time.Since(start))
			}
		}()
	}
}

// probe — запрос ". NS" к апстриму. При старте неудача помечает его упавшим;
// в периодической проверке он и так упавший — важен только успех.
func (p *Picker) probe(ctx context.Context, st *upstreamState, startup bool) bool {
	q := new(dns.Msg)
	q.SetQuestion(".", dns.TypeNS)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, rtt, err := exchangeTimed(cctx, st.ex, q)
	switch {
	case err == nil:
		if !startup {
			slog.Info("upstream recovered", "upstream", st.ex.Address())
		}
		p.markSuccess(st, rtt)
		return true
	case startup && ctx.Err() == nil:
		slog.Warn("upstream failed the startup probe", "upstream", st.ex.Address(), "err", err)
		p.markDown(st)
	}
	return false
}

// ExchangeTimed — exchangeTimed для пакетов снаружи: время самого обмена без
// дозвона, если апстрим его отдаёт.
func ExchangeTimed(ctx context.Context, ex Exchanger, m *dns.Msg) (*dns.Msg, time.Duration, error) {
	return exchangeTimed(ctx, ex, m)
}

// CapTTL ограничивает TTL всех записей ответа сверху.
func CapTTL(m *dns.Msg, maxTTL uint32) { capTTL(m, maxTTL) }

// SoftFail — SERVFAIL или REFUSED: ответ есть, но бесполезный.
func SoftFail(resp *dns.Msg) bool { return softFail(resp) }
