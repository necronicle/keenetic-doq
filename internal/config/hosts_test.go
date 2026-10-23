package config

import (
	"strings"
	"testing"
)

func TestHostsSource(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"", "builtin"}, {"hosts /opt/etc/custom.hosts", "/opt/etc/custom.hosts"}, {"hosts builtin", "builtin"}} {
		c, e := Parse(strings.NewReader(tc.in))
		if e != nil {
			t.Fatal(e)
		}
		if c.Hosts != tc.want {
			t.Errorf("got %q want %q", c.Hosts, tc.want)
		}
	}
}
func TestLegacyGeoRejectedWithMigrationAdvice(t *testing.T) {
	for _, s := range []string{"geo quic://g.example", "geo-domain example.com"} {
		_, e := Parse(strings.NewReader(s))
		if e == nil || !strings.Contains(e.Error(), "migrat") {
			t.Fatalf("%q: %v", s, e)
		}
	}
}
func TestHostsRejectsMissingAndAmbiguousValues(t *testing.T) {
	for _, s := range []string{"hosts", "hosts /some/file ignored", "hosts builtin\nhosts other"} {
		if _, e := Parse(strings.NewReader(s)); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
