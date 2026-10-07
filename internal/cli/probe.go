package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/config"
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
func probe(rawURL string, boot []string) probeResult {
	u, err := upstream.NewDoQ(rawURL)
	if err != nil {
		return probeResult{Err: err}
	}
	u.SetBootstrap(upstream.NewBootstrap(boot))
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
	fmt.Printf("probing %s ... ", url)
	r := probe(url, bootstrapFor(defaultConf))
	if r.Err != nil {
		fmt.Println("FAIL")
		fmt.Fprintln(os.Stderr, "error:", r.Err)
		if h := bootstrapHint([]probeResult{r}); h != "" {
			fmt.Fprintln(os.Stderr, h)
		}
		return 1
	}
	fmt.Printf("OK — answered in %d ms\n", r.RTT.Milliseconds())
	return 0
}
