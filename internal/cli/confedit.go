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

// confServer — DoQ-сервер из конфига: основной (upstream) или резервный
// (fallback).
type confServer struct {
	URL      string
	Fallback bool
}

func (c confServer) key() string {
	if c.Fallback {
		return "fallback"
	}
	return "upstream"
}

// serverLine распознаёт строку "upstream <url>" или "fallback <url>" по тем же
// правилам, что и config.Parse (первые два поля).
func serverLine(line string) (confServer, bool) {
	fields := strings.Fields(line)
	if len(fields) >= 2 && (fields[0] == "upstream" || fields[0] == "fallback") {
		return confServer{URL: fields[1], Fallback: fields[0] == "fallback"}, true
	}
	return confServer{}, false
}

// confServers — все серверы в порядке строк конфига; по этому порядку
// нумерует `doqd list` и выбирает `doqd remove`.
func confServers(lines []string) []confServer {
	var out []confServer
	for _, l := range lines {
		if s, ok := serverLine(l); ok {
			out = append(out, s)
		}
	}
	return out
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

// addServer вставляет сервер после последней строки того же вида. Первый
// резервный встаёт после последнего основного, первый основной — перед
// первым резервным; если серверов нет вовсе — в конец.
func addServer(lines []string, srv confServer) ([]string, error) {
	same, anyLast, firstFallback := -1, -1, -1
	for i, l := range lines {
		s, ok := serverLine(l)
		if !ok {
			continue
		}
		if s.URL == srv.URL {
			return nil, fmt.Errorf("%s is already in the config (as %s)", srv.URL, s.key())
		}
		if s.Fallback == srv.Fallback {
			same = i
		}
		if s.Fallback && firstFallback == -1 {
			firstFallback = i
		}
		anyLast = i
	}
	at := len(lines) // индекс, перед которым вставить
	switch {
	case same != -1:
		at = same + 1
	case !srv.Fallback && firstFallback != -1:
		at = firstFallback
	case anyLast != -1:
		at = anyLast + 1
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, srv.key()+" "+srv.URL)
	return append(out, lines[at:]...), nil
}

func removeServer(lines []string, sel string) ([]string, confServer, error) {
	servers := confServers(lines)
	if len(servers) == 0 {
		return nil, confServer{}, fmt.Errorf("no upstreams in the config")
	}
	var target confServer
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(servers) {
			return nil, confServer{}, fmt.Errorf("no upstream #%d (config has %d)", n, len(servers))
		}
		target = servers[n-1]
	} else {
		found := false
		for _, s := range servers {
			if s.URL == sel {
				target, found = s, true
				break
			}
		}
		if !found {
			return nil, confServer{}, fmt.Errorf("upstream %s not found in the config", sel)
		}
	}
	if !target.Fallback {
		primaries := 0
		for _, s := range servers {
			if !s.Fallback {
				primaries++
			}
		}
		if primaries == 1 {
			return nil, confServer{}, fmt.Errorf("refusing to remove the last upstream — " +
				"add another one first: doqd add quic://...")
		}
	}
	var out []string
	removed := false
	for _, l := range lines {
		if s, ok := serverLine(l); ok && s.URL == target.URL && !removed {
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
	for _, u := range def.Geo {
		lines = append(lines, "geo "+u)
	}
	for _, u := range def.Upstreams {
		lines = append(lines, "upstream "+u)
	}
	for _, u := range def.Fallbacks {
		lines = append(lines, "fallback "+u)
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
