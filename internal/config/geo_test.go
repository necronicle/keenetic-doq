package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseGeoKeys(t *testing.T) {
	c, err := Parse(strings.NewReader("geo quic://g.example\nupstream quic://u.example\n" +
		"geo-domain Example.AI.\ngeo-domain x.example.org\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Geo, []string{"quic://g.example"}) ||
		!reflect.DeepEqual(c.Upstreams, []string{"quic://u.example"}) || len(c.Fallbacks) != 0 {
		t.Fatalf("servers = %v / %v / %v", c.Geo, c.Upstreams, c.Fallbacks)
	}
	if !reflect.DeepEqual(c.GeoDomains, []string{"example.ai", "x.example.org"}) {
		t.Fatalf("GeoDomains = %v", c.GeoDomains)
	}
}

func TestParseAnyServerLineDropsAllServerDefaults(t *testing.T) {
	c, err := Parse(strings.NewReader("upstream quic://u.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Geo) != 0 || len(c.Fallbacks) != 0 {
		t.Fatalf("defaults leaked: geo %v fallback %v", c.Geo, c.Fallbacks)
	}
}

func TestParseGeoOnlyIsValid(t *testing.T) {
	c, err := Parse(strings.NewReader("geo quic://g.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Upstreams) != 0 || len(c.Geo) != 1 {
		t.Fatalf("Geo = %v, Upstreams = %v", c.Geo, c.Upstreams)
	}
}

func TestParseFallbackOnlyRejected(t *testing.T) {
	if _, err := Parse(strings.NewReader("fallback quic://f.example\n")); err == nil {
		t.Fatal("fallback without upstream or geo must be rejected")
	}
}

func TestParseBadGeoDomain(t *testing.T) {
	for _, d := range []string{"localhost", "bad..name", "-"} {
		if _, err := Parse(strings.NewReader("geo-domain " + d + "\n")); err == nil {
			t.Errorf("geo-domain %q accepted", d)
		}
	}
}

func TestGeoDomainNormalizes(t *testing.T) {
	got, err := GeoDomain("Sub.Example.COM.")
	if err != nil || got != "sub.example.com" {
		t.Fatalf("GeoDomain = %q, %v", got, err)
	}
}
