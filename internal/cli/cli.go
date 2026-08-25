// Package cli — режим утилиты управления doqd: подкоманды
// list/test/add/remove/status поверх того же бинарника, что и демон.
package cli

import (
	"fmt"
	"os"
)

const defaultConf = "/opt/etc/doqd.conf"

var subcommands = map[string]func(args []string) int{
	"help": func([]string) int { usage(os.Stdout); return 0 },
}

func IsSubcommand(s string) bool {
	_, ok := subcommands[s]
	return ok
}

func Run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	cmd, ok := subcommands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
	return cmd(args[1:])
}

func usage(w *os.File) {
	fmt.Fprint(w, `doqd — DNS-over-QUIC forwarder for Keenetic routers

Daemon mode (used by the init script):
  doqd [-c /opt/etc/doqd.conf]

Management commands:
  doqd list                       upstreams from the config with live probes
  doqd test quic://host[:port]    probe any DoQ server, config untouched
  doqd add [--force] quic://...   probe, add to config, restart the daemon
  doqd remove <number|url>        remove an upstream, restart the daemon
  doqd status                     daemon, registration and resolve check

Flags accepted by management commands:
  -c path    config file (default /opt/etc/doqd.conf)
`)
}
