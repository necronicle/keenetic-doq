package upstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeUp struct {
	name     string
	fail     bool
	hang     bool // висит до отмены — как апстрим, до которого не доходит QUIC
	hangOnce bool // виснет только на первом вызове (мёртвое соединение после смены WAN)
	rcode    int
	delay    time.Duration
	slowNext atomic.Int64 // разовая добавка к задержке, нс
	n        atomic.Int64
}

func (f *fakeUp) Address() string { return f.name }
func (f *fakeUp) calls() int      { return int(f.n.Load()) }
func (f *fakeUp) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.n.Add(1)
	if f.fail {
		return nil, errors.New(f.name + " down")
	}
	if f.hang || (f.hangOnce && f.n.Load() == 1) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-time.After(f.delay + time.Duration(f.slowNext.Swap(0))):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	resp := new(dns.Msg)
	resp.SetReply(m)
	resp.Rcode = f.rcode
	return resp, nil
}

func query() *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	return m
}

func TestFailover(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	if _, err := p.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	if a.calls() != 1 || b.calls() != 1 {
		t.Errorf("calls a=%d b=%d, want 1/1", a.calls(), b.calls())
	}
}

func TestDownCooldownSkips(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	fake := time.Unix(1000000, 0)
	p.now = func() time.Time { return fake }
	p.Exchange(context.Background(), query()) // a падает, уходит в down
	p.Exchange(context.Background(), query()) // a в cooldown — не трогаем
	if a.calls() != 1 {
		t.Errorf("a.calls = %d, want 1 (in cooldown)", a.calls())
	}
	fake = fake.Add(31 * time.Second) // cooldown истёк
	a.fail = false
	p.Exchange(context.Background(), query())
	// Истёкший cooldown не возвращает упавший апстрим в начало очереди: иначе
	// мёртвый сервер раз в 30 с вставал бы первым и съедал запросы. Вернёт его
	// health check или запасной ход, когда откажут здоровые.
	if a.calls() != 1 {
		t.Errorf("a.calls = %d, want 1 (failed upstream stays behind healthy ones)", a.calls())
	}
	b.fail = true
	p.Exchange(context.Background(), query())
	if a.calls() != 2 {
		t.Errorf("a.calls = %d, want 2 (tried as a fallback)", a.calls())
	}
}

func TestAllDownStillTries(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	p := NewPicker([]Exchanger{a})
	p.Exchange(context.Background(), query()) // уходит в down
	if _, err := p.Exchange(context.Background(), query()); err == nil {
		t.Error("want error when all down")
	}
	if a.calls() != 2 {
		t.Errorf("a.calls = %d, want 2 (down upstreams are last resort, not skipped)", a.calls())
	}
}

func TestFastestFirst(t *testing.T) {
	slow := &fakeUp{name: "slow", delay: 30 * time.Millisecond}
	fast := &fakeUp{name: "fast", delay: time.Millisecond}
	p := NewPicker([]Exchanger{slow, fast})
	// прогрев: обоим даём по замеру (порядок конфигурации: slow первый)
	p.Exchange(context.Background(), query())
	slowCalls := slow.calls()
	// slow теперь имеет большой EWMA; следующие запросы должны идти в fast
	for i := 0; i < 3; i++ {
		p.Exchange(context.Background(), query())
	}
	if slow.calls() != slowCalls {
		t.Errorf("slow.calls grew to %d — fastest must be tried first", slow.calls())
	}
	if fast.calls() < 3 {
		t.Errorf("fast.calls = %d, want >=3", fast.calls())
	}
}

func TestHealthCheckRevives(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	p.Exchange(context.Background(), query()) // a в down
	a.fail = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthCheck(ctx, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !p.isDown(a) {
			return // ожил
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("health check did not revive upstream")
}

// fastPicker укорачивает тайминги выбора апстрима для тестов.
func fastPicker(t *testing.T) {
	t.Helper()
	oldA, oldU := attemptTimeout, unmeasuredHedge
	attemptTimeout, unmeasuredHedge = 500*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { attemptTimeout, unmeasuredHedge = oldA, oldU })
}

// Регрессия из ревью: живой апстрим первым, второй висит (QUIC режет
// провайдер). Раньше пачка одновременных запросов уходила в мёртвый, ждала
// общий бюджет и получала SERVFAIL, а живой помечался упавшим.
func TestHungUpstreamDoesNotStallBurst(t *testing.T) {
	fastPicker(t)
	live := &fakeUp{name: "live", delay: 5 * time.Millisecond}
	dead := &fakeUp{name: "dead", hang: true}
	p := NewPicker([]Exchanger{live, dead})
	if _, err := p.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			start := time.Now()
			if _, err := p.Exchange(ctx, query()); err != nil {
				t.Errorf("query failed: %v", err)
			}
			if d := time.Since(start); d > 400*time.Millisecond {
				t.Errorf("query took %v", d)
			}
		}()
	}
	wg.Wait()
	if p.isDown(live) {
		t.Error("live upstream was blamed")
	}
}

