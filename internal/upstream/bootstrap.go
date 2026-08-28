package upstream

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
)

// DefaultBootstrapServers — обычные DNS-серверы, через которые резолвятся имена
// DoQ-апстримов. Только IP-литералы: имя здесь потребовало бы резолва, а резолв
// именно то, чего bootstrap избегает.
var DefaultBootstrapServers = []string{"77.88.8.8:53", "8.8.8.8:53", "1.1.1.1:53"}

const bootstrapTimeout = 3 * time.Second

// Bootstrap резолвит имя DoQ-апстрима в IP.
type Bootstrap interface {
	LookupIP(ctx context.Context, host string) (net.IP, error)
}

// PlainDNS спрашивает обычным DNS явно заданные серверы — в обход системного
// резолвера. На Keenetic системный резолвер это 127.0.0.1:53 (ndnproxy), в
// списке серверов которого прописан сам doqd: пойти туда за адресом апстрима
// значит послать запрос самому себе и повесить DNS роутера.
type PlainDNS struct {
	Servers []string
}

func NewBootstrap(servers []string) *PlainDNS {
	if len(servers) == 0 {
		servers = DefaultBootstrapServers
	}
	return &PlainDNS{Servers: servers}
}

func (b *PlainDNS) LookupIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(host), dns.TypeA)
	c := &dns.Client{Timeout: bootstrapTimeout}
	var lastErr error
	for _, s := range b.Servers {
		cctx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
		resp, _, err := c.ExchangeContext(cctx, q, s)
		cancel()
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", s, err)
			continue
		}
		for _, rr := range resp.Answer {
			if a, ok := rr.(*dns.A); ok {
				return a.A, nil
			}
		}
		lastErr = fmt.Errorf("%s: no A record for %s", s, host)
	}
	return nil, fmt.Errorf("bootstrap lookup %s: %w", host, lastErr)
}
