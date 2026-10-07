package cli

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

func TestProbeBadURL(t *testing.T) {
	if r := probe("https://not-quic.example", nil, time.Second); r.Err == nil {
		t.Fatal("non-quic scheme must fail")
	}
}

func TestRunTestUsage(t *testing.T) {
	if got := Run([]string{"test"}); got != 2 {
		t.Fatalf("test without args = %d, want 2", got)
	}
}

func TestBootstrapHint(t *testing.T) {
	silent := fmt.Errorf("x: %w", upstream.ErrBootstrapNoAnswer)
	if h := bootstrapHint([]probeResult{{Err: errors.New("dial: handshake failed")}, {}}); h != "" {
		t.Errorf("hint for a non-bootstrap failure: %q", h)
	}
	h := bootstrapHint([]probeResult{{}, {Err: silent}})
	if !strings.Contains(h, "77.88.8.8:1253") {
		t.Errorf("hint %q does not suggest the alternative port", h)
	}
}

func TestStagesShowWhereItStopped(t *testing.T) {
	ok := probeResult{
		Boot: upstream.Resolution{
			IPs:    []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")},
			Server: "77.88.8.8:1253", Proto: "tcp", Took: 85 * time.Millisecond,
		},
		Dials: []dialAttempt{
			{Addr: "192.0.2.2:853", Took: 210 * time.Millisecond},
			{Addr: "192.0.2.1:853", Took: 1210 * time.Millisecond, Err: upstream.ErrOvertaken},
		},
		Query: 60 * time.Millisecond,
		RTT:   355 * time.Millisecond,
	}
	out := formatStages(ok)
	for _, want := range []string{"77.88.8.8:1253 over tcp", "192.0.2.1:853  no answer within 1210 ms, another address was faster", "192.0.2.2:853  OK in 210 ms", "60 ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("stages do not mention %q:\n%s", want, out)
		}
	}

	dead := probeResult{
		Boot:  ok.Boot,
		Dials: []dialAttempt{{Addr: "192.0.2.1:853", Took: 5 * time.Second, Err: errors.New("timeout")}},
		Err:   errors.New("dial dns.example:853: no address answered"),
	}
	if h := dialHint(dead); !strings.Contains(h, "udp/853") {
		t.Errorf("no QUIC-blocked hint for a dead server: %q", h)
	}
	if h := dialHint(ok); h != "" {
		t.Errorf("hint for a healthy server: %q", h)
	}
}
