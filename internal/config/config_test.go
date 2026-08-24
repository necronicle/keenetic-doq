package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Listen != "127.0.0.1:5353" {
		t.Errorf("Listen = %q", c.Listen)
	}
	want := []string{"quic://dns.comss.one", "quic://unfiltered.adguard-dns.com"}
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
