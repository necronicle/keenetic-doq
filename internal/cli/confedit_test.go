package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/necronicle/keenetic-doq/internal/config"
)

var withFallback = []string{
	"# doqd config",
	"listen 192.168.1.1:5354",
	"",
	"upstream quic://dns.comss.one",
	"upstream quic://unfiltered.adguard-dns.com",
	"fallback quic://dns.quad9.net",
	"cache_size 4096",
	"log info",
}

var sample = []string{
	"# doqd config",
	"listen 192.168.1.1:5354",
	"",
	"upstream quic://dns.comss.one",
	"upstream quic://unfiltered.adguard-dns.com",
	"cache_size 4096",
	"log info",
}

func TestConfServersAndListen(t *testing.T) {
	got := confServers(withFallback)
	want := []confServer{{URL: "quic://dns.comss.one"}, {URL: "quic://unfiltered.adguard-dns.com"},
		{URL: "quic://dns.quad9.net", Fallback: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("confServers = %v, want %v", got, want)
	}
	if l := confListen(sample); l != "192.168.1.1:5354" {
		t.Fatalf("confListen = %q", l)
	}
	if l := confListen([]string{"# empty"}); l != config.Default().Listen {
		t.Fatalf("confListen fallback = %q", l)
	}
}

func TestAddUpstream(t *testing.T) {
	out, err := addServer(sample, confServer{URL: "quic://dns.quad9.net"})
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
	if _, err := addServer(sample, confServer{URL: "quic://dns.comss.one"}); err == nil {
		t.Fatal("duplicate must be rejected")
	}
	if _, err := addServer(withFallback, confServer{URL: "quic://dns.quad9.net"}); err == nil {
		t.Fatal("a fallback added again as an upstream must be rejected")
	}
}

// Основной встаёт к основным, перед резервными; первый резервный — после
// основных.
func TestAddServerKeepsKindsTogether(t *testing.T) {
	out, err := addServer(withFallback, confServer{URL: "quic://geohide.ru"})
	if err != nil {
		t.Fatal(err)
	}
	if out[5] != "upstream quic://geohide.ru" || out[6] != "fallback quic://dns.quad9.net" {
		t.Fatalf("upstream not placed with upstreams: %v", out)
	}
	out, err = addServer(withFallback, confServer{URL: "quic://dns10.quad9.net", Fallback: true})
	if err != nil {
		t.Fatal(err)
	}
	if out[6] != "fallback quic://dns10.quad9.net" || out[7] != "cache_size 4096" {
		t.Fatalf("fallback not placed after fallbacks: %v", out)
	}
	out, err = addServer(sample, confServer{URL: "quic://dns.quad9.net", Fallback: true})
	if err != nil {
		t.Fatal(err)
	}
	if out[5] != "fallback quic://dns.quad9.net" {
		t.Fatalf("first fallback not placed after the upstreams: %v", out)
	}
	out, err = addServer([]string{"fallback quic://f.example", "log info"}, confServer{URL: "quic://u.example"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != "upstream quic://u.example" {
		t.Fatalf("first upstream not placed before the fallbacks: %v", out)
	}
}

func TestAddUpstreamNoExisting(t *testing.T) {
	out, err := addServer([]string{"listen 1.2.3.4:5354"}, confServer{URL: "quic://a.example"})
	if err != nil || out[len(out)-1] != "upstream quic://a.example" {
		t.Fatalf("append to end: %v, %v", out, err)
	}
}

func TestRemoveUpstream(t *testing.T) {
	out, removed, err := removeServer(sample, "2")
	if err != nil || removed.URL != "quic://unfiltered.adguard-dns.com" {
		t.Fatalf("remove by number: %v, %v", removed, err)
	}
	if len(confServers(out)) != 1 {
		t.Fatalf("one upstream must remain: %v", out)
	}
	if _, _, err := removeServer(sample, "quic://dns.comss.one"); err != nil {
		t.Fatalf("remove by url: %v", err)
	}
	if _, _, err := removeServer(sample, "9"); err == nil {
		t.Fatal("out-of-range number must fail")
	}
	if _, _, err := removeServer(sample, "quic://nope.example"); err == nil {
		t.Fatal("unknown url must fail")
	}
	one := []string{"upstream quic://dns.comss.one"}
	if _, _, err := removeServer(one, "1"); err == nil {
		t.Fatal("last upstream must be protected")
	}
	// Резервный без основных конфиг не примет: последний основной защищён и
	// при живом резерве, а сам резерв удаляется спокойно.
	oneAndFallback := []string{"upstream quic://dns.comss.one", "fallback quic://dns.quad9.net"}
	if _, _, err := removeServer(oneAndFallback, "1"); err == nil {
		t.Fatal("last upstream must be protected even with a fallback left")
	}
	out, removed, err = removeServer(oneAndFallback, "2")
	if err != nil || !removed.Fallback || len(confServers(out)) != 1 {
		t.Fatalf("remove fallback: %v, %v, %v", out, removed, err)
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

func TestConfBootstrap(t *testing.T) {
	lines := []string{"listen 192.168.1.1:5354", "bootstrap 9.9.9.9", "bootstrap 8.8.4.4:5300"}
	got := confBootstrap(lines)
	want := []string{"9.9.9.9:53", "8.8.4.4:5300"}
	if len(got) != len(want) {
		t.Fatalf("confBootstrap = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfBootstrapFallsBackToDefaults(t *testing.T) {
	got := confBootstrap([]string{"listen 192.168.1.1:5354"})
	if len(got) == 0 {
		t.Fatal("want built-in bootstrap servers when the config names none")
	}
}
