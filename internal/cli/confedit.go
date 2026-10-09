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

// confServer — DoQ-сервер из конфига: сервер обхода (geo), обычный
// (upstream) или резервный (fallback).
type confServer struct {
	URL      string
	Fallback bool
	Geo      bool
}

func (c confServer) key() string {
	switch {
	case c.Geo:
		return "geo"
	case c.Fallback:
		return "fallback"
	}
	return "upstream"
}

// rank — порядок блоков в конфиге: geo, upstream, fallback.
func (c confServer) rank() int {
	switch {
	case c.Geo:
		return 0
	case c.Fallback:
		return 2
	}
	return 1
}

// serverLine распознаёт строку "geo|upstream|fallback <url>" по тем же
// правилам, что и config.Parse (первые два поля).
func serverLine(line string) (confServer, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return confServer{}, false
	}
	switch fields[0] {
	case "geo":
		return confServer{URL: fields[1], Geo: true}, true
	case "upstream":
		return confServer{URL: fields[1]}, true
	case "fallback":
		return confServer{URL: fields[1], Fallback: true}, true
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

// addServer вставляет сервер после последней строки того же вида; первый
// своего вида — перед первым сервером следующего блока (geo → upstream →
// fallback); если серверов нет вовсе — в конец.
func addServer(lines []string, srv confServer) ([]string, error) {
	same, anyLast, firstHigher := -1, -1, -1
	for i, l := range lines {
		s, ok := serverLine(l)
		if !ok {
			continue
		}
		if s.URL == srv.URL {
			return nil, fmt.Errorf("%s is already in the config (as %s)", srv.URL, s.key())
		}
		switch {
		case s.rank() == srv.rank():
			same = i
		case s.rank() > srv.rank() && firstHigher == -1:
			firstHigher = i
		}
		anyLast = i
	}
	at := len(lines) // индекс, перед которым вставить
	switch {
	case same != -1:
		at = same + 1
	case firstHigher != -1:
		at = firstHigher
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
			return nil, confServer{}, fmt.Errorf("refusing to remove the last geo/upstream server — " +
				"add another one first: doqd add [--geo] quic://...")
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

// domainLine распознаёт строку "geo-domain <домен>" и возвращает домен в
// нормализованном виде (нижний регистр, без точки на конце).
func domainLine(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) >= 2 && fields[0] == "geo-domain" {
		return strings.TrimSuffix(strings.ToLower(fields[1]), "."), true
	}
	return "", false
}

// addDomain вставляет geo-domain после последней такой строки, иначе после
// последнего сервера, иначе в конец.
func addDomain(lines []string, val string) ([]string, error) {
	d, err := config.GeoDomain(val)
	if err != nil {
		return nil, err
	}
	lastDomain, lastServer := -1, -1
	for i, l := range lines {
		if x, ok := domainLine(l); ok {
			if x == d {
				return nil, fmt.Errorf("%s is already in the config", d)
			}
			lastDomain = i
		}
		if _, ok := serverLine(l); ok {
			lastServer = i
		}
	}
	at := len(lines)
	switch {
	case lastDomain != -1:
		at = lastDomain + 1
	case lastServer != -1:
		at = lastServer + 1
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, "geo-domain "+d)
	return append(out, lines[at:]...), nil
}

// removeDomain удаляет geo-domain из конфига; встроенные домены в конфиге не
// лежат, поэтому удалить их нельзя.
func removeDomain(lines []string, val string) ([]string, error) {
	d := strings.TrimSuffix(strings.ToLower(val), ".")
	var out []string
	found := false
	for _, l := range lines {
		if x, ok := domainLine(l); ok && x == d {
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		return nil, fmt.Errorf("geo-domain %s is not in the config (built-in domains cannot be removed)", d)
	}
	return out, nil
}
