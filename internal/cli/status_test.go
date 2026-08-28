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
