package cli

import (
	"reflect"
	"strings"
	"testing"
)

// Вывод `uci show dhcp.@dnsmasq[0]` после инсталлера: doqd, NTP-имена через
// bootstrap и noresolv.
const uciShow = `dhcp.cfg01411c=dnsmasq
dhcp.cfg01411c.domainneeded='1'
dhcp.cfg01411c.local='/lan/'
dhcp.cfg01411c.server='127.0.0.1#5354' '/0.openwrt.pool.ntp.org/1.openwrt.pool.ntp.org/77.88.8.8'
dhcp.cfg01411c.noresolv='1'
`

func TestParseUCIDnsmasq(t *testing.T) {
	servers, noResolv := parseUCIDnsmasq(uciShow)
	want := []string{"127.0.0.1#5354", "/0.openwrt.pool.ntp.org/1.openwrt.pool.ntp.org/77.88.8.8"}
	if !reflect.DeepEqual(servers, want) || !noResolv {
		t.Fatalf("got %q noresolv=%v, want %q noresolv=true", servers, noResolv, want)
	}
	if _, on := parseUCIDnsmasq("dhcp.x.noresolv='yes'\n"); !on {
		t.Fatal("noresolv='yes' must count as on")
	}
	servers, noResolv = parseUCIDnsmasq("dhcp.cfg01411c=dnsmasq\ndhcp.cfg01411c.noresolv='0'\n")
	if len(servers) != 0 || noResolv {
		t.Fatalf("stock config: got %q noresolv=%v", servers, noResolv)
	}
}

func TestUCIValues(t *testing.T) {
	cases := map[string][]string{
		`'1'`:                  {"1"},
		`'a' 'b#53'`:           {"a", "b#53"},
		`'it'\''s'`:            {"it's"},
		`1`:                    {"1"},
		`'/x.org/1.2.3.4' 'y'`: {"/x.org/1.2.3.4", "y"},
	}
	for in, want := range cases {
		if got := uciValues(in); !reflect.DeepEqual(got, want) {
			t.Errorf("uciValues(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestDnsmasqTarget(t *testing.T) {
	cases := map[[2]string]string{
		{"127.0.0.1", "5354"}: "127.0.0.1#5354",
		{"0.0.0.0", "5354"}:   "127.0.0.1#5354",
		{"::1", "5354"}:       "::1#5354",
		{"192.168.1.1", "53"}: "192.168.1.1#53",
	}
	for in, want := range cases {
		if got := dnsmasqTarget(in[0], in[1]); got != want {
			t.Errorf("dnsmasqTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDnsmasqReport(t *testing.T) {
	const target = "127.0.0.1#5354"
	ntp := "/0.openwrt.pool.ntp.org/77.88.8.8"
	cases := []struct {
		name      string
		st        dnsmasqState
		want, not []string
	}{
		{"installed", dnsmasqState{UCI: true, Servers: []string{target, ntp}, NoResolv: true,
			Running: []string{target, ntp}, RunningKnown: true},
			[]string{"forwards to doqd"}, []string{"other DNS", "NOT"}},
		{"not applied", dnsmasqState{UCI: true, Servers: []string{target}, NoResolv: true,
			Running: []string{}, RunningKnown: true},
			[]string{"does not use it yet", "/etc/init.d/dnsmasq restart"}, []string{"forwards to doqd"}},
		{"missing", dnsmasqState{UCI: true, NoResolv: false},
			[]string{"NOT forwarding", "uci add_list dhcp.@dnsmasq[0].server='127.0.0.1#5354'"}, nil},
		{"wan dns on", dnsmasqState{UCI: true, Servers: []string{target}},
			[]string{"forwards to doqd", "other DNS:       the WAN DNS servers (noresolv is off)"}, nil},
		{"another proxy", dnsmasqState{UCI: true, Servers: []string{"127.0.0.1#5053", target}, NoResolv: true},
			[]string{"other DNS:       127.0.0.1#5053"}, []string{"WAN DNS"}},
		{"no uci", dnsmasqState{}, []string{"unknown"}, []string{"forwards"}},
	}
	for _, c := range cases {
		out := strings.Join(dnsmasqReport(c.st, target), "\n")
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q in:\n%s", c.name, w, out)
			}
		}
		for _, n := range c.not {
			if strings.Contains(out, n) {
				t.Errorf("%s: unexpected %q in:\n%s", c.name, n, out)
			}
		}
	}
}
