package cli

import (
	"net"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

// Если LAN-адрес роутера сменился (его выдаёт вышестоящий DHCP), в конфиге
// остаётся прежний listen — bind по нему уже не пройдёт, и это надо назвать
// вслух, а не показывать «daemon: not running» без объяснений.
func TestHostOnInterfaces(t *testing.T) {
	addrs := []net.Addr{mustCIDR(t, "127.0.0.1/8"), mustCIDR(t, "192.168.1.1/24")}
	cases := []struct {
		host string
		want bool
	}{
		{"192.168.1.1", true},
		{"127.0.0.1", true},
		{"192.168.1.200", false},
		{"", true},          // не адрес — проверять нечего
		{"0.0.0.0", true},   // джокер занимает любой адрес
		{"localhost", true}, // не IP — не наше дело
	}
	for _, c := range cases {
		if got := hostOnInterfaces(c.host, addrs); got != c.want {
			t.Errorf("hostOnInterfaces(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// Вывод `show ip name-server` с тест-роутера: doqd плюс DNS провайдера,
// пришедшие по PPPoE. Их ndnproxy спрашивает наравне с doqd.
const showNameServers = `
           server: 
              address: 192.168.1.1
                 port: 5354
               domain: 
               global: 0
              service: Dns::Manager
            interface: 

           server: 
              address: 88.87.64.6
               domain: 
               global: 32767
              service: Network::Interface::Ppp-PPPoE0
            interface: PPPoE0

           server: 
              address: 10.0.0.53
               domain: corp.example
               global: 0
            interface: 

           server: 
              address: 5.3.3.3
               domain: 
               global: 32767
              service: Network::Interface::Ppp-PPPoE0
            interface: PPPoE0
`

func TestOtherNameServers(t *testing.T) {
	got := otherNameServers(showNameServers, "192.168.1.1", "5354")
	want := []string{"88.87.64.6 (PPPoE0)", "5.3.3.3 (PPPoE0)"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("otherNameServers = %q, want %q", got, want)
	}
	only := "server:\n address: 192.168.1.1\n port: 5354\n domain: \n"
	if got := otherNameServers(only, "192.168.1.1", "5354"); len(got) != 0 {
		t.Fatalf("doqd alone: got %q", got)
	}
}
