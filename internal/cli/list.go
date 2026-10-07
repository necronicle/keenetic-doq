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
	ups, listen := confServers(lines), confListen(lines)
	if !exists {
		def := config.Default()
		ups, listen = nil, def.Listen
		for _, u := range def.Upstreams {
			ups = append(ups, confServer{URL: u})
		}
		for _, u := range def.Fallbacks {
			ups = append(ups, confServer{URL: u, Fallback: true})
		}
		fmt.Printf("config %s not found — showing built-in defaults\n\n", *conf)
	}

	boot := confBootstrap(lines)
	results := make([]probeResult, len(ups))
	var wg sync.WaitGroup
	for i, u := range ups {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			results[i] = probe(u, boot, listProbeTimeout)
		}(i, u.URL)
	}
	wg.Wait()

	fmt.Printf("UPSTREAMS (%s):\n", *conf)
	fallbacks := false
	for i, u := range ups {
		tag := ""
		if u.Fallback {
			tag, fallbacks = "  [fallback]", true
		}
		if results[i].Err != nil {
			fmt.Printf(" %d. %-42s down   (%v)%s\n", i+1, u.URL, results[i].Err, tag)
		} else {
			fmt.Printf(" %d. %-42s alive  rtt %d ms%s\n", i+1, u.URL, results[i].RTT.Milliseconds(), tag)
		}
	}
	if fallbacks {
		fmt.Println("\n[fallback] is asked only when every other upstream has failed.")
	}

	if h := bootstrapHint(results); h != "" {
		fmt.Printf("\n%s\n", h)
	}

	state := "not running"
	if pid := daemonPID(); pid > 0 {
		state = fmt.Sprintf("running (pid %d)", pid)
	}
	fmt.Printf("\nlisten: %s   daemon: %s\n", listen, state)
	return 0
}
