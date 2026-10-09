package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/necronicle/keenetic-doq/internal/geo"
)

func sampleSnap() *geo.Snapshot {
	return &geo.Snapshot{
		Pinned: "quic://dns.dns-ai.ru",
		Since:  time.Date(2026, 10, 9, 14, 5, 0, 0, time.Local),
		Ranking: []geo.Result{
			{URL: "quic://dns.dns-ai.ru", Coverage: 3, Alive: 2, Total: 2, MedianTLSMs: 140},
			{URL: "quic://geohide.ru", Coverage: 3, Alive: 3, Total: 4, MedianTLSMs: 170},
		},
		Proxies: []geo.ProxyStatus{
			{Addr: "13.140.94.151", State: "healthy", MedianMs: 120},
			{Addr: "62.60.230.61", State: "dead", Fails: 2},
		},
		LearnedGeo: 7,
	}
}

func TestFormatListGeo(t *testing.T) {
	ups := []confServer{{URL: "quic://geohide.ru", Geo: true}, {URL: "quic://dns.dns-ai.ru", Geo: true},
		{URL: "quic://dns.quad9.net"}}
	res := []probeResult{{RTT: 25 * time.Millisecond}, {RTT: 30 * time.Millisecond}, {Err: errors.New("timeout")}}
	out := formatList("/opt/etc/doqd.conf", ups, res, sampleSnap())
	for _, want := range []string{
		"GEO",
		"   1. quic://geohide.ru",
		"proxies 3/4 alive, tls 170 ms",
		" * 2. quic://dns.dns-ai.ru",
		"proxies 1/2 ok, tls 120 ms",
		"FAST",
		"   3. quic://dns.quad9.net",
		"down   (timeout)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output misses %q:\n%s", want, out)
		}
	}
}

func TestFormatListWithoutGeoKeepsOldLayout(t *testing.T) {
	out := formatList("c", []confServer{{URL: "quic://u.example"}}, []probeResult{{RTT: time.Millisecond}}, nil)
	if !strings.HasPrefix(out, "UPSTREAMS (c):\n 1. quic://u.example") {
		t.Fatalf("old layout broken:\n%s", out)
	}
}

func TestFormatGeo(t *testing.T) {
	out := formatGeo(sampleSnap(), 3)
	for _, want := range []string{
		"pinned:     quic://dns.dns-ai.ru (since 2026-10-09 14:05)",
		" 1. quic://dns.dns-ai.ru",
		"coverage 3/3  proxies 2/2 alive  tls 140 ms",
		"13.140.94.151",
		"dead",
		"learned geo-blocked names: 7",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("geo output misses %q:\n%s", want, out)
		}
	}
}

func TestGeoStatusLine(t *testing.T) {
	if got := geoStatusLine(sampleSnap()); got != "geo:             pinned dns.dns-ai.ru since 2026-10-09 14:05, proxies 1/2 healthy" {
		t.Fatalf("got %q", got)
	}
	if got := geoStatusLine(nil); !strings.Contains(got, "no state yet") {
		t.Fatalf("got %q", got)
	}
}

func TestFormatGeoInconclusive(t *testing.T) {
	s := sampleSnap()
	s.EvaluatedAt = time.Date(2026, 10, 9, 13, 0, 0, 0, time.Local)
	s.AttemptedAt = time.Date(2026, 10, 9, 14, 30, 0, 0, time.Local)
	s.Inconclusive = true
	s.RetryAt = time.Date(2026, 10, 9, 14, 31, 0, 0, time.Local)
	out := formatGeo(s, 3)
	for _, want := range []string{
		"evaluation: 2026-10-09 13:00",
		"last attempt 2026-10-09 14:30 was inconclusive (network down?), kept the previous choice; retry at 14:31",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("geo output misses %q:\n%s", want, out)
		}
	}
}

func TestFormatGeoTemporaryPin(t *testing.T) {
	s := &geo.Snapshot{Pinned: "quic://geohide.ru"}
	out := formatGeo(s, 3)
	if !strings.Contains(out, "pinned:     quic://geohide.ru (temporary, until the first evaluation)") {
		t.Fatalf("temporary pin must not show a zero date:\n%s", out)
	}
	if got := geoStatusLine(s); strings.Contains(got, "0001") {
		t.Fatalf("status must not show a zero date: %q", got)
	}
}
