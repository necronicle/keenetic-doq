package cli

import (
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/necronicle/keenetic-doq/internal/config"
)

func runList(args []string) int {
	fs := flag.NewFlagSet("doqd list", flag.ExitOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	fs.Parse(args)

	lines, exists, err := readConfLines(*conf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ups, listen := confUpstreams(lines), confListen(lines)
	if !exists {
		def := config.Default()
		ups, listen = def.Upstreams, def.Listen
		fmt.Printf("config %s not found — showing built-in defaults\n\n", *conf)
	}

	results := make([]probeResult, len(ups))
	var wg sync.WaitGroup
	for i, u := range ups {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			results[i] = probe(u)
		}(i, u)
	}
	wg.Wait()

	fmt.Printf("UPSTREAMS (%s):\n", *conf)
	for i, u := range ups {
		if results[i].Err != nil {
			fmt.Printf(" %d. %-42s down   (%v)\n", i+1, u, results[i].Err)
		} else {
			fmt.Printf(" %d. %-42s alive  rtt %d ms\n", i+1, u, results[i].RTT.Milliseconds())
		}
	}

	state := "not running"
	if pid := daemonPID(); pid > 0 {
		state = fmt.Sprintf("running (pid %d)", pid)
	}
	fmt.Printf("\nlisten: %s   daemon: %s\n", listen, state)
	return 0
}
