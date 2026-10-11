package cli

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dnsmasqState — настройки dnsmasq на OpenWRT, которые решают, идут ли
// запросы в doqd: список `server` и `noresolv` из /etc/config/dhcp (UCI) и
// строки `server=` конфига, с которым dnsmasq запущен сейчас.
type dnsmasqState struct {
	// UCI — false, если uci нет или он не ответил: остальное тогда неизвестно.
	UCI      bool
	Servers  []string
	NoResolv bool
	// Running — `server=` из сгенерированного конфига dnsmasq; RunningKnown —
	// false, если файла нет (dnsmasq не запущен или путь другой).
	Running      []string
	RunningKnown bool
}

// dnsmasqRunningGlob — конфиги, которые init-скрипт dnsmasq генерирует из
// UCI при каждом старте, по одному на секцию dnsmasq.
const dnsmasqRunningGlob = "/var/etc/dnsmasq.conf.*"

// readDnsmasq читает первую (штатную) секцию dnsmasq — её настраивает
// инсталлер.
func readDnsmasq() dnsmasqState {
	var st dnsmasqState
	if out, err := exec.Command("uci", "-q", "show", "dhcp.@dnsmasq[0]").Output(); err == nil {
		st.UCI = true
		st.Servers, st.NoResolv = parseUCIDnsmasq(string(out))
	}
	paths, _ := filepath.Glob(dnsmasqRunningGlob)
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		st.RunningKnown = true
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "server="); ok {
				st.Running = append(st.Running, v)
			}
		}
		f.Close()
	}
	return st
}

// parseUCIDnsmasq разбирает вывод `uci show dhcp.@dnsmasq[0]`: строки вида
// dhcp.cfg01411c.server='127.0.0.1#5354' '/pool.ntp.org/77.88.8.8'
// dhcp.cfg01411c.noresolv='1'
func parseUCIDnsmasq(out string) (servers []string, noResolv bool) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k[strings.LastIndexByte(k, '.')+1:] {
		case "server":
			servers = append(servers, uciValues(v)...)
		case "noresolv":
			vals := uciValues(v)
			noResolv = len(vals) == 1 && uciTrue(vals[0])
		}
	}
	return servers, noResolv
}

// uciTrue — истинное булево значение UCI (get_bool в
// /lib/functions.sh принимает эти же слова).
func uciTrue(v string) bool {
	switch v {
	case "1", "on", "true", "yes", "enabled":
		return true
	}
	return false
}

// uciValues разбирает значение из `uci show`: одно или несколько слов в
// одинарных кавычках через пробел. Кавычка внутри значения выглядит как '\''.
func uciValues(v string) []string {
	var out []string
	var cur strings.Builder
	in := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\'' && !in:
			in = true
		case c == '\'' && in:
			if strings.HasPrefix(v[i:], `'\''`) {
				cur.WriteByte('\'')
				i += 3
				continue
			}
			in = false
			out = append(out, cur.String())
			cur.Reset()
		case in:
			cur.WriteByte(c)
		case c != ' ':
			cur.WriteByte(c) // значение без кавычек
			if i == len(v)-1 || v[i+1] == ' ' {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
	}
	return out
}

// dnsmasqTarget — запись `server` dnsmasq, ведущая в doqd: адрес#порт.
func dnsmasqTarget(host, port string) string {
	h := dialHost(host)
	if ip := net.ParseIP(h); ip != nil && ip.To4() == nil {
		h = ip.String() // [::1] → ::1: dnsmasq пишет IPv6 без скобок
	}
	return h + "#" + port
}

// otherUpstream — сервер dnsmasq, который отвечает на любые имена (без
// /домена/) и не ведёт в doqd.
func otherUpstream(server, target string) bool {
	return server != "" && !strings.HasPrefix(server, "/") && server != target
}

// dnsmasqReport — строки `doqd status` про связку dnsmasq → doqd.
func dnsmasqReport(st dnsmasqState, target string) []string {
	fix := []string{
		"                     uci add_list dhcp.@dnsmasq[0].server='" + target + "'",
		"                     uci set dhcp.@dnsmasq[0].noresolv='1'",
		"                     uci commit dhcp",
		"                     /etc/init.d/dnsmasq restart",
	}
	if !st.UCI {
		return []string{"dnsmasq:         unknown (uci not found or no dnsmasq section)"}
	}
	var out []string
	inUCI := contains(st.Servers, target)
	switch {
	case !inUCI:
		out = append(out, "dnsmasq:         NOT forwarding to doqd ("+target+" is not in its servers). Fix:")
		out = append(out, fix...)
		return out
	case st.RunningKnown && !contains(st.Running, target):
		out = append(out,
			"dnsmasq:         "+target+" is in /etc/config/dhcp, but the running dnsmasq",
			"                 does not use it yet. Apply it: /etc/init.d/dnsmasq restart")
	default:
		out = append(out, "dnsmasq:         forwards to doqd ("+target+")")
	}

	var others []string
	for _, s := range st.Servers {
		if otherUpstream(s, target) {
			others = append(others, s)
		}
	}
	if !st.NoResolv {
		others = append(others, "the WAN DNS servers (noresolv is off)")
	}
	if len(others) > 0 {
		out = append(out,
			"other DNS:       "+strings.Join(others, ", "),
			"                 dnsmasq sends each query to one of its servers, not always",
			"                 to doqd. Then a geo-blocked service can get its real address,",
			"                 and the query leaves the router unencrypted. To send every",
			"                 query through doqd, remove the other servers and run:",
			"                     uci set dhcp.@dnsmasq[0].noresolv='1'",
			"                     uci commit dhcp && /etc/init.d/dnsmasq restart")
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
