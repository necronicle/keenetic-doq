package resolver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
)

type fakeUp struct {
	calls atomic.Int64
	err   error         // вернуть ошибку
	rcode int           // вернуть ответ с этим кодом
	delay time.Duration // задержка ответа
	// extraOpts — опции, которые апстрим кладёт в свой OPT.
	extraOpts []dns.EDNS0
}

func (f *fakeUp) Address() string { return "fake" }
func (f *fakeUp) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	resp := new(dns.Msg)
	resp.SetReply(m)
	resp.Rcode = f.rcode
	if o := m.IsEdns0(); o != nil { // настоящий апстрим отвечает на EDNS своим OPT
		resp.SetEdns0(o.UDPSize(), o.Do())
		resp.IsEdns0().Option = append(resp.IsEdns0().Option, f.extraOpts...)
	}
	rr, _ := dns.NewRR(m.Question[0].Name + " 300 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp, nil
}

func TestCacheMissThenHit(t *testing.T) {
	up := &fakeUp{}
	r := New(cache.New(16, time.Second, time.Hour), up)
	q1 := new(dns.Msg)
	q1.SetQuestion("example.com.", dns.TypeA)
	q1.Id = 1
	resp1, err := r.Resolve(context.Background(), q1)
	if err != nil || len(resp1.Answer) != 1 {
		t.Fatalf("resp1=%v err=%v", resp1, err)
	}
	q2 := new(dns.Msg)
	q2.SetQuestion("example.com.", dns.TypeA)
	q2.Id = 2
	resp2, err := r.Resolve(context.Background(), q2)
	if err != nil {
		t.Fatal(err)
	}
	if up.calls.Load() != 1 {
		t.Errorf("upstream calls = %d, want 1 (second must be cache hit)", up.calls.Load())
	}
	if resp2.Id != 2 {
		t.Errorf("resp2.Id = %d, want 2 (request ID)", resp2.Id)
	}
}

func ask(r *Resolver, name string, id uint16) (*dns.Msg, error) {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = id
	return r.Resolve(context.Background(), q)
}

// Апстримы недоступны — лучше вчерашний адрес, чем SERVFAIL (RFC 8767).
func TestServeStaleWhenUpstreamsFail(t *testing.T) {
	up := &fakeUp{}
	c := cache.New(16, time.Millisecond, time.Millisecond) // запись тут же протухнет
	r := New(c, up)
	if _, err := ask(r, "example.com.", 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	up.err = errors.New("all upstreams failed")
	resp, err := ask(r, "example.com.", 2)
	if err != nil {
		t.Fatalf("want stale answer, got %v", err)
	}
	if resp.Id != 2 || len(resp.Answer) != 1 {
		t.Errorf("stale resp = %v", resp)
	}
}

// SERVFAIL от апстрима — тоже повод отдать устаревший ответ.
func TestServeStaleOnServfail(t *testing.T) {
	up := &fakeUp{}
	r := New(cache.New(16, time.Millisecond, time.Millisecond), up)
	ask(r, "example.com.", 1)
	time.Sleep(5 * time.Millisecond)
	up.rcode = dns.RcodeServerFailure
	resp, err := ask(r, "example.com.", 2)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Errorf("want stale NOERROR, got rcode %d with %d answers", resp.Rcode, len(resp.Answer))
	}
}

// Одинаковые одновременные запросы уходят к апстриму один раз.
func TestCoalescesIdenticalQueries(t *testing.T) {
	up := &fakeUp{delay: 100 * time.Millisecond}
	r := New(cache.New(16, time.Second, time.Hour), up)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id uint16) {
			defer wg.Done()
			resp, err := ask(r, "example.com.", id)
			if err != nil {
				t.Error(err)
				return
			}
			if resp.Id != id {
				t.Errorf("resp.Id = %d, want %d", resp.Id, id)
			}
		}(uint16(i + 1))
	}
	wg.Wait()
	if n := up.calls.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want 1", n)
	}
}

// Секция вопроса повторяет запрос как есть, вплоть до регистра: так проверяют
// ответ клиенты с 0x20-рандомизацией.
func TestAnswerEchoesQuestionCase(t *testing.T) {
	r := New(cache.New(16, time.Second, time.Hour), &fakeUp{})
	ask(r, "example.com.", 1)
	resp, err := ask(r, "ExAmPlE.CoM.", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Question[0].Name; got != "ExAmPlE.CoM." {
		t.Errorf("question = %q, want the request's own case", got)
	}
}

// RFC 8767: апстрим тянет, а в кеше есть устаревший ответ — клиент получает
// его через staleAnswerDelay, обновление продолжается в фоне.
func TestStaleAnsweredWhileUpstreamIsSlow(t *testing.T) {
	old := staleAnswerDelay
	staleAnswerDelay = 100 * time.Millisecond
	t.Cleanup(func() { staleAnswerDelay = old })
	up := &fakeUp{}
	c := cache.New(16, time.Millisecond, time.Millisecond)
	r := New(c, up)
	ask(r, "example.com.", 1)
	time.Sleep(5 * time.Millisecond)
	up.delay = 500 * time.Millisecond
	start := time.Now()
	resp, err := ask(r, "example.com.", 2)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("stale answer after %v, want about %v", d, staleAnswerDelay)
	}
	if len(resp.Answer) != 1 || resp.Answer[0].Header().Ttl != 30 {
		t.Errorf("want the stale answer with TTL 30, got %v", resp.Answer)
	}
	time.Sleep(600 * time.Millisecond) // фоновое обновление дошло до кеша
	if n := up.calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2 (one background refresh)", n)
	}
}

