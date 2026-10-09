package geo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func attempted(l *Lanes) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.attemptedAt.IsZero() && !l.evaluating
}

func retryDelay(l *Lanes) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retryDelay
}

// WAN лёг: серверы обхода не отвечают, оценка даёт нулевой охват у всех.
func TestInconclusiveEvaluationKeepsPreviousChoice(t *testing.T) {
	l, g1, g2, _ := lifecycleFixture(t)
	l.retryBase, l.retryMax = 50*time.Millisecond, 120*time.Millisecond
	saved := &State{Pinned: "g2", Since: time.Now().Add(-time.Hour), Ranking: savedRanking()}
	if err := SaveState(l.cfg.StatePath, saved); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(l.cfg.StatePath)
	start(t, l)
	g1.setErr(errors.New("network is unreachable"))
	g2.setErr(errors.New("network is unreachable"))
	l.Reselect("pinned server failed 3 queries in a row")
	waitFor(t, func() bool { return attempted(l) }, 5*time.Second)
	if l.Pinned() != "g2" {
		t.Fatalf("pin must stay, got %s", l.Pinned())
	}
	if !l.prints.MatchAny(aMsg("a.", "1.1.1.1")) || !l.prints.MatchAny(aMsg("a.", "2.2.2.2")) {
		t.Fatal("fingerprints must survive an inconclusive evaluation")
	}
	l.mu.Lock()
	ranking := l.ranking
	l.mu.Unlock()
	if !reflect.DeepEqual(ranking, savedRanking()) {
		t.Fatalf("ranking must survive, got %+v", ranking)
	}
	if after, _ := os.ReadFile(l.cfg.StatePath); !bytes.Equal(before, after) {
		t.Fatal("geo.state must not be rewritten by an inconclusive evaluation")
	}
	// Повтор по расписанию: 50 мс, затем 100, потом потолок 120.
	waitFor(t, func() bool { return retryDelay(l) == 120*time.Millisecond }, 5*time.Second)
	// Сеть вернулась — следующий повтор доводит оценку до конца и сбрасывает отсрочку.
	g1.setErr(nil)
	g2.setErr(nil)
	waitFor(t, func() bool { return evaluated(l) && retryDelay(l) == 0 }, 5*time.Second)
}

func TestFirstStartInconclusiveSavesNothing(t *testing.T) {
	l, g1, g2, _ := lifecycleFixture(t)
	l.retryBase, l.retryMax = time.Hour, time.Hour
	g1.setErr(errors.New("down"))
	g2.setErr(errors.New("down"))
	start(t, l)
	waitFor(t, func() bool { return attempted(l) }, 5*time.Second)
	if l.Pinned() != "g1" {
		t.Fatalf("temporary pin = first config server, got %s", l.Pinned())
	}
	if _, err := os.Stat(l.cfg.StatePath); !os.IsNotExist(err) {
		t.Fatalf("nothing to save after an inconclusive first evaluation: %v", err)
	}
	if retryDelay(l) != time.Hour {
		t.Fatalf("retry must be scheduled, delay = %v", retryDelay(l))
	}
}

func TestFailoverWithNetworkDownKeepsPin(t *testing.T) {
	fast := newFake("fast", nil)
	fast.setErr(errors.New("network is unreachable"))
	g1 := newFake("g1", nil)
	g1.setErr(errors.New("network is unreachable"))
	g2 := newFake("g2", nil)
	g2.setErr(errors.New("network is unreachable"))
	l, _ := newTestLanes(t, fast, nil, g1, g2)
	l.ranking = []Result{{URL: "g1", Coverage: 1, Alive: 1}, {URL: "g2", Coverage: 1, Alive: 1}}
	m := new(dns.Msg)
	m.SetQuestion("chatgpt.com.", dns.TypeA)
	for i := 0; i < 3; i++ {
		l.Exchange(context.Background(), m)
	}
	waitFor(t, func() bool { return fast.count() >= 4 }, 2*time.Second) // 3 запроса + проверка сети
	time.Sleep(100 * time.Millisecond)
	if l.Pinned() != "g1" {
		t.Fatalf("network down: the pinned server is not at fault, got %s", l.Pinned())
	}
	if l.Snapshot().Fails != 0 {
		t.Fatal("failure streak must be reset")
	}
	if len(l.reselect) != 0 {
		t.Fatal("no re-evaluation while the network is down")
	}
}

func TestDeadProxiesWithNetworkDownKeepPin(t *testing.T) {
	l, _, _, p := lifecycleFixture(t)
	fast := newFake("fast", nil)
	fast.setErr(errors.New("network is unreachable"))
	l.cfg.Fast = fast
	SaveState(l.cfg.StatePath, &State{Pinned: "g1", Since: time.Now(), Ranking: []Result{
		savedRanking()[1], savedRanking()[0]}})
	start(t, l)
	p.setDead("1.1.1.1", true)
	for i := 0; i < 4; i++ {
		l.checkProxies(context.Background())
	}
	time.Sleep(100 * time.Millisecond)
	if l.Pinned() != "g1" || fast.count() == 0 {
		t.Fatalf("network down: pin must stay after a network check, got %s (checks %d)", l.Pinned(), fast.count())
	}
}

func TestPinUnchangedDoesNotRewriteState(t *testing.T) {
	l, _, _, _ := lifecycleFixture(t)
	SaveState(l.cfg.StatePath, &State{Pinned: "g2", Since: time.Now(), Ranking: savedRanking()})
	start(t, l)
	os.Remove(l.cfg.StatePath)
	l.pin("g2", "evaluation: same result")
	if _, err := os.Stat(l.cfg.StatePath); !os.IsNotExist(err) {
		t.Fatal("same pin and ranking: geo.state must not be rewritten")
	}
	l.pin("g1", "manual")
	if st, err := LoadState(l.cfg.StatePath); err != nil || st.Pinned != "g1" {
		t.Fatalf("a changed pin must be saved: %+v, %v", st, err)
	}
}
