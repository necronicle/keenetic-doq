package upstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeUp struct {
	name  string
	fail  bool
	delay time.Duration
	calls int
}

func (f *fakeUp) Address() string { return f.name }
func (f *fakeUp) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.calls++
	if f.fail {
		return nil, errors.New(f.name + " down")
	}
	time.Sleep(f.delay)
	resp := new(dns.Msg)
	resp.SetReply(m)
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
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("calls a=%d b=%d, want 1/1", a.calls, b.calls)
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
	if a.calls != 1 {
		t.Errorf("a.calls = %d, want 1 (in cooldown)", a.calls)
	}
	fake = fake.Add(31 * time.Second) // cooldown истёк
	a.fail = false
	p.Exchange(context.Background(), query())
	if a.calls != 2 {
		t.Errorf("a.calls = %d, want 2 (retried after cooldown)", a.calls)
	}
}

func TestAllDownStillTries(t *testing.T) {
	a := &fakeUp{name: "a", fail: true}
	p := NewPicker([]Exchanger{a})
	p.Exchange(context.Background(), query()) // уходит в down
	if _, err := p.Exchange(context.Background(), query()); err == nil {
		t.Error("want error when all down")
	}
	if a.calls != 2 {
		t.Errorf("a.calls = %d, want 2 (down upstreams are last resort, not skipped)", a.calls)
	}
}

func TestFastestFirst(t *testing.T) {
	slow := &fakeUp{name: "slow", delay: 30 * time.Millisecond}
	fast := &fakeUp{name: "fast", delay: time.Millisecond}
	p := NewPicker([]Exchanger{slow, fast})
	// прогрев: обоим даём по замеру (порядок конфигурации: slow первый)
	p.Exchange(context.Background(), query())
	slowCalls := slow.calls
	// slow теперь имеет большой EWMA; следующие запросы должны идти в fast
	for i := 0; i < 3; i++ {
		p.Exchange(context.Background(), query())
	}
	if slow.calls != slowCalls {
		t.Errorf("slow.calls grew to %d — fastest must be tried first", slow.calls)
	}
	if fast.calls < 3 {
		t.Errorf("fast.calls = %d, want >=3", fast.calls)
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