// Зависший апстрим проигрывает подстраховке и уходит за победителя, а не
// остаётся первым, заставляя каждый запрос ждать.
func TestHedgeLoserMovesBehindWinner(t *testing.T) {
	fastPicker(t)
	dead := &fakeUp{name: "dead", hang: true}
	live := &fakeUp{name: "live", delay: 5 * time.Millisecond}
	p := NewPicker([]Exchanger{dead, live})
	start := time.Now()
	if _, err := p.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*unmeasuredHedge {
		t.Errorf("first query took %v, want the hedge to answer after ~%v", d, unmeasuredHedge)
	}
	before := dead.calls()
	for i := 0; i < 3; i++ {
		p.Exchange(context.Background(), query())
	}
	if dead.calls() != before {
		t.Errorf("dead upstream still tried first (%d more calls)", dead.calls()-before)
	}
}

// У каждой попытки свой предел: зависший апстрим не съедает весь бюджет
// запроса и помечается упавшим.
func TestAttemptTimeoutMarksHungUpstreamDown(t *testing.T) {
	fastPicker(t)
	dead := &fakeUp{name: "dead", hang: true}
	p := NewPicker([]Exchanger{dead})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := p.Exchange(ctx, query()); err == nil {
		t.Fatal("want error")
	}
	// Две попытки (второй круг после таймаута), а не весь бюджет в 10 с.
	if d := time.Since(start); d > 3*attemptTimeout {
		t.Errorf("took %v, want about two attempt timeouts (%v each)", d, attemptTimeout)
	}
	if !p.isDown(dead) {
		t.Error("hung upstream not marked down")
	}
}

// Кончилось время самого запроса — апстрим не виноват.
func TestCallerDeadlineDoesNotBlameUpstream(t *testing.T) {
	fastPicker(t)
	// Ответ апстрима «кончилось время» и истечение самого запроса приходят
	// почти одновременно, и кто выиграет select — случай. Повторы ловят оба.
	for i := 0; i < 30; i++ {
		hung := &fakeUp{name: "hung", hang: true}
		p := NewPicker([]Exchanger{hung})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		if _, err := p.Exchange(ctx, query()); err == nil {
			t.Fatal("want error")
		}
		cancel()
		time.Sleep(time.Millisecond)
		if p.isDown(hung) {
			t.Fatalf("run %d: upstream blamed for the caller's own deadline", i)
		}
	}
}

// Health check возвращает упавший апстрим и после истечения cooldown.
func TestHealthCheckRevivesAfterCooldown(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	b := &fakeUp{name: "b"}
	p := NewPicker([]Exchanger{a, b})
	fake := time.Unix(1000000, 0)
	var mu sync.Mutex
	p.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return fake }
	p.Exchange(context.Background(), query())
	mu.Lock()
	fake = fake.Add(time.Minute)
	mu.Unlock()
	a.fail = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthCheck(ctx, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !p.isDown(a) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("health check did not revive the upstream")
}

// Быстрый SERVFAIL сломанного рекурсора не должен выигрывать у здорового
// апстрима; но если других ответов нет — отдаётся он, а не ошибка.
func TestServfailIsSoftFailure(t *testing.T) {
	fastPicker(t)
	broken := &fakeUp{name: "broken", rcode: dns.RcodeServerFailure}
	good := &fakeUp{name: "good", delay: 20 * time.Millisecond}
	p := NewPicker([]Exchanger{broken, good})
	resp, err := p.Exchange(context.Background(), query())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Errorf("rcode = %s, want the healthy upstream's NOERROR", dns.RcodeToString[resp.Rcode])
	}
	good.fail = true
	resp, err = p.Exchange(context.Background(), query())
	if err != nil {
		t.Fatalf("want the SERVFAIL answer when nothing better came, got %v", err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Errorf("rcode = %s, want SERVFAIL", dns.RcodeToString[resp.Rcode])
	}
}

