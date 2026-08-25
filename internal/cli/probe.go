package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

const (
	probeTimeout = 5 * time.Second
	probeName    = "keenetic.com."
)

type probeResult struct {
	RTT time.Duration
	Err error
}

// probe делает один живой DoQ-запрос к серверу и меряет RTT.
func probe(rawURL string) probeResult {
	u, err := upstream.NewDoQ(rawURL)
	if err != nil {
		return probeResult{Err: err}
	}
	defer u.Close()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	m := new(dns.Msg)
	m.SetQuestion(probeName, dns.TypeA)
	start := time.Now()
	if _, err := u.Exchange(ctx, m); err != nil {
		return probeResult{Err: err}
	}
	return probeResult{RTT: time.Since(start)}
}

func runTest(args []string) int {
	fs := flag.NewFlagSet("doqd test", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: doqd test quic://host[:port]")
		return 2
	}
	url := fs.Arg(0)
	if _, err := upstream.NewDoQ(url); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("probing %s ... ", url)
	r := probe(url)
	if r.Err != nil {
		fmt.Println("FAIL")
		fmt.Fprintln(os.Stderr, "error:", r.Err)
		return 1
	}
	fmt.Printf("OK — answered in %d ms\n", r.RTT.Milliseconds())
	return 0
}
