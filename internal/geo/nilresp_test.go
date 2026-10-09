package geo

import (
	"context"
	"reflect"
	"testing"

	"github.com/miekg/dns"
)

// nilEx — апстрим, нарушающий контракт: ни ответа, ни ошибки.
type nilEx struct{ addr string }

func (n nilEx) Address() string { return n.addr }

func (n nilEx) Exchange(context.Context, *dns.Msg) (*dns.Msg, error) { return nil, nil }

func TestNilResponseFromPinnedIsAFailure(t *testing.T) {
	g2 := newFake("g2", map[string][]string{"chatgpt.com.": {"2.2.2.2"}})
	fast := newFake("fast", nil)
	l := New(Config{Fast: fast, Geo: []Server{{URL: "g1", Ex: nilEx{"g1"}}, {URL: "g2", Ex: g2}}, Prober: &fakeProber{}})
	if got := ipsOf(ask(t, l, "chatgpt.com", dns.TypeA)); !reflect.DeepEqual(got, []string{"2.2.2.2"}) {
		t.Fatalf("got %v", got)
	}
	if l.Snapshot().Fails != 1 {
		t.Fatal("(nil, nil) from the pinned server is a failure")
	}
}

func TestNilResponseEverywhereIsAnError(t *testing.T) {
	l := New(Config{Fast: nilEx{"fast"}, Geo: []Server{{URL: "g1", Ex: nilEx{"g1"}}}, Prober: &fakeProber{}})
	for _, name := range []string{"chatgpt.com.", "unknown.example."} {
		m := new(dns.Msg)
		m.SetQuestion(name, dns.TypeA)
		resp, err := l.Exchange(context.Background(), m)
		if err == nil || resp != nil {
			t.Fatalf("%s: want an error, got %v, %v", name, resp, err)
		}
	}
}

func TestNilResponseFromFastOnUnknownName(t *testing.T) {
	g1 := newFake("g1", map[string][]string{"new.example.": {"7.7.7.7"}})
	l := New(Config{Fast: nilEx{"fast"}, Geo: []Server{{URL: "g1", Ex: g1}}, Prober: &fakeProber{}})
	if got := ipsOf(ask(t, l, "new.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"7.7.7.7"}) {
		t.Fatalf("an empty fast lane must not win: got %v", got)
	}
}

func TestEvaluateNilResponse(t *testing.T) {
	ev := &Evaluator{Reference: nilEx{"ref"}, Prober: &fakeProber{}, Probes: ProbeDomains, Attempts: 1, Timeout: evaluationTimeout}
	rs := ev.Run(context.Background(), []Server{{URL: "g1", Ex: nilEx{"g1"}}})
	if len(rs) != 1 || rs[0].Coverage != 0 || rs[0].Err == "" {
		t.Fatalf("(nil, nil) must be an evaluation error, got %+v", rs)
	}
}
