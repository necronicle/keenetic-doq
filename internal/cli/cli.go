// Package cli — режим утилиты управления doqd: подкоманды
// list/test/add/remove/add-domain/remove-domain/status поверх того же
// бинарника, что и демон.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/necronicle/keenetic-doq/internal/platform"
)

// plat — прошивка, на которой запущена утилита; defaultConf — её конфиг.
var (
	plat        = platform.Detect()
	defaultConf = plat.Conf
)

var subcommands = map[string]func(args []string) int{
	"help":          func([]string) int { usage(os.Stdout); return 0 },
	"test":          runTest,
	"list":          runList,
	"add":           runAdd,
	"remove":        runRemove,
	"add-domain":    runAddDomain,
	"remove-domain": runRemoveDomain,
	"status":        runStatus,
	"geo":           runGeo,
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

const usageText = `doqd — DNS-over-QUIC forwarder for Keenetic and OpenWRT routers

Daemon mode (used by the init script):
  doqd [-c CONF]

Management commands:
  doqd list                       upstreams from the config with live probes
  doqd test quic://host[:port]    probe any DoQ server, config untouched
  doqd add [--force] quic://...   probe, add to config, restart the daemon
  doqd add --fallback quic://...  same, as a fallback: asked only when every
                                  other upstream has failed
  doqd add --geo quic://...       same, as a geo-unblocking server: geo-blocked
                                  names go only to the pinned one of these
  doqd add-domain <domain>        treat the domain (and subdomains) as geo-blocked
  doqd remove-domain <domain>     remove it again (built-in domains stay)
  doqd remove <number|url>        remove an upstream, restart the daemon
  doqd status                     daemon, DNS integration and resolve check
  doqd geo                        pinned geo-unblocking server, ranking, proxies
  doqd geo reselect               re-evaluate the geo-unblocking servers now

Flags accepted by management commands:
  -c path    config file (default CONF)
`

func usage(w *os.File) {
	fmt.Fprint(w, strings.ReplaceAll(usageText, "CONF", defaultConf))
}
