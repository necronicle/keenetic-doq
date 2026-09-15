package cli

import (
	"errors"
	"testing"
)

var errExit = errors.New("exit status 1")

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

func TestNdmSessionFailed(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"ok", "           server:\n              address: 192.168.1.1\n                 port: 5354\n", nil, false},
		{"empty list", "\n", nil, false},
		// Шелл из `exec sh` внутри CLI роутера: ndmc не может открыть вложенную сессию.
		{"exec sh, rc=1", "[C] ndm: ndmc: system failed [0xcffd0062].\n[C] ndm: Cli::Main: failed to initialize.\n", errExit, true},
		{"exec sh, rc=0", "[C] ndm: Cli::Main: failed to initialize.\n", nil, true},
		{"system failed only", "ndmc: system failed [0xcffd0062].\n", nil, true},
	}
	for _, c := range cases {
		if got := ndmSessionFailed(c.out, c.err); got != c.want {
			t.Errorf("%s: ndmSessionFailed = %v, want %v", c.name, got, c.want)
		}
	}
}
