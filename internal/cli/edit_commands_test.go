package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doqd.conf")
	if err := writeConfLines(path, sample); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunAddRejectsBadURL(t *testing.T) {
	path := writeSample(t)
	if got := Run([]string{"add", "-c", path, "https://wrong.example"}); got != 1 {
		t.Fatalf("add bad url = %d, want 1", got)
	}
	lines, _, _ := readConfLines(path)
	if len(confUpstreams(lines)) != 2 {
		t.Fatal("config must be untouched")
	}
}

func TestRunRemoveByNumber(t *testing.T) {
	path := writeSample(t)
	if got := Run([]string{"remove", "-c", path, "2"}); got != 0 {
		t.Fatalf("remove = %d, want 0", got)
	}
	lines, _, _ := readConfLines(path)
	ups := confUpstreams(lines)
	if len(ups) != 1 || ups[0] != "quic://dns.comss.one" {
		t.Fatalf("wrong config after remove: %v", ups)
	}
}

func TestRunRemoveLastRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doqd.conf")
	if err := writeConfLines(path, []string{"upstream quic://dns.comss.one"}); err != nil {
		t.Fatal(err)
	}
	if got := Run([]string{"remove", "-c", path, "1"}); got != 1 {
		t.Fatalf("remove last = %d, want 1", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "quic://dns.comss.one") {
		t.Fatal("config must be untouched")
	}
}
