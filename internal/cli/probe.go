package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/config"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

const (
	// listProbeTimeout хватает на bootstrap (3 с) и дозвон с лесенкой по
	// адресам; doqd test ждёт дольше — его задача показать, где застряло.
	listProbeTimeout = 8 * time.Second
	testProbeTimeout = 12 * time.Second
	probeName        = "keenetic.com."
)

type dialAttempt struct {
	Addr string
	Took time.Duration
	Err  error
}

type probeResult struct {
	RTT time.Duration // от начала до ответа, включая bootstrap и дозвон
	Err error

	// Этапы — для doqd test.
	Boot    upstream.Resolution
	BootErr error
	Dials   []dialAttempt
	Query   time.Duration // сам обмен, без дозвона
}

// probe делает один живой DoQ-запрос к серверу и записывает, сколько занял
// каждый этап.
func probe(rawURL string, boot []string, timeout time.Duration) probeResult {
	u, err := upstream.NewDoQ(rawURL)
	if err != nil {
		return probeResult{Err: err}
	}
	defer u.Close()
	pd := upstream.NewBootstrap(boot)
	u.SetBootstrap(pd)
	var r probeResult
	var mu sync.Mutex
	u.OnDialAttempt = func(addr string, took time.Duration, err error) {
		mu.Lock()
		r.Dials = append(r.Dials, dialAttempt{Addr: addr, Took: took, Err: err})
		mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	host, _, _ := net.SplitHostPort(u.Address())
	// Адреса ищутся отдельно, чтобы знать, кто ответил; DoQ возьмёт их из кеша.
	r.Boot, r.BootErr = pd.Resolve(ctx, host)
	if r.BootErr != nil {
		r.Err = r.BootErr
		return r
	}
	m := new(dns.Msg)
	m.SetQuestion(probeName, dns.TypeA)
	_, query, err := u.ExchangeTimed(ctx, m) // время обмена без дозвона
	r.RTT = time.Since(start)
	mu.Lock()
	defer mu.Unlock()
	r.Query, r.Err = query, err
	return r
}

func ms(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }

// formatStages расписывает пробу по этапам: где ушло время и где застряло.
func formatStages(r probeResult) string {
	var b strings.Builder
	switch {
	case r.BootErr != nil:
		fmt.Fprintf(&b, "  bootstrap  FAILED after %s\n", ms(r.Boot.Took))
		return b.String()
	case r.Boot.Server == "" && r.Boot.Cached:
		fmt.Fprintf(&b, "  bootstrap  %s (cached)\n", ipList(r.Boot.IPs))
	case r.Boot.Server == "":
		fmt.Fprintf(&b, "  bootstrap  not needed, the address is an IP\n")
	default:
		fmt.Fprintf(&b, "  bootstrap  %d address(es) from %s over %s in %s: %s\n",
			len(r.Boot.IPs), r.Boot.Server, r.Boot.Proto, ms(r.Boot.Took), ipList(r.Boot.IPs))
	}
	for _, d := range r.Dials {
		switch {
		case d.Err == nil:
			fmt.Fprintf(&b, "  connect    %s  OK in %s\n", d.Addr, ms(d.Took))
		case errors.Is(d.Err, upstream.ErrOvertaken):
			fmt.Fprintf(&b, "  connect    %s  no answer within %s, another address was faster\n", d.Addr, ms(d.Took))
		default:
			fmt.Fprintf(&b, "  connect    %s  failed after %s: %v\n", d.Addr, ms(d.Took), d.Err)
		}
	}
	if r.Err == nil {
		fmt.Fprintf(&b, "  query      %s A  answered in %s\n", strings.TrimSuffix(probeName, "."), ms(r.Query))
	}
	return b.String()
}

func ipList(ips []net.IP) string {
	parts := make([]string, len(ips))
	for i, ip := range ips {
		parts[i] = ip.String()
	}
	return strings.Join(parts, ", ")
}

// dialHint — подсказка, когда адреса нашлись, но ни с одним не удалось
// установить QUIC-соединение.
func dialHint(r probeResult) string {
	if r.Err == nil || r.BootErr != nil || len(r.Dials) == 0 {
		return ""
	}
	for _, d := range r.Dials {
		if d.Err == nil {
			return ""
		}
	}
	return "hint: no address of this server completed a QUIC handshake.\n" +
		"If this server works from other networks, your ISP is most likely blocking it\n" +
		"(DNS-over-QUIC runs on udp/853). Nothing in doqd can get around that; prefer the\n" +
		"upstreams that are alive in `doqd list`."
}

// bootstrapHint — подсказка, когда имя апстрима не нашлось: ни один bootstrap-
// сервер не дал ответа. Типичная причина — провайдер выбрасывает DNS-запросы
// на 53-й порт с именами сервисов обхода блокировок.
func bootstrapHint(results []probeResult) string {
	for _, r := range results {
		if errors.Is(r.Err, upstream.ErrBootstrapNoAnswer) {
			return "hint: no bootstrap server resolved the upstream name, the DoQ server itself\n" +
				"was never contacted. If your ISP filters DNS on port 53, the bootstrap list in\n" +
				defaultConf + " needs a server on another port, e.g. \"bootstrap 77.88.8.8:1253\"\n" +
				"(Yandex DNS); after editing: /opt/etc/init.d/S56doqd restart"
		}
	}
	return ""
}

// bootstrapFor достаёт bootstrap-серверы из конфига; нет конфига — дефолты.
func bootstrapFor(conf string) []string {
	lines, exists, err := readConfLines(conf)
	if err != nil || !exists {
		return config.Default().Bootstrap
	}
	return confBootstrap(lines)
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
	fmt.Printf("probing %s\n", url)
	r := probe(url, bootstrapFor(defaultConf), testProbeTimeout)
	fmt.Print(formatStages(r))
	if r.Err != nil {
		fmt.Println("FAIL")
		fmt.Fprintln(os.Stderr, "error:", r.Err)
		for _, h := range []string{bootstrapHint([]probeResult{r}), dialHint(r)} {
			if h != "" {
				fmt.Fprintln(os.Stderr, h)
			}
		}
		return 1
	}
	fmt.Printf("OK — answered in %d ms\n", r.RTT.Milliseconds())
	return 0
}
