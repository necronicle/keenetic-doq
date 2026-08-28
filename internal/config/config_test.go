package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Listen != "127.0.0.1:5354" {
		t.Errorf("Listen = %q", c.Listen)
	}
	want := []string{"quic://dns.comss.one", "quic://dns.quad9.net"}
	if len(c.Upstreams) != 2 || c.Upstreams[0] != want[0] || c.Upstreams[1] != want[1] {
		t.Errorf("Upstreams = %v", c.Upstreams)
	}
	if c.CacheSize != 4096 || c.MinTTL != 60*time.Second || c.MaxTTL != 24*time.Hour || c.LogLevel != "info" {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestParseOverridesAndUpstreamReplacement(t *testing.T) {
	in := `
# comment
listen 0.0.0.0:5454
upstream quic://dns.example.com
upstream quic://dns2.example.com:8853
cache_size 100
min_ttl 10
max_ttl 3600
log debug
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:5454" || c.CacheSize != 100 || c.LogLevel != "debug" {
		t.Errorf("%+v", c)
	}
	if c.MinTTL != 10*time.Second || c.MaxTTL != 3600*time.Second {
		t.Errorf("ttl: %+v", c)
	}
	// первая же строка upstream ЗАМЕНЯЕТ дефолтные, а не дополняет
	if len(c.Upstreams) != 2 || c.Upstreams[0] != "quic://dns.example.com" {
		t.Errorf("Upstreams = %v", c.Upstreams)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"listen", "unknown_key 1", "cache_size abc", "min_ttl x"} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("Parse(%q): want error", bad)
		}
	}
}

func TestBootstrapDefaultsToBuiltInServers(t *testing.T) {
	cfg, err := Parse(strings.NewReader("listen 127.0.0.1:5354\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bootstrap) == 0 {
		t.Fatal("Bootstrap must have defaults, otherwise upstream names fall back to the system resolver")
	}
}

func TestBootstrapOverridesDefaultsAndGetsDefaultPort(t *testing.T) {
	cfg, err := Parse(strings.NewReader("bootstrap 9.9.9.9\nbootstrap 8.8.4.4:5300\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"9.9.9.9:53", "8.8.4.4:5300"}
	if len(cfg.Bootstrap) != len(want) {
		t.Fatalf("Bootstrap = %v, want %v", cfg.Bootstrap, want)
	}
	for i := range want {
		if cfg.Bootstrap[i] != want[i] {
			t.Errorf("Bootstrap[%d] = %q, want %q", i, cfg.Bootstrap[i], want[i])
		}
	}
}

// Имя в bootstrap само требует резолва — то есть системного резолвера, то есть
// той самой петли, ради которой bootstrap и появился.
func TestBootstrapRejectsHostname(t *testing.T) {
	if _, err := Parse(strings.NewReader("bootstrap dns.example.com\n")); err == nil {
		t.Error("hostname in bootstrap must be rejected")
	}
}

// Прямой автоголос: bootstrap, указывающий на сам doqd, замыкает петлю.
func TestBootstrapRejectsOwnListenAddress(t *testing.T) {
	in := "listen 192.168.1.1:5354\nbootstrap 192.168.1.1:5354\n"
	if _, err := Parse(strings.NewReader(in)); err == nil {
		t.Error("bootstrap pointing at our own listener must be rejected")
	}
}

// Любой DNS на самом роутере — это ndnproxy, а в его списке серверов стоит
// doqd. Резолвить через него имена апстримов значит спрашивать самого себя.
func TestBootstrapRejectsRouterLocalServers(t *testing.T) {
	for _, in := range []string{
		"listen 192.168.1.1:5354\nbootstrap 127.0.0.1\n",
		"listen 192.168.1.1:5354\nbootstrap 192.168.1.1\n",
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("must be rejected:\n%s", in)
		}
	}
}
