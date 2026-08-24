package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
)

type fakeUp struct{ calls int }

func (f *fakeUp) Address() string { return "fake" }
func (f *fakeUp) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	f.calls++
	resp := new(dns.Msg)
	resp.SetReply(m)
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
	if up.calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (second must be cache hit)", up.calls)
	}
	if resp2.Id != 2 {
		t.Errorf("resp2.Id = %d, want 2 (request ID)", resp2.Id)
	}
}
