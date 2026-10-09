package cli

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/necronicle/keenetic-doq/internal/geo"
)

// readSnap — снимок демона; без работающего демона снимок устарел и не нужен.
func readSnap() *geo.Snapshot {
	if daemonPID() == 0 {
		return nil
	}
	s, err := geo.ReadSnapshot(geo.DefaultSnapshotPath)
	if err != nil {
		return nil
	}
	return s
}

// poolProxies — прокси пула закреплённого; прочие адреса из его ответов
// (настоящие адреса гео-имён без подмены) прокси не считаются.
func poolProxies(s *geo.Snapshot) (pool []geo.ProxyStatus, other int) {
	for _, p := range s.Proxies {
		if p.Pool {
			pool = append(pool, p)
		} else {
			other++
		}
	}
	return pool, other
}

func medianInt(xs []int64) int64 {
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// geoInfo — прокси сервера обхода: у закреплённого — живая проверка, у
// остальных — итог последней оценки.
func geoInfo(url string, snap *geo.Snapshot) string {
	if snap == nil {
		return ""
	}
	if pool, _ := poolProxies(snap); url == snap.Pinned && len(pool) > 0 {
		ok := 0
		var ms []int64
		for _, p := range pool {
			if p.State == "healthy" {
				ok++
				if p.MedianMs > 0 {
					ms = append(ms, p.MedianMs)
				}
			}
		}
		s := fmt.Sprintf("  proxies %d/%d ok", ok, len(pool))
		if len(ms) > 0 {
			s += fmt.Sprintf(", tls %d ms", medianInt(ms))
		}
		return s
	}
	for _, r := range snap.Ranking {
		if r.URL == url {
			s := fmt.Sprintf("  proxies %d/%d alive", r.Alive, r.Total)
			if r.MedianTLSMs > 0 {
				s += fmt.Sprintf(", tls %d ms", r.MedianTLSMs)
			}
			return s
		}
	}
	return ""
}

func formatList(conf string, ups []confServer, results []probeResult, snap *geo.Snapshot) string {
	var b strings.Builder
	hasGeo, fallbacks := false, false
	for _, u := range ups {
		hasGeo = hasGeo || u.Geo
		fallbacks = fallbacks || u.Fallback
	}
	line := func(i int, u confServer, mark string) {
		tag := ""
		if u.Fallback {
			tag = "  [fallback]"
		}
		if results[i].Err != nil {
			fmt.Fprintf(&b, " %s%d. %-42s down   (%v)%s\n", mark, i+1, u.URL, results[i].Err, tag)
			return
		}
		info := ""
		if u.Geo {
			info = geoInfo(u.URL, snap)
		}
		fmt.Fprintf(&b, " %s%d. %-42s alive  rtt %d ms%s%s\n", mark, i+1, u.URL, results[i].RTT.Milliseconds(), info, tag)
	}
	if !hasGeo {
		fmt.Fprintf(&b, "UPSTREAMS (%s):\n", conf)
		for i, u := range ups {
			line(i, u, "")
		}
	} else {
		fmt.Fprintf(&b, "GEO — geo-blocked names go only to the pinned server, marked * (%s):\n", conf)
		for i, u := range ups {
			if u.Geo {
				mark := "  "
				if snap != nil && snap.Pinned == u.URL {
					mark = "* "
				}
				line(i, u, mark)
			}
		}
		b.WriteString("FAST — every other name goes to the fastest of these and the servers above:\n")
		for i, u := range ups {
			if !u.Geo {
				line(i, u, "  ")
			}
		}
	}
	if fallbacks {
		b.WriteString("\n[fallback] is asked only when every other upstream has failed.\n")
	}
	return b.String()
}

func formatGeo(s *geo.Snapshot, probes int) string {
	var b strings.Builder
	switch {
	case s.Pinned == "":
		b.WriteString("pinned:     none\n")
	case s.Since.IsZero():
		fmt.Fprintf(&b, "pinned:     %s (temporary, until the first evaluation)\n", s.Pinned)
	default:
		fmt.Fprintf(&b, "pinned:     %s (since %s)\n", s.Pinned, s.Since.Local().Format("2006-01-02 15:04"))
	}
	switch {
	case s.Evaluating:
		b.WriteString("evaluation: running\n")
	case s.EvaluatedAt.IsZero():
		b.WriteString("evaluation: none yet\n")
	default:
		fmt.Fprintf(&b, "evaluation: %s\n", s.EvaluatedAt.Local().Format("2006-01-02 15:04"))
	}
	if s.Inconclusive && !s.Evaluating {
		fmt.Fprintf(&b, "            last attempt %s was inconclusive (network down?), kept the previous choice; retry at %s\n",
			s.AttemptedAt.Local().Format("2006-01-02 15:04"), s.RetryAt.Local().Format("15:04"))
	}
	if len(s.Ranking) > 0 {
		b.WriteString("\nRANKING (last evaluation):\n")
		for i, r := range s.Ranking {
			fmt.Fprintf(&b, " %d. %-34s coverage %d/%d  proxies %d/%d alive", i+1, r.URL, r.Coverage, probes, r.Alive, r.Total)
			if r.MedianTLSMs > 0 {
				fmt.Fprintf(&b, "  tls %d ms", r.MedianTLSMs)
			}
			if r.Err != "" {
				fmt.Fprintf(&b, "  (%s)", r.Err)
			}
			b.WriteString("\n")
		}
	}
	if pool, other := poolProxies(s); len(pool) > 0 || other > 0 {
		b.WriteString("\nPINNED SERVER PROXIES (checked every 30 s):\n")
		for _, p := range pool {
			fmt.Fprintf(&b, "  %-40s %-8s", p.Addr, p.State)
			if p.MedianMs > 0 {
				fmt.Fprintf(&b, " tls %d ms", p.MedianMs)
			}
			b.WriteString("\n")
		}
		if other > 0 {
			fmt.Fprintf(&b, "  (+ other addresses from its answers: %d)\n", other)
		}
	}
	fmt.Fprintf(&b, "\nlearned geo-blocked names: %d\n", s.LearnedGeo)
	return b.String()
}

func geoStatusLine(s *geo.Snapshot) string {
	if s == nil || s.Pinned == "" {
		return "geo:             no state yet (daemon starting or evaluating)"
	}
	pool, _ := poolProxies(s)
	ok := 0
	for _, p := range pool {
		if p.State == "healthy" {
			ok++
		}
	}
	since := "(temporary, until the first evaluation)"
	if !s.Since.IsZero() {
		since = "since " + s.Since.Local().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("geo:             pinned %s %s, proxies %d/%d healthy",
		strings.TrimPrefix(s.Pinned, "quic://"), since, ok, len(pool))
}

func runGeo(args []string) int {
	fs := flag.NewFlagSet("doqd geo", flag.ExitOnError)
	fs.Parse(args)
	switch {
	case fs.NArg() == 1 && fs.Arg(0) == "reselect":
		return runGeoReselect()
	case fs.NArg() != 0:
		fmt.Fprintln(os.Stderr, "usage: doqd geo [reselect]")
		return 2
	}
	s := readSnap()
	if s == nil {
		fmt.Fprintln(os.Stderr, "no geo state: the daemon is not running or has no geo servers (see: doqd list)")
		return 1
	}
	fmt.Print(formatGeo(s, len(geo.ProbeDomains)))
	return 0
}

func runGeoReselect() int {
	pid := daemonPID()
	if pid == 0 {
		fmt.Fprintln(os.Stderr, "error: the daemon is not running")
		return 1
	}
	var before time.Time
	if s, err := geo.ReadSnapshot(geo.DefaultSnapshotPath); err == nil {
		before = s.AttemptedAt
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Print("re-evaluating geo-unblocking servers ... ")
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		time.Sleep(500 * time.Millisecond)
		s, err := geo.ReadSnapshot(geo.DefaultSnapshotPath)
		if err == nil && s.AttemptedAt.After(before) && !s.Evaluating {
			if s.Inconclusive {
				fmt.Print("inconclusive\n\n")
				fmt.Print(formatGeo(s, len(geo.ProbeDomains)))
				fmt.Fprintln(os.Stderr, "no server unblocked any probe domain — is the network up? The previous choice is kept.")
				return 1
			}
			fmt.Print("done\n\n")
			fmt.Print(formatGeo(s, len(geo.ProbeDomains)))
			return 0
		}
	}
	fmt.Println("timeout")
	fmt.Fprintln(os.Stderr, "the daemon did not finish the evaluation in 45 s — check: doqd geo")
	return 1
}
