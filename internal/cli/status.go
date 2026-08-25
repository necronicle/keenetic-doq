package cli

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/config"
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
	if err != nil { // конфиг без порта — берём порт из дефолта
		host = listen
		_, port, _ = net.SplitHostPort(config.Default().Listen)
	}
	if out, ok := ndmShow("show ip name-server"); !ok {
		fmt.Println("registration:    unknown (ndmc/ndmq not found — not on the router?)")
	} else if strings.Contains(out, host) && (port == "" || strings.Contains(out, port)) {
		fmt.Println("registration:    present in KeeneticOS name-servers")
	} else {
		fmt.Printf("registration:    NOT found — run: ndmc -c 'ip name-server %s'\n", listen)
	}

	// Прямо в doqd — проверяет сам слушатель и путь до DoQ-апстрима.
	reportResolve("resolve via doqd:", dialHost(host)+":"+port)
	// Через штатный ndnproxy — проверяет интеграцию с системным DNS.
	reportResolve("resolve via :53: ", net.JoinHostPort(dialHost(host), "53"))
	return 0
}

// dialHost подменяет адрес-джокер на loopback: к 0.0.0.0 не подключиться.
func dialHost(host string) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return host
}

func reportResolve(label, addr string) {
	m := new(dns.Msg)
	m.SetQuestion(probeName, dns.TypeA)
	c := &dns.Client{Timeout: probeTimeout}
	start := time.Now()
	resp, _, err := c.Exchange(m, addr)
	switch {
	case err != nil:
		fmt.Printf("%s FAIL (%v)\n", label, err)
	case resp.Rcode != dns.RcodeSuccess:
		fmt.Printf("%s %s\n", label, dns.RcodeToString[resp.Rcode])
	default:
		fmt.Printf("%s NOERROR, %d ms\n", label, time.Since(start).Milliseconds())
	}
}
