// Package config parses doqd.conf — плоский файл "key value".
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen    string
	Upstreams []string
	CacheSize int
	MinTTL    time.Duration
	MaxTTL    time.Duration
	LogLevel  string
}

func Default() *Config {
	return &Config{
		Listen:    "127.0.0.1:5353",
		Upstreams: []string{"quic://unfiltered.adguard-dns.com", "quic://dns.adguard-dns.com"},
		CacheSize: 4096,
		MinTTL:    60 * time.Second,
		MaxTTL:    24 * time.Hour,
		LogLevel:  "info",
	}
}

func Parse(r io.Reader) (*Config, error) {
	cfg := Default()
	sawUpstream := false
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
	return cfg, sc.Err()
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
