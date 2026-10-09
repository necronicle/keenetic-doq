package geo

import (
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Закреплённый завис: первое неизвестное имя ждёт его не дольше предела, а
// таймаут засчитывается; пока счёт неудач > 0, следующие его не ждут вовсе.
func TestClassifyHangingPinnedIsCountedAndNotAwaited(t *testing.T) {
	fast := newFake("fast", map[string][]string{"a.example.": {"7.7.7.1"}, "b.example.": {"7.7.7.2"}})
	g1 := newFake("g1", nil)
	g1.setDelay(10 * time.Second)
	l, _ := newTestLanes(t, fast, nil, g1)
	l.attempt = 500 * time.Millisecond // дольше ожидания, как 3 с против 1 с
	l.rtt = 100 * time.Millisecond     // ожидание = 300 мс
	if got := ipsOf(ask(t, l, "a.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"7.7.7.1"}) {
		t.Fatalf("got %v", got)
	}
	waitFor(t, func() bool { return l.Snapshot().Fails == 1 }, 2*time.Second)
	start := time.Now()
	if got := ipsOf(ask(t, l, "b.example", dns.TypeA)); !reflect.DeepEqual(got, []string{"7.7.7.2"}) {
		t.Fatalf("got %v", got)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("pinned server is failing: the fast answer must come at once, took %v", d)
	}
	if l.class.Lookup("b.example.") != Unknown {
		t.Fatal("a name answered without the pinned server must not be learned")
	}
}
