package geo

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"time"

	"github.com/miekg/dns"
)

// Start поднимает гео-полосу: сохранённый выбор или первая оценка, затем
// фоновый цикл проверок прокси и перевыборов. Без серверов обхода — ничего.
func (l *Lanes) Start(ctx context.Context) {
	if len(l.cfg.Geo) == 0 {
		return
	}
	st, err := LoadState(l.cfg.StatePath)
	switch {
	case err == nil && l.has(st.Pinned):
		l.mu.Lock()
		l.pinned, l.since, l.evaluatedAt, l.ranking = st.Pinned, st.Since, st.EvaluatedAt, st.Ranking
		l.saved = st
		l.mu.Unlock()
		l.applyRanking(st.Ranking)
		slog.Info("geo: pinned server restored", "server", st.Pinned, "since", st.Since)
	case err == nil:
		l.Reselect("saved server is not in the config")
	default:
		if !os.IsNotExist(err) {
			slog.Warn("geo: cannot read state, re-evaluating", "path", l.cfg.StatePath, "err", err)
		}
		l.Reselect("first start")
	}
	l.loopDone = make(chan struct{})
	go func() {
		defer close(l.loopDone)
		l.loop(ctx)
	}()
}

func (l *Lanes) has(url string) bool {
	_, ok := l.server(url)
	return ok
}

// applyRanking переносит отпечатки пулов в классификатор, а пул закреплённого —
// в проверку прокси.
func (l *Lanes) applyRanking(rs []Result) {
	for _, r := range rs {
		l.prints.Set(r.URL, r.Pool())
	}
	l.health.Reset(poolSNI(l.Pinned(), rs))
}

func (l *Lanes) loop(ctx context.Context) {
	t := newTicker(l.interval)
	defer t.Stop()
	// Повтор оценки, не давшей охвата (сеть лежала): 1 мин, удваивается до 30 мин.
	var retry *time.Timer
	var retryC <-chan time.Time
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	evaluate := func(reason string) {
		if retry != nil {
			retry.Stop()
			retry, retryC = nil, nil
		}
		if d := l.evaluate(ctx, reason); d > 0 {
			retry = time.NewTimer(d)
			retryC = retry.C
		}
	}
	l.writeSnapshot()
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-l.reselect:
			evaluate(reason)
		case <-retryC:
			retry, retryC = nil, nil
			evaluate("retry after an inconclusive evaluation")
		case <-t.C:
			l.checkProxies(ctx)
		}
		l.writeSnapshot()
	}
}

// evaluate оценивает все серверы обхода и закрепляет лучший. Оценка, в которой
// ни у кого нет охвата, — безрезультатная (обычно лежит сеть): рейтинг,
// отпечатки, закреплённый и geo.state остаются прежними. Возвращает отсрочку
// повтора; 0 — повтор не нужен.
func (l *Lanes) evaluate(ctx context.Context, reason string) time.Duration {
	l.mu.Lock()
	l.evaluating = true
	l.mu.Unlock()
	l.writeSnapshot()
	slog.Info("geo: evaluating unblocking servers", "reason", reason)
	rs := l.ev.Run(ctx, l.cfg.Geo)
	if ctx.Err() != nil {
		// Остановка посреди оценки: результаты пусты, их нельзя сохранять.
		l.mu.Lock()
		l.evaluating = false
		l.mu.Unlock()
		slog.Info("geo: evaluation cancelled", "reason", reason)
		return 0
	}
	conclusive := false
	for _, r := range rs {
		conclusive = conclusive || r.Coverage > 0
		slog.Info("geo: evaluated", "server", r.URL, "coverage", r.Coverage, "proxies_alive", r.Alive,
			"proxies", r.Total, "median_tls_ms", r.MedianTLSMs, "err", r.Err)
	}
	l.mu.Lock()
	l.evaluating = false
	l.attemptedAt = l.now()
	if !conclusive {
		if l.retryDelay == 0 {
			l.retryDelay = l.retryBase
		} else {
			l.retryDelay = min(2*l.retryDelay, l.retryMax)
		}
		d, pinned := l.retryDelay, l.pinned
		l.mu.Unlock()
		slog.Warn("geo: evaluation inconclusive: no server unblocked any probe domain (network down?); "+
			"keeping the pinned server and the previous ranking", "reason", reason, "pinned", pinned, "retry_in", d)
		return d
	}
	l.retryDelay = 0
	l.ranking = rs
	l.evaluatedAt = l.attemptedAt
	l.mu.Unlock()
	for _, r := range rs {
		l.prints.Set(r.URL, r.Pool())
	}
	// Рейтинг начинается с охвата, так что первый — с охватом.
	l.pin(rs[0].URL, "evaluation: "+reason)
	return 0
}

// checkProxies — цикл проверки прокси закреплённого; все мертвы три цикла
// подряд — отказ.
func (l *Lanes) checkProxies(ctx context.Context) {
	l.health.CheckAll(ctx)
	if ctx.Err() != nil {
		return // проверка прервана остановкой — не засчитывать
	}
	l.flushBad()
	l.mu.Lock()
	if !l.health.AllDead() {
		l.deadCycles = 0
		l.mu.Unlock()
		return
	}
	l.deadCycles++
	trip := l.deadCycles == deadCyclesToFailover
	l.mu.Unlock()
	if trip {
		l.failover("every proxy of the pinned server is dead")
	}
}

// flushBad убирает из кеша ответы с адресами, ставшими мёртвыми или медленными:
// иначе клиенты получали бы их до конца TTL. l.mu не держится.
func (l *Lanes) flushBad() {
	bad := l.health.BadAddrs()
	if len(bad) == 0 {
		return
	}
	set := make(map[netip.Addr]struct{}, len(bad))
	for _, a := range bad {
		set[a] = struct{}{}
	}
	n := l.cfg.Flush(func(m *dns.Msg) bool {
		ips, _ := answerAddrs(m)
		for _, x := range ips {
			if _, ok := set[x]; ok {
				return true
			}
		}
		return false
	})
	if n > 0 {
		slog.Info("geo: dropped cached answers with dead or slow proxies", "entries", n)
	}
}

type ticker struct {
	C    <-chan time.Time
	stop func()
}

func (t ticker) Stop() { t.stop() }

func newTicker(d time.Duration) ticker {
	if d <= 0 {
		return ticker{C: nil, stop: func() {}}
	}
	tk := time.NewTicker(d)
	return ticker{C: tk.C, stop: tk.Stop}
}
