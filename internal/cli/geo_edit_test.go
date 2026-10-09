package cli

import (
	"path/filepath"
	"reflect"
	"testing"
)

var withGeo = []string{
	"listen 192.168.1.1:5354",
	"geo quic://geohide.ru",
	"geo quic://dns.dns-ai.ru",
	"upstream quic://dns.quad9.net",
	"fallback quic://f.example",
	"log info",
}

func TestConfServersGeo(t *testing.T) {
	got := confServers(withGeo)
	want := []confServer{{URL: "quic://geohide.ru", Geo: true}, {URL: "quic://dns.dns-ai.ru", Geo: true},
		{URL: "quic://dns.quad9.net"}, {URL: "quic://f.example", Fallback: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("confServers = %v", got)
	}
}

func TestAddServerGeoPlacement(t *testing.T) {
	out, err := addServer(withGeo, confServer{URL: "quic://new.geo", Geo: true})
	if err != nil || out[3] != "geo quic://new.geo" {
		t.Fatalf("geo goes after the last geo: %v %v", out, err)
	}
	noGeo := []string{"listen x", "upstream quic://u.example", "fallback quic://f.example"}
	out, err = addServer(noGeo, confServer{URL: "quic://new.geo", Geo: true})
	if err != nil || out[1] != "geo quic://new.geo" {
		t.Fatalf("first geo goes before the first upstream: %v %v", out, err)
	}
}

func TestRemoveServerKeepsOnePrimary(t *testing.T) {
	lines := []string{"geo quic://g.example", "fallback quic://f.example"}
	if _, _, err := removeServer(lines, "1"); err == nil {
		t.Fatal("the last geo/upstream server must not be removable")
	}
	lines = []string{"geo quic://g.example", "upstream quic://u.example"}
	if _, _, err := removeServer(lines, "1"); err != nil {
		t.Fatalf("geo with an upstream left is removable: %v", err)
	}
}

func TestAddRemoveDomain(t *testing.T) {
	out, err := addDomain(withGeo, "Example.AI.")
	if err != nil || out[5] != "geo-domain example.ai" {
		t.Fatalf("after the last server line: %v %v", out, err)
	}
	out, err = addDomain(out, "x.example.org")
	if err != nil || out[6] != "geo-domain x.example.org" {
		t.Fatalf("after the last geo-domain: %v %v", out, err)
	}
	if _, err := addDomain(out, "example.ai"); err == nil {
		t.Fatal("duplicate must be refused")
	}
	if _, err := addDomain(out, "localhost"); err == nil {
		t.Fatal("single-label name must be refused")
	}
	out, err = removeDomain(out, "EXAMPLE.ai")
	if err != nil || len(out) != len(withGeo)+1 {
		t.Fatalf("remove: %v %v", out, err)
	}
	if _, err := removeDomain(out, "chatgpt.com"); err == nil {
		t.Fatal("built-in domains are not in the config and cannot be removed")
	}
}

func TestRunAddDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doqd.conf")
	if err := writeConfLines(path, withGeo); err != nil {
		t.Fatal(err)
	}
	if got := Run([]string{"add-domain", "-c", path, "example.ai"}); got != 0 {
		t.Fatalf("add-domain = %d", got)
	}
	lines, _, _ := readConfLines(path)
	if lines[5] != "geo-domain example.ai" {
		t.Fatalf("config = %v", lines)
	}
	if got := Run([]string{"remove-domain", "-c", path, "example.ai"}); got != 0 {
		t.Fatalf("remove-domain = %d", got)
	}
}

func TestRunAddGeoAndFallbackConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doqd.conf")
	writeConfLines(path, withGeo)
	if got := Run([]string{"add", "-c", path, "--geo", "--fallback", "quic://x.example"}); got != 2 {
		t.Fatalf("--geo with --fallback = %d, want 2", got)
	}
}
