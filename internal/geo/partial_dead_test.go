package geo

import (
	"context"
	"testing"
	"time"
)

// Пул закреплённого, где мёртв один прокси из двух: Bezmezhau 21.10.2026.
// Имена, подменяемые на мёртвый адрес, уходили в никуда, а отказ не
// срабатывал — он ждал смерти всех прокси.

// splitAnswers: чётные пробные имена → aliveIP, нечётные → deadIP.
func splitAnswers(aliveIP, deadIP string) map[string][]string {
	m := map[string][]string{}
	for i, d := range ProbeDomains {
		if i%2 == 0 {
			m[d] = []string{aliveIP}
		} else {
			m[d] = []string{deadIP}
		}
	}
	return m
}

// pairAnswers: каждое пробное имя → оба адреса.
func pairAnswers(a, b string) map[string][]string {
	m := map[string][]string{}
	for _, d := range ProbeDomains {
		m[d] = []string{a, b}
	}
	return m
}

// twoProxyState: закреплён g1 с пулом 1.1.1.1 + 1.1.1.2, g2 — запасной.
func twoProxyState(t *testing.T, l *Lanes) {
	t.Helper()
	g1 := Result{URL: "g1", Coverage: len(ProbeDomains), Covered: coveredAll(), Alive: 2, Total: 2,
		PoolIPs: []string{"1.1.1.1", "1.1.1.2"}, SNI: map[string]string{"1.1.1.1": "chatgpt.com", "1.1.1.2": "claude.ai"}}
	if err := SaveState(l.cfg.StatePath, &State{Pinned: "g1", Since: time.Now(), Probes: ProbeDomains,
		Ranking: []Result{g1, savedRanking()[0]}}); err != nil {
		t.Fatal(err)
	}
}

func attemptedAt(l *Lanes) time.Time { l.mu.Lock(); defer l.mu.Unlock(); return l.attemptedAt }

func TestHalfDeadPoolSwitchesToFullCoverage(t *testing.T) {
	l, _, _, p := lifecycleFixture(t)
	l.cfg.Geo[0].Ex = newFake("g1", splitAnswers("1.1.1.1", "1.1.1.2"))
	twoProxyState(t, l)
	start(t, l)
	if l.Pinned() != "g1" {
		t.Fatalf("pinned = %s", l.Pinned())
	}
	p.setDead("1.1.1.2", true)
	for i := 0; i < 4; i++ { // 1-я — ещё жив; 2–4 — мёртв три проверки → переоценка
		l.checkProxies(context.Background())
	}
	waitFor(t, func() bool { return evaluated(l) && l.Pinned() == "g2" }, 5*time.Second)
}

// Мёртвый прокси, рядом с которым в каждом ответе есть живой, покрытия не
// отнимает: переоценка идёт, но закреплённый остаётся — смена сервера
// обхода меняет IP выхода, и сессии у пользователей слетают зря.
func TestHalfDeadPoolKeepsPinnedWhileCovered(t *testing.T) {
	l, _, _, p := lifecycleFixture(t)
	l.cfg.Geo[0].Ex = newFake("g1", pairAnswers("1.1.1.1", "1.1.1.2"))
	twoProxyState(t, l)
	start(t, l)
	p.setDead("1.1.1.2", true)
	for i := 0; i < 4; i++ {
		l.checkProxies(context.Background())
	}
	waitFor(t, func() bool { return !attemptedAt(l).IsZero() && !evaluatedBusy(l) }, 5*time.Second)
	if l.Pinned() != "g1" {
		t.Fatalf("a still-covering server must stay pinned, got %s", l.Pinned())
	}
}

// Пока мёртв тот же набор прокси, переоценка не повторяется каждые три
// проверки.
func TestHalfDeadPoolReevaluatesOnce(t *testing.T) {
	l, _, _, p := lifecycleFixture(t)
	l.cfg.Geo[0].Ex = newFake("g1", pairAnswers("1.1.1.1", "1.1.1.2"))
	twoProxyState(t, l)
	start(t, l)
	p.setDead("1.1.1.2", true)
	for i := 0; i < 4; i++ {
		l.checkProxies(context.Background())
	}
	waitFor(t, func() bool { return !attemptedAt(l).IsZero() && !evaluatedBusy(l) }, 5*time.Second)
	first := attemptedAt(l)
	for i := 0; i < 6; i++ {
		l.checkProxies(context.Background())
	}
	time.Sleep(300 * time.Millisecond)
	if at := attemptedAt(l); !at.Equal(first) {
		t.Fatalf("re-evaluated again for the same dead set: %v → %v", first, at)
	}
}
