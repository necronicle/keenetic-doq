package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

func runAdd(args []string) int {
	fs := flag.NewFlagSet("doqd add", flag.ExitOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	force := fs.Bool("force", false, "add even if the live probe fails")
	fallback := fs.Bool("fallback", false, "add as a fallback: asked only when every upstream has failed")
	geoFlag := fs.Bool("geo", false, "add as a geo-unblocking server: geo-blocked names go only to the pinned one")
	fs.Parse(args)
	if *geoFlag && *fallback {
		fmt.Fprintln(os.Stderr, "error: --geo and --fallback exclude each other")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: doqd add [--force] [--geo|--fallback] quic://host[:port]")
		return 2
	}
	url := fs.Arg(0)
	if _, err := upstream.NewDoQ(url); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("probing %s ... ", url)
	if r := probe(url, bootstrapFor(*conf), listProbeTimeout); r.Err != nil {
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
	srv := confServer{URL: url, Fallback: *fallback, Geo: *geoFlag}
	lines, err = addServer(lines, srv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeConfLines(*conf, lines); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	for i, s := range confServers(lines) {
		if s.URL == url {
			fmt.Printf("added to %s as %s #%d\n", *conf, srv.key(), i+1)
		}
	}
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
	lines, removed, err := removeServer(lines, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeConfLines(*conf, lines); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("removed %s %s\n", removed.key(), removed.URL)
	if err := restartDaemon(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func runAddDomain(args []string) int {
	return runDomain("add-domain", args, addDomain, "added geo-domain")
}

func runRemoveDomain(args []string) int {
	return runDomain("remove-domain", args, removeDomain, "removed geo-domain")
}

func runDomain(name string, args []string, edit func([]string, string) ([]string, error), done string) int {
	fs := flag.NewFlagSet("doqd "+name, flag.ExitOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: doqd %s <domain>\n", name)
		return 2
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
	lines, err = edit(lines, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeConfLines(*conf, lines); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("%s %s\n", done, strings.TrimSuffix(strings.ToLower(fs.Arg(0)), "."))
	if err := restartDaemon(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
