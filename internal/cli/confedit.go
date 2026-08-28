package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/necronicle/keenetic-doq/internal/config"
)

// readConfLines читает конфиг построчно; отсутствие файла — не ошибка.
func readConfLines(path string) ([]string, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, true, sc.Err()
}

// isUpstreamLine распознаёт строку "upstream <url>" по тем же правилам,
// что и config.Parse (первые два поля).
func isUpstreamLine(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) >= 2 && fields[0] == "upstream" {
		return fields[1], true
	}
	return "", false
}

func confUpstreams(lines []string) []string {
	var ups []string
	for _, l := range lines {
		if u, ok := isUpstreamLine(l); ok {
			ups = append(ups, u)
		}
	}
	return ups
}

// confBootstrap читает bootstrap-серверы прямо из строк конфига, чтобы пробы
// CLI ходили за адресами апстримов туда же, куда и демон.
func confBootstrap(lines []string) []string {
	var out []string
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[0] == "bootstrap" {
			if addr, err := config.BootstrapAddr(fields[1]); err == nil {
				out = append(out, addr)
			}
		}
	}
	if len(out) == 0 {
		return config.Default().Bootstrap
	}
	return out
}

func confListen(lines []string) string {
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[0] == "listen" {
			return fields[1]
		}
	}
	return config.Default().Listen
}

func addUpstream(lines []string, url string) ([]string, error) {
	last := -1
	for i, l := range lines {
		u, ok := isUpstreamLine(l)
		if !ok {
			continue
		}
		if u == url {
			return nil, fmt.Errorf("upstream %s is already in the config", url)
		}
		last = i
	}
	entry := "upstream " + url
	out := make([]string, 0, len(lines)+1)
	if last == -1 {
		out = append(out, lines...)
		return append(out, entry), nil
	}
	out = append(out, lines[:last+1]...)
	out = append(out, entry)
	return append(out, lines[last+1:]...), nil
}

func removeUpstream(lines []string, sel string) ([]string, string, error) {
	ups := confUpstreams(lines)
	if len(ups) == 0 {
		return nil, "", fmt.Errorf("no upstreams in the config")
	}
	target := sel
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(ups) {
			return nil, "", fmt.Errorf("no upstream #%d (config has %d)", n, len(ups))
		}
		target = ups[n-1]
	}
	found := false
	for _, u := range ups {
		if u == target {
			found = true
		}
	}
	if !found {
		return nil, "", fmt.Errorf("upstream %s not found in the config", target)
	}
	if len(ups) == 1 {
		return nil, "", fmt.Errorf("refusing to remove the last upstream — add another one first: doqd add quic://...")
	}
	var out []string
	removed := false
	for _, l := range lines {
		if u, ok := isUpstreamLine(l); ok && u == target && !removed {
			removed = true
			continue
		}
		out = append(out, l)
	}
	return out, target, nil
}

// writeConfLines атомарно перезаписывает конфиг: tmp-файл в том же
// каталоге + rename, права 0644.
func writeConfLines(path string, lines []string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".doqd.conf.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	for _, l := range lines {
		if _, err := fmt.Fprintln(tmp, l); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func defaultConfLines() []string {
	def := config.Default()
	lines := []string{
		"# doqd — DNS-over-QUIC forwarder. https://github.com/necronicle/keenetic-doq",
		"listen " + def.Listen,
	}
	for _, u := range def.Upstreams {
		lines = append(lines, "upstream "+u)
	}
	for _, b := range def.Bootstrap {
		lines = append(lines, "bootstrap "+b)
	}
	return append(lines,
		fmt.Sprintf("cache_size %d", def.CacheSize),
		fmt.Sprintf("min_ttl %d", int(def.MinTTL.Seconds())),
		fmt.Sprintf("max_ttl %d", int(def.MaxTTL.Seconds())),
		"log "+def.LogLevel)
}
