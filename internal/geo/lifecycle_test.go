package geo

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// lifecycleFixture: g1 отдаёт прокси 1.1.1.1 (900 мс), g2 — 2.2.2.2 (100 мс),
// обычный резолвер — настоящие адреса.
func lifecycleFixture(t *testing.T) (*Lanes, *fakeEx, *fakeEx, *fakeProber) {
	t.Helper()
	ref := newFake("ref", map[string][]string{"chatgpt.com.": {"7.7.7.7"}, "claude.ai.": {"7.7.7.8"},
		"gemini.google.com.": {"7.7.7.9"}})
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	g2 := newFake("g2", probeAnswers("2.2.2.2"))
	p := &fakeProber{lat: map[string]time.Duration{"1.1.1.1": 900 * time.Millisecond, "2.2.2.2": 100 * time.Millisecond}}
	dir := t.TempDir()
	l := New(Config{Fast: ref, Reference: ref, Prober: p,
		Geo:       []Server{{URL: "g1", Ex: g1}, {URL: "g2", Ex: g2}},
		StatePath: filepath.Join(dir, "geo.state"), SnapshotPath: filepath.Join(dir, "snap.json")})
	l.interval = time.Hour
	return l, g1, g2, p
}

func start(t *testing.T, l *Lanes) {
	ctx, cancel := context.WithCancel(context.Background())
	l.Start(ctx)
	// Цикл дописывает состояние уже после отмены; дождаться его выхода до
	// удаления временного каталога.
	t.Cleanup(func() {
		cancel()
		if l.loopDone != nil {
			<-l.loopDone
		}
	})
}

func evaluated(l *Lanes) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.evaluatedAt.IsZero() && !l.evaluating
}

func TestStartEvaluatesAndPinsBest(t *testing.T) {
	l, _, _, _ := lifecycleFixture(t)
	start(t, l)
	waitFor(t, func() bool { return evaluated(l) && l.Pinned() == "g2" }, 5*time.Second)
	st, err := LoadState(l.cfg.StatePath)
	if err != nil || st.Pinned != "g2" || len(st.Ranking) != 2 || st.Ranking[0].URL != "g2" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if !l.prints.MatchAny(aMsg("a.", "1.1.1.1")) || !l.prints.MatchAny(aMsg("a.", "2.2.2.2")) {
		t.Fatal("every server's pool must be fingerprinted")
	}
	if s, err := ReadSnapshot(l.cfg.SnapshotPath); err != nil || s.Pinned != "g2" {
		t.Fatalf("snapshot = %+v, %v", s, err)
	}
}

func savedRanking() []Result {
	return []Result{
		{URL: "g2", Coverage: 3, Alive: 1, Total: 1, PoolIPs: []string{"2.2.2.2"}, SNI: map[string]string{"2.2.2.2": "chatgpt.com"}},
		{URL: "g1", Coverage: 3, Alive: 1, Total: 1, PoolIPs: []string{"1.1.1.1"}, SNI: map[string]string{"1.1.1.1": "chatgpt.com"}},
	}
}

func TestStartUsesSavedState(t *testing.T) {
	l, g1, g2, _ := lifecycleFixture(t)
	if err := SaveState(l.cfg.StatePath, &State{Pinned: "g2", Since: time.Now(), Ranking: savedRanking()}); err != nil {
		t.Fatal(err)
	}
	start(t, l)
	if l.Pinned() != "g2" {
		t.Fatalf("pinned = %s", l.Pinned())
	}
	time.Sleep(200 * time.Millisecond)
	if g1.count() != 0 || g2.count() != 0 {
		t.Fatal("a saved pin must not trigger an evaluation")
	}
	if !l.prints.MatchAny(aMsg("a.", "1.1.1.1")) {
		t.Fatal("saved fingerprints must be restored")
	}
}

func TestSavedPinMissingFromConfigReevaluates(t *testing.T) {
	l, _, _, _ := lifecycleFixture(t)
	SaveState(l.cfg.StatePath, &State{Pinned: "gone", Ranking: savedRanking()})
	start(t, l)
	waitFor(t, func() bool { return evaluated(l) && l.Pinned() == "g2" }, 5*time.Second)
}

func TestNoCoverageKeepsFirstServer(t *testing.T) {
	l, g1, g2, _ := lifecycleFixture(t)
	for _, g := range []*fakeEx{g1, g2} {
		g.mu.Lock()
		g.ips = map[string][]string{"chatgpt.com.": {"7.7.7.7"}, "claude.ai.": {"7.7.7.8"}, "gemini.google.com.": {"7.7.7.9"}}
		g.mu.Unlock()
	}
	start(t, l)
	waitFor(t, func() bool { return evaluated(l) }, 5*time.Second)
	if l.Pinned() != "g1" {
		t.Fatalf("no coverage anywhere — keep the first config server, got %s", l.Pinned())
	}
	if st, err := LoadState(l.cfg.StatePath); err != nil || st.Pinned != "g1" {
		t.Fatalf("state = %+v, %v", st, err)
	}
}

