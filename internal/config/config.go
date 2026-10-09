// Package config parses doqd.conf — плоский файл "key value".
package config

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

type Config struct {
	Listen string
	// Geo — серверы обхода геоблокировок: геоблокированные имена идут только
	// в один из них, закреплённый.
	Geo []string
	// Upstreams — обычные резолверы: остальные имена идут в быстрейший из них
	// и серверов обхода.
	Upstreams []string
	// Fallbacks спрашиваются, только когда отказали все остальные.
	Fallbacks []string
	// GeoDomains — свои геоблокированные домены сверх встроенного списка.
	GeoDomains []string
	Bootstrap  []string
	CacheSize  int
	MinTTL     time.Duration
	MaxTTL     time.Duration
	LogLevel   string
}

func Default() *Config {
	return &Config{
		Listen: "127.0.0.1:5354",
		// Серверы обхода: для заблокированных по геолокации сервисов отдают
		// адреса своих прокси. comss убран в 0.3.5 — медленные прокси.
		Geo: []string{"quic://geohide.ru", "quic://dns.dns-ai.ru"},
		// Обычные резолверы без фильтрации из разных сетей. AdGuard сюда не
		// годится — его режет ТСПУ.
		Upstreams: []string{"quic://dns.quad9.net", "quic://p0.freedns.controld.com"},
		Bootstrap: append([]string(nil), upstream.DefaultBootstrapServers...),
		CacheSize: 4096,
		MinTTL:    60 * time.Second,
		MaxTTL:    24 * time.Hour,
		LogLevel:  "info",
	}
}

func Parse(r io.Reader) (*Config, error) {
	cfg := Default()
	sawServers, sawBootstrap := false, false
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: %q needs a value", line, fields[0])
		}
		key, val := fields[0], fields[1]
		switch key {
		case "listen":
			cfg.Listen = val
		case "geo", "upstream", "fallback":
			// Первая же строка любого из трёх ключей отменяет все три дефолта:
			// иначе свой список молча дополнялся бы встроенным.
			if !sawServers {
				cfg.Geo, cfg.Upstreams, cfg.Fallbacks = nil, nil, nil
				sawServers = true
			}
			switch key {
			case "geo":
				cfg.Geo = append(cfg.Geo, val)
			case "upstream":
				cfg.Upstreams = append(cfg.Upstreams, val)
			default:
				cfg.Fallbacks = append(cfg.Fallbacks, val)
			}
		case "geo-domain":
			d, err := GeoDomain(val)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			cfg.GeoDomains = append(cfg.GeoDomains, d)
		case "bootstrap":
			addr, err := BootstrapAddr(val)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			if !sawBootstrap {
				cfg.Bootstrap = nil
				sawBootstrap = true
			}
			cfg.Bootstrap = append(cfg.Bootstrap, addr)
		case "cache_size":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("line %d: bad cache_size %q", line, val)
			}
			cfg.CacheSize = n
		case "min_ttl", "max_ttl":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("line %d: bad %s %q", line, key, val)
			}
			if key == "min_ttl" {
				cfg.MinTTL = time.Duration(n) * time.Second
			} else {
				cfg.MaxTTL = time.Duration(n) * time.Second
			}
		case "log":
			cfg.LogLevel = val
		default:
			return nil, fmt.Errorf("line %d: unknown key %q", line, key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(cfg.Upstreams) == 0 && len(cfg.Geo) == 0 {
		return nil, fmt.Errorf("fallback servers need at least one upstream or geo server: " +
			"a fallback is asked only when every other server has failed")
	}
	listenHost, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		listenHost = cfg.Listen
	}
	for _, b := range cfg.Bootstrap {
		host, _, _ := net.SplitHostPort(b)
		ip := net.ParseIP(host)
		if host == listenHost || (ip != nil && ip.IsLoopback()) {
			return nil, fmt.Errorf("bootstrap %s points back at this router: any DNS here is "+
				"the router's own proxy, which forwards to doqd — use an external resolver", b)
		}
	}
	return cfg, nil
}

// GeoDomain приводит домен из `geo-domain` к виду для сравнения: нижний
// регистр, без завершающей точки. Однословные имена не принимаются — суффикс
// вроде "com" увёл бы в гео-полосу половину интернета.
func GeoDomain(val string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(val), ".")
	if _, ok := dns.IsDomainName(d); !ok || !strings.Contains(d, ".") || strings.Contains(d, "..") {
		return "", fmt.Errorf("bad geo-domain %q: want a domain like example.com", val)
	}
	return d, nil
}

// BootstrapAddr приводит значение к IP:порт. Имя тут недопустимо: его пришлось
// бы резолвить системным резолвером, ради обхода которого bootstrap и заведён.
func BootstrapAddr(val string) (string, error) {
	host, port, err := net.SplitHostPort(val)
	if err != nil {
		host, port = val, "53"
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("bad bootstrap %q: must be an IP address, not a name", val)
	}
	return net.JoinHostPort(host, port), nil
}

// Load читает конфиг из файла; отсутствие файла — ошибка (main сам решает
// использовать Default при отсутствии).
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}
