package cli

import "testing"

func TestIsDaemonArgs(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"/opt/sbin/doqd"}, true},
		{[]string{"/opt/sbin/doqd", "-c", "/opt/etc/doqd.conf"}, true},
		{[]string{"/opt/sbin/doqd", "add", "quic://dns.quad9.net"}, false},
		{[]string{"/opt/sbin/doqd", "list"}, false},
		{[]string{"/opt/sbin/doqd", "status", "-c", "/opt/etc/doqd.conf"}, false},
	}
	for _, c := range cases {
		if got := IsDaemonArgs(c.args); got != c.want {
			t.Errorf("IsDaemonArgs(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
