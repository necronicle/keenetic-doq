package cli

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func runStatus(args []string) int {
	fs := flag.NewFlagSet("doqd status", flag.ExitOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	fs.Parse(args)

	lines, _, err := readConfLines(*conf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	listen := confListen(lines)

	if pid := daemonPID(); pid > 0 {
		if up, ok := daemonUptime(pid); ok {
			fmt.Printf("daemon:          running (pid %d, uptime %s)\n", pid, up.Round(time.Second))
		} else {
			fmt.Printf("daemon:          running (pid %d)\n", pid)
		}
	} else {
		fmt.Println("daemon:          not running")
	}
	fmt.Printf("listen:          %s (udp+tcp)\n", listen)

	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = listen, ""
	}
	if out, ok := ndmShow("show ip name-server"); !ok {
		fmt.Println("registration:    unknown (ndmc/ndmq not found — not on the router?)")
	} else if strings.Contains(out, host) && (port == "" || strings.Contains(out, port)) {
		fmt.Println("registration:    present in KeeneticOS name-servers")
	} else {
		fmt.Printf("registration:    NOT found — run: ndmc -c 'ip name-server %s'\n", listen)
	}

	m := new(dns.Msg)
	m.SetQuestion(probeName, dns.TypeA)
	c := &dns.Client{Timeout: probeTimeout}
	start := time.Now()
	resp, _, err := c.Exchange(m, net.JoinHostPort(host, "53"))
	switch {
	case err != nil:
		fmt.Printf("resolve via :53: FAIL (%v)\n", err)
	case resp.Rcode != dns.RcodeSuccess:
		fmt.Printf("resolve via :53: %s\n", dns.RcodeToString[resp.Rcode])
	default:
		fmt.Printf("resolve via :53: NOERROR, %d ms\n", time.Since(start).Milliseconds())
	}
	return 0
}
