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

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

type Config struct {
	Listen    string
	Upstreams []string
	Bootstrap []string
	CacheSize int
	MinTTL    time.Duration
	MaxTTL    time.Duration
	LogLevel  string
}

func Default() *Config {
	return &Config{
		Listen:    "127.0.0.1:5354",
		Upstreams: []string{"quic://dns.comss.one", "quic://dns.quad9.net"},
		Bootstrap: append([]string(nil), upstream.DefaultBootstrapServers...),
		CacheSize: 4096,
		MinTTL:    60 * time.Second,
		MaxTTL:    24 * time.Hour,
		LogLevel:  "info",
	}
}

func Parse(r io.Reader) (*Config, error) {
	cfg := Default()
	sawUpstream, sawBootstrap := false, false
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
		case "upstream":
			if !sawUpstream {
				cfg.Upstreams = nil
				sawUpstream = true
			}
			cfg.Upstreams = append(cfg.Upstreams, val)
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
