package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/necronicle/keenetic-doq/internal/config"
)

var sample = []string{
	"# doqd config",
	"listen 192.168.1.1:5354",
	"",
	"upstream quic://dns.comss.one",
	"upstream quic://unfiltered.adguard-dns.com",
	"cache_size 4096",
	"log info",
}

func TestConfUpstreamsAndListen(t *testing.T) {
	got := confUpstreams(sample)
	want := []string{"quic://dns.comss.one", "quic://unfiltered.adguard-dns.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("confUpstreams = %v, want %v", got, want)
	}
	if l := confListen(sample); l != "192.168.1.1:5354" {
		t.Fatalf("confListen = %q", l)
	}
	if l := confListen([]string{"# empty"}); l != config.Default().Listen {
		t.Fatalf("confListen fallback = %q", l)
	}
}

func TestAddUpstream(t *testing.T) {
	out, err := addUpstream(sample, "quic://dns.quad9.net")
	if err != nil {
		t.Fatal(err)
	}
	// вставка сразу после последней upstream-строки, остальное нетронуто
	if out[5] != "upstream quic://dns.quad9.net" || out[6] != "cache_size 4096" {
		t.Fatalf("wrong insertion: %v", out)
	}
	if out[0] != "# doqd config" || len(out) != len(sample)+1 {
		t.Fatalf("other lines must be preserved: %v", out)
	}
	if _, err := addUpstream(sample, "quic://dns.comss.one"); err == nil {
		t.Fatal("duplicate must be rejected")
	}
}

func TestAddUpstreamNoExisting(t *testing.T) {
	out, err := addUpstream([]string{"listen 1.2.3.4:5354"}, "quic://a.example")
	if err != nil || out[len(out)-1] != "upstream quic://a.example" {
		t.Fatalf("append to end: %v, %v", out, err)
	}
}

func TestRemoveUpstream(t *testing.T) {
	out, removed, err := removeUpstream(sample, "2")
	if err != nil || removed != "quic://unfiltered.adguard-dns.com" {
		t.Fatalf("remove by number: %q, %v", removed, err)
	}
	if len(confUpstreams(out)) != 1 {
		t.Fatalf("one upstream must remain: %v", out)
	}
	if _, _, err := removeUpstream(sample, "quic://dns.comss.one"); err != nil {
		t.Fatalf("remove by url: %v", err)
	}
	if _, _, err := removeUpstream(sample, "9"); err == nil {
		t.Fatal("out-of-range number must fail")
	}
	if _, _, err := removeUpstream(sample, "quic://nope.example"); err == nil {
		t.Fatal("unknown url must fail")
	}
	one := []string{"upstream quic://dns.comss.one"}
	if _, _, err := removeUpstream(one, "1"); err == nil {
		t.Fatal("last upstream must be protected")
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doqd.conf")
	if _, exists, err := readConfLines(path); err != nil || exists {
		t.Fatalf("missing file: exists=%v err=%v", exists, err)
	}
	if err := writeConfLines(path, sample); err != nil {
		t.Fatal(err)
	}
	lines, exists, err := readConfLines(path)
	if err != nil || !exists || !reflect.DeepEqual(lines, sample) {
		t.Fatalf("round trip: %v %v %v", lines, exists, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("file must end with newline")
	}
}

func TestDefaultConfLinesParse(t *testing.T) {
	cfg, err := config.Parse(strings.NewReader(strings.Join(defaultConfLines(), "\n")))
	if err != nil {
		t.Fatalf("defaults must parse: %v", err)
	}
	if !reflect.DeepEqual(cfg, config.Default()) {
		t.Fatalf("defaults mismatch: %+v vs %+v", cfg, config.Default())
	}
}