func TestDeadProxiesFailOver(t *testing.T) {
	l, _, _, p := lifecycleFixture(t)
	SaveState(l.cfg.StatePath, &State{Pinned: "g1", Since: time.Now(), Ranking: []Result{
		savedRanking()[1], savedRanking()[0]}})
	start(t, l)
	p.setDead("1.1.1.1", true)
	for i := 0; i < 4; i++ { // 1-я — ещё жив; 2–4 — мёртв, deadCycles 1→2→3 → отказ
		l.checkProxies(context.Background())
	}
	waitFor(t, func() bool { return l.Pinned() == "g2" }, 2*time.Second)
}

func TestReselectReevaluates(t *testing.T) {
	l, _, _, _ := lifecycleFixture(t)
	SaveState(l.cfg.StatePath, &State{Pinned: "g1", Since: time.Now(), Ranking: []Result{
		savedRanking()[1], savedRanking()[0]}})
	start(t, l)
	l.Reselect("manual")
	waitFor(t, func() bool { return evaluated(l) && l.Pinned() == "g2" }, 5*time.Second)
}

func TestNoGeoStartIsNoop(t *testing.T) {
	dir := t.TempDir()
	fast := newFake("fast", nil)
	l := New(Config{Fast: fast, StatePath: filepath.Join(dir, "geo.state"), SnapshotPath: filepath.Join(dir, "snap.json")})
	start(t, l)
	m := new(dns.Msg)
	m.SetQuestion("chatgpt.com.", dns.TypeA)
	l.Exchange(context.Background(), m)
	time.Sleep(100 * time.Millisecond)
	for _, f := range []string{l.cfg.StatePath, l.cfg.SnapshotPath} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s must not be written without geo servers", f)
		}
	}
}

func TestCancelledEvaluationIsNotPersisted(t *testing.T) {
	l, g1, g2, _ := lifecycleFixture(t)
	g1.setDelay(2 * time.Second)
	g2.setDelay(2 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	l.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-l.loopDone
	if _, err := os.Stat(l.cfg.StatePath); !os.IsNotExist(err) {
		t.Fatalf("cancelled evaluation must not write state: %v", err)
	}
	// Второй запуск на том же файле состояния обязан оценить заново.
	g1.setDelay(0)
	g2.setDelay(0)
	l2 := New(Config{Fast: l.cfg.Fast, Reference: l.cfg.Reference, Prober: l.cfg.Prober, Geo: l.cfg.Geo,
		StatePath: l.cfg.StatePath, SnapshotPath: l.cfg.SnapshotPath})
	l2.interval = time.Hour
	start(t, l2)
	waitFor(t, func() bool { return evaluated(l2) && l2.Pinned() == "g2" }, 5*time.Second)
}

func TestCorruptStateReevaluates(t *testing.T) {
	l, _, _, _ := lifecycleFixture(t)
	if err := os.WriteFile(l.cfg.StatePath, []byte("\x00garbage{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	start(t, l)
	waitFor(t, func() bool { return evaluated(l) && l.Pinned() == "g2" }, 5*time.Second)
}

func TestCheckProxiesFlushesBadAddresses(t *testing.T) {
	p := &fakeProber{}
	g1 := newFake("g1", map[string][]string{"chatgpt.com.": {"1.1.1.1", "5.5.5.5"}})
	l, rec := newTestLanes(t, newFake("fast", nil), p, g1)
	l.health.Reset(map[netip.Addr]string{mustAddr("1.1.1.1"): "chatgpt.com", mustAddr("5.5.5.5"): "chatgpt.com"})
	ctx := context.Background()
	l.checkProxies(ctx)
	if len(rec.preds()) != 0 {
		t.Fatal("nothing is bad, nothing to flush")
	}
	p.setDead("5.5.5.5", true)
	l.checkProxies(ctx)
	l.checkProxies(ctx) // second failure in a row = dead
	preds := rec.preds()
	if len(preds) == 0 {
		t.Fatal("a proxy turned dead: cached answers with it must be flushed")
	}
	pred := preds[len(preds)-1]
	if !pred(aMsg("chatgpt.com", "1.1.1.1", "5.5.5.5")) {
		t.Fatal("answer with the dead address must match")
	}
	if pred(aMsg("chatgpt.com", "1.1.1.1")) {
		t.Fatal("answer without the dead address must not match")
	}
}