// Один потерянный пакет не должен навсегда отодвинуть лучший апстрим за
// медленный: проигрыш подстраховке подмешивается в среднее, а не заменяет его.
func TestOneHedgeLossDoesNotDemoteBestUpstream(t *testing.T) {
	fastPicker(t)
	fast := &fakeUp{name: "fast", delay: 30 * time.Millisecond}
	slow := &fakeUp{name: "slow", delay: 250 * time.Millisecond}
	p := NewPicker([]Exchanger{fast, slow})
	for i := 0; i < 3; i++ {
		p.Exchange(context.Background(), query())
	}
	p.mu.Lock()
	p.ups[1].rtt = 250 * time.Millisecond // slow замерен
	p.mu.Unlock()
	fast.slowNext.Store(int64(600 * time.Millisecond)) // разовая потеря
	p.Exchange(context.Background(), query())          // выигрывает slow
	before := fast.calls()
	p.Exchange(context.Background(), query())
	if fast.calls() != before+1 {
		t.Error("best upstream demoted after a single hedge loss")
	}
}

// Время дозвона не входит в замер скорости апстрима.
type timedUp struct{ fakeUp }

func (f *timedUp) ExchangeTimed(ctx context.Context, m *dns.Msg) (*dns.Msg, time.Duration, error) {
	resp, err := f.Exchange(ctx, m)
	return resp, time.Millisecond, err // «обмен» — 1 мс, остальное — дозвон
}

func TestRTTSampleExcludesDial(t *testing.T) {
	up := &timedUp{fakeUp{name: "timed", delay: 100 * time.Millisecond}}
	p := NewPicker([]Exchanger{up})
	if _, err := p.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	rtt := p.ups[0].rtt
	p.mu.Unlock()
	if rtt != time.Millisecond {
		t.Errorf("rtt = %v, want the exchange time reported without the dial", rtt)
	}
}

// Единственный апстрим после смены WAN: первая попытка висит на мёртвом
// соединении, но в бюджете запроса есть время на вторую — по новому.
func TestSecondRoundAfterTimeout(t *testing.T) {
	fastPicker(t)
	up := &fakeUp{name: "lone", hangOnce: true}
	p := NewPicker([]Exchanger{up})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.Exchange(ctx, query()); err != nil {
		t.Fatalf("want success on the second round, got %v", err)
	}
	if up.calls() != 2 {
		t.Errorf("calls = %d, want 2", up.calls())
	}
}

// При старте демона все апстримы пробуются сразу: мёртвый не должен ждать
// первого клиентского запроса, чтобы оказаться в конце очереди.
func TestStartupProbeSortsUpstreams(t *testing.T) {
	fastPicker(t)
	dead := &fakeUp{name: "dead", fail: true}
	live := &fakeUp{name: "live", delay: 5 * time.Millisecond}
	p := NewPicker([]Exchanger{dead, live})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthCheck(ctx, time.Hour)
	deadline := time.Now().Add(2 * time.Second)
	for !p.isDown(dead) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.isDown(dead) {
		t.Fatal("dead upstream not detected at startup")
	}
	before := dead.calls()
	p.Exchange(context.Background(), query())
	if dead.calls() != before {
		t.Error("dead upstream tried first after the startup probe")
	}
}

// Мёртвый апстрим, который при старте молчит (а не отказывает сразу), уходит
// за живого, как только тот ответил, — не дожидаясь своего таймаута.
func TestStartupProbeDemotesSilentUpstream(t *testing.T) {
	fastPicker(t)
	dead := &fakeUp{name: "dead", hang: true}
	live := &fakeUp{name: "live", delay: 5 * time.Millisecond}
	p := NewPicker([]Exchanger{dead, live})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthCheck(ctx, time.Hour)
	time.Sleep(100 * time.Millisecond) // live ответил, dead ещё висит
	before := dead.calls()
	start := time.Now()
	if _, err := p.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > unmeasuredHedge/2 {
		t.Errorf("query took %v: silent upstream was still tried first", d)
	}
	if dead.calls() != before {
		t.Error("silent upstream tried first after the startup probe")
	}
}

// Апстрим со сломанным рекурсором отвечает SERVFAIL быстро. Проиграв
// полезному ответу, он уходит за здоровый, а не остаётся первым навсегда.
func TestFastServfailUpstreamIsDemoted(t *testing.T) {
	fastPicker(t)
	broken := &fakeUp{name: "broken", rcode: dns.RcodeServerFailure}
	good := &fakeUp{name: "good", delay: 20 * time.Millisecond}
	p := NewPicker([]Exchanger{broken, good})
	p.Exchange(context.Background(), query())
	before := broken.calls()
	for i := 0; i < 3; i++ {
		p.Exchange(context.Background(), query())
	}
	if broken.calls() != before {
		t.Errorf("broken upstream still asked first (%d more calls)", broken.calls()-before)
	}
}
