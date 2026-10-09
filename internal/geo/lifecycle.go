package geo

import (
	"context"
	"log/slog"
	"os"
	"time"
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
	l.writeSnapshot()
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-l.reselect:
			l.evaluate(ctx, reason)
		case <-t.C:
			l.checkProxies(ctx)
		}
		l.writeSnapshot()
	}
}

// evaluate оценивает все серверы обхода и закрепляет лучший с охватом; без
// охвата у всех остаётся текущий.
func (l *Lanes) evaluate(ctx context.Context, reason string) {
	l.mu.Lock()
	l.evaluating = true
	l.mu.Unlock()
	l.writeSnapshot()
	slog.Info("geo: evaluating unblocking servers", "reason", reason)
	rs := l.ev.Run(ctx, l.cfg.Geo)
	for _, r := range rs {
		l.prints.Set(r.URL, r.Pool())
		slog.Info("geo: evaluated", "server", r.URL, "coverage", r.Coverage, "proxies_alive", r.Alive,
			"proxies", r.Total, "median_tls_ms", r.MedianTLSMs, "err", r.Err)
	}
	l.mu.Lock()
	l.evaluating = false
	l.ranking = rs
	l.evaluatedAt = l.now()
	winner := l.pinned
	l.mu.Unlock()
	if len(rs) > 0 && rs[0].Coverage > 0 {
		winner = rs[0].URL
	}
	l.pin(winner, "evaluation: "+reason)
}

// checkProxies — цикл проверки прокси закреплённого; все мертвы три цикла
// подряд — отказ.
func (l *Lanes) checkProxies(ctx context.Context) {
	l.health.CheckAll(ctx)
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
