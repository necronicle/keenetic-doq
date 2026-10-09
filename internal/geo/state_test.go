package geo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "geo.state") // каталог создаётся сам
	since := time.Date(2026, 10, 9, 14, 5, 0, 0, time.UTC)
	in := &State{Pinned: "quic://dns.dns-ai.ru", Since: since, Ranking: []Result{{URL: "quic://dns.dns-ai.ru",
		Coverage: 3, PoolIPs: []string{"13.140.94.151"}, SNI: map[string]string{"13.140.94.151": "chatgpt.com"}}}}
	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatal("temporary file must be renamed away")
	}
	out, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Pinned != in.Pinned || !out.Since.Equal(since) || out.Ranking[0].SNI["13.140.94.151"] != "chatgpt.com" {
		t.Fatalf("state = %+v", out)
	}
	if _, err := LoadState(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing file must give IsNotExist, got %v", err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doqd.state.json")
	if err := WriteSnapshot(path, Snapshot{Pinned: "g1", LearnedGeo: 4,
		Proxies: []ProxyStatus{{Addr: "1.1.1.1", State: "healthy", MedianMs: 120}}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSnapshot(path)
	if err != nil || s.Pinned != "g1" || s.LearnedGeo != 4 || s.Proxies[0].MedianMs != 120 {
		t.Fatalf("snapshot = %+v, %v", s, err)
	}
}
