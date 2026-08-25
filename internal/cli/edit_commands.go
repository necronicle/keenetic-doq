package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

func runAdd(args []string) int {
	fs := flag.NewFlagSet("doqd add", flag.ExitOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	force := fs.Bool("force", false, "add even if the live probe fails")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: doqd add [--force] quic://host[:port]")
		return 2
	}
	url := fs.Arg(0)
	if _, err := upstream.NewDoQ(url); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("probing %s ... ", url)
	if r := probe(url); r.Err != nil {
		fmt.Println("FAIL")
		if !*force {
			fmt.Fprintf(os.Stderr, "error: %v\nnot added — re-run with --force to add anyway\n", r.Err)
			return 1
		}
		fmt.Println("adding anyway (--force)")
	} else {
		fmt.Printf("OK (%d ms)\n", r.RTT.Milliseconds())
	}

	lines, exists, err := readConfLines(*conf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !exists {
		lines = defaultConfLines()
		fmt.Printf("config %s not found — creating it with defaults, review the listen address\n", *conf)
	}
	lines, err = addUpstream(lines, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeConfLines(*conf, lines); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("added to %s (upstream #%d)\n", *conf, len(confUpstreams(lines)))
	if err := restartDaemon(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func runRemove(args []string) int {
	fs := flag.NewFlagSet("doqd remove", flag.ExitOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: doqd remove <number|quic://url>  (numbers as shown by: doqd list)")
		return 2
	}
	lines, exists, err := readConfLines(*conf)
	if err == nil && !exists {
		err = fmt.Errorf("config %s not found", *conf)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	lines, removed, err := removeUpstream(lines, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeConfLines(*conf, lines); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("removed %s\n", removed)
	if err := restartDaemon(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