// Запрос, присоединившийся к уже идущему, не умирает вместе с дедлайном
// первого: общий запрос живёт по своим часам.
func TestFollowerOutlivesLeaderDeadline(t *testing.T) {
	up := &fakeUp{delay: 200 * time.Millisecond}
	r := New(cache.New(16, time.Second, time.Hour), up)
	leaderDone := make(chan error)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		_, err := r.Resolve(ctx, q)
		leaderDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := ask(r, "example.com.", 2); err != nil {
		t.Errorf("follower failed with the leader's deadline: %v", err)
	}
	if err := <-leaderDone; err == nil {
		t.Error("leader should have hit its own 50 ms deadline")
	}
}

// RFC 6891: OPT в ответе — ровно когда он был в запросе. В кеше ключ общий
// (DO и CD совпадают), так что ответ на запрос с EDNS может достаться
// клиенту без EDNS, и наоборот.
func TestOPTFollowsTheRequest(t *testing.T) {
	r := New(cache.New(16, time.Second, time.Hour), &fakeUp{})
	query := func(name string, edns bool) *dns.Msg {
		q := new(dns.Msg)
		q.SetQuestion(name, dns.TypeA)
		if edns {
			q.SetEdns0(4096, false)
		}
		resp, err := r.Resolve(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// В кеше ответ с OPT — клиенту без EDNS его OPT не положен.
	if query("a.example.", true).IsEdns0() == nil {
		t.Error("EDNS query answered without OPT")
	}
	if query("a.example.", false).IsEdns0() != nil {
		t.Error("query without EDNS answered with OPT (from the cached EDNS answer)")
	}
	// В кеше ответ без OPT — клиенту с EDNS OPT нужен.
	query("b.example.", false)
	if query("b.example.", true).IsEdns0() == nil {
		t.Error("EDNS query answered without OPT (from the cached plain answer)")
	}
}

// Расширенный RCODE (BADVERS, BADCOOKIE) живёт в OPT. Клиенту без EDNS такой
// ответ не собрать — он получает SERVFAIL, а не тишину.
func TestExtendedRcodeForNonEDNSClient(t *testing.T) {
	up := &fakeUp{rcode: dns.RcodeBadCookie}
	r := New(cache.New(16, time.Second, time.Hour), up)
	resp, err := ask(r, "example.com.", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resp.Pack(); err != nil {
		t.Fatalf("answer cannot be packed: %v", err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Errorf("rcode = %s, want SERVFAIL", dns.RcodeToString[resp.Rcode])
	}
}

// OPT собирается заново: COOKIE и padding апстрима (или другого клиента из
// кеша) не уходят клиенту; расширенная ошибка (EDE) — уходит.
func TestOPTIsRebuilt(t *testing.T) {
	up := &fakeUp{extraOpts: []dns.EDNS0{
		&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0102030405060708"},
		&dns.EDNS0_EDE{InfoCode: dns.ExtendedErrorCodeStaleAnswer},
	}}
	r := New(cache.New(16, time.Second, time.Hour), up)
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.SetEdns0(4096, false)
	resp, err := r.Resolve(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	opt := resp.IsEdns0()
	if opt == nil {
		t.Fatal("no OPT")
	}
	var cookie, ede bool
	for _, o := range opt.Option {
		switch o.(type) {
		case *dns.EDNS0_COOKIE:
			cookie = true
		case *dns.EDNS0_EDE:
			ede = true
		}
	}
	if cookie || !ede {
		t.Errorf("options: cookie=%v ede=%v, want only EDE", cookie, ede)
	}
}

// После недавнего общего сбоя апстримов устаревший ответ отдаётся сразу, а не
// через staleAnswerDelay на каждом запросе (RFC 8767, failure recheck).
func TestStaleServedAtOnceAfterRecentFailure(t *testing.T) {
	up := &fakeUp{}
	r := New(cache.New(16, time.Millisecond, time.Millisecond), up)
	ask(r, "a.example.", 1)
	ask(r, "b.example.", 2)
	time.Sleep(5 * time.Millisecond)
	up.err = errors.New("all upstreams failed")
	ask(r, "a.example.", 3) // сбой замечен
	up.err = nil
	up.delay = time.Second
	start := time.Now()
	if _, err := ask(r, "b.example.", 4); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("stale answer after %v, want at once during the failure window", d)
	}
}
