package upstream

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// startTestDNS поднимает обычный UDP DNS-сервер; ip — один адрес или
// несколько через запятую.
func startTestDNS(t *testing.T, ip string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, q *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(q)
		for _, one := range strings.Split(ip, ",") {
			rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN A " + one)
			resp.Answer = append(resp.Answer, rr)
		}
		w.WriteMsg(resp)
	})
	srv := &dns.Server{PacketConn: pc, Handler: mux}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	return pc.LocalAddr().String()
}

// startSilentUDP — сервер, до которого запросы доходят, но ответа нет: так
// выглядит 53-й порт, когда DPI провайдера выбрасывает запрос по имени.
func startSilentUDP(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String()
}

// startTCPOnlyDNS — UDP на порту молчит, по TCP тот же порт отвечает.
func startTCPOnlyDNS(t *testing.T, ip string) string {
	t.Helper()
	for i := 0; i < 20; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		pc, err := net.ListenPacket("udp", ln.Addr().String())
		if err != nil { // порт занят по UDP — пробуем другой
			ln.Close()
			continue
		}
		t.Cleanup(func() { pc.Close() })
		go func() {
			buf := make([]byte, 512)
			for {
				if _, _, err := pc.ReadFrom(buf); err != nil {
					return
				}
			}
		}()
		mux := dns.NewServeMux()
		mux.HandleFunc(".", func(w dns.ResponseWriter, q *dns.Msg) {
			resp := new(dns.Msg)
			resp.SetReply(q)
			rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN A " + ip)
			resp.Answer = append(resp.Answer, rr)
			w.WriteMsg(resp)
		})
		srv := &dns.Server{Listener: ln, Handler: mux}
		go srv.ActivateAndServe()
		t.Cleanup(func() { srv.Shutdown() })
		return ln.Addr().String()
	}
	t.Fatal("no free tcp+udp port")
	return ""
}

// fastTimers укорачивает тайминги bootstrap, чтобы тесты на молчание не ждали
// боевые секунды.
func fastTimers(t *testing.T) {
	t.Helper()
	oldTimeout, oldDelay := bootstrapTimeout, tcpFallbackDelay
	bootstrapTimeout, tcpFallbackDelay = 800*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { bootstrapTimeout, tcpFallbackDelay = oldTimeout, oldDelay })
}

func TestBootstrapLookupUsesConfiguredServer(t *testing.T) {
	addr := startTestDNS(t, "203.0.113.9")
	b := NewBootstrap([]string{addr})
	ips, err := b.LookupIPs(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if ip := ips[0]; !ip.Equal(net.ParseIP("203.0.113.9")) {
		t.Errorf("ip = %v, want 203.0.113.9", ip)
	}
}

func TestBootstrapFailsOverToNextServer(t *testing.T) {
	live := startTestDNS(t, "203.0.113.10")
	b := NewBootstrap([]string{"127.0.0.1:1", live})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := b.LookupIPs(ctx, "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if ip := ips[0]; !ip.Equal(net.ParseIP("203.0.113.10")) {
		t.Errorf("ip = %v, want 203.0.113.10", ip)
	}
}

func TestBootstrapErrorsWhenAllServersFail(t *testing.T) {
	b := NewBootstrap([]string{"127.0.0.1:1"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := b.LookupIPs(ctx, "dns.example.test"); err == nil {
		t.Error("want error when no bootstrap server answers")
	}
}

// Молчащий сервер первым в списке не должен съедать время остальных: серверы
// опрашиваются параллельно.
func TestBootstrapSilentServerDoesNotDelayOthers(t *testing.T) {
	fastTimers(t)
	live := startTestDNS(t, "203.0.113.11")
	b := NewBootstrap([]string{startSilentUDP(t), startSilentUDP(t), live})
	start := time.Now()
	ips, err := b.LookupIPs(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if ip := ips[0]; !ip.Equal(net.ParseIP("203.0.113.11")) {
		t.Errorf("ips = %v, want 203.0.113.11", ips)
	}
	if d := time.Since(start); d > bootstrapTimeout/2 {
		t.Errorf("lookup took %v: silent servers were waited out one by one", d)
	}
}

// DPI часто режет только UDP: когда UDP молчит, тот же сервер спрашивается по TCP.
func TestBootstrapFallsBackToTCP(t *testing.T) {
	fastTimers(t)
	b := NewBootstrap([]string{startTCPOnlyDNS(t, "203.0.113.12")})
	ips, err := b.LookupIPs(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if ip := ips[0]; !ip.Equal(net.ParseIP("203.0.113.12")) {
		t.Errorf("ip = %v, want 203.0.113.12", ip)
	}
}

// Ошибка называет все серверы, а не только последний, и не выдаёт за причину
// мгновенный "dial ... i/o timeout", который получается, когда время уже
// кончилось. Именно так v0.3.1 сваливал вину на 1.1.1.1.
func TestBootstrapErrorNamesEveryServer(t *testing.T) {
	fastTimers(t)
	a, b2 := startSilentUDP(t), startSilentUDP(t)
	b := NewBootstrap([]string{a, b2})
	_, err := b.LookupIPs(context.Background(), "dns.example.test")
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, ErrBootstrapNoAnswer) {
		t.Errorf("err = %v, want ErrBootstrapNoAnswer", err)
	}
	msg := err.Error()
	for _, s := range []string{a, b2, "dns.example.test"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error %q does not mention %s", msg, s)
		}
	}
	if strings.Contains(msg, "dial") {
		t.Errorf("error %q blames dial: that is the budget running out, not the cause", msg)
	}
}

// startCountingDNS — как startTestDNS, но считает запросы и умеет замолчать.
func startCountingDNS(t *testing.T, ip string, ttl string) (addr string, queries *atomic.Int64, mute func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	var muted atomic.Bool
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, q *dns.Msg) {
		n.Add(1)
		if muted.Load() {
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		rr, _ := dns.NewRR(q.Question[0].Name + " " + ttl + " IN A " + ip)
		resp.Answer = append(resp.Answer, rr)
		w.WriteMsg(resp)
	})
	srv := &dns.Server{PacketConn: pc, Handler: mux}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	return pc.LocalAddr().String(), &n, func() { muted.Store(true) }
}

func TestBootstrapCachesWithinTTL(t *testing.T) {
	addr, queries, _ := startCountingDNS(t, "203.0.113.13", "300")
	b := NewBootstrap([]string{addr})
	for i := 0; i < 3; i++ {
		if _, err := b.LookupIPs(context.Background(), "dns.example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if q := queries.Load(); q != 1 {
		t.Errorf("queries = %d, want 1 (answer cached for its TTL)", q)
	}
}

// Перестал отвечать bootstrap — переподключение к апстриму идёт на последний
// известный адрес. Подмена тут не страшна: сертификат проверяется по имени.
func TestBootstrapFallsBackToLastKnownIP(t *testing.T) {
	fastTimers(t)
	addr, _, mute := startCountingDNS(t, "203.0.113.14", "300")
	b := NewBootstrap([]string{addr})
	if _, err := b.LookupIPs(context.Background(), "dns.example.test"); err != nil {
		t.Fatal(err)
	}
	mute()
	b.expireCache() // TTL истёк — надо спрашивать заново
	ips, err := b.LookupIPs(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatalf("want last known IP, got error %v", err)
	}
	if ip := ips[0]; !ip.Equal(net.ParseIP("203.0.113.14")) {
		t.Errorf("ip = %v, want the last known 203.0.113.14", ip)
	}
}

// Апстрим может жить на нескольких адресах, и часть из них бывает мертва:
// bootstrap отдаёт все, дозвон сам выбирает живой.
func TestBootstrapReturnsAllAddresses(t *testing.T) {
	b := NewBootstrap([]string{startTestDNS(t, "203.0.113.21,203.0.113.22,203.0.113.23")})
	ips, err := b.LookupIPs(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 3 {
		t.Fatalf("ips = %v, want all three", ips)
	}
}

// doqd test показывает, кто и как ответил — для этого Resolve отдаёт источник.
func TestBootstrapResolveReportsSource(t *testing.T) {
	fastTimers(t)
	addr := startTCPOnlyDNS(t, "203.0.113.24")
	b := NewBootstrap([]string{addr})
	r, err := b.Resolve(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if r.Server != addr || r.Proto != "tcp" || r.Cached {
		t.Errorf("resolution = %+v, want %s over tcp, not cached", r, addr)
	}
	r, _ = b.Resolve(context.Background(), "dns.example.test")
	if !r.Cached {
		t.Errorf("second resolution = %+v, want cached", r)
	}
}

// Адреса просрочены, а bootstrap молчит (DPI): старые адреса отдаются сразу,
// а не после 3 с ожидания на каждом переподключении. Обновление идёт в фоне.
func TestBootstrapServesExpiredAddressesWithoutWaiting(t *testing.T) {
	fastTimers(t)
	addr, queries, mute := startCountingDNS(t, "203.0.113.15", "300")
	b := NewBootstrap([]string{addr})
	if _, err := b.LookupIPs(context.Background(), "dns.example.test"); err != nil {
		t.Fatal(err)
	}
	mute()
	b.expireCache()
	start := time.Now()
	r, err := b.Resolve(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("expired addresses returned after %v, want at once", d)
	}
	if !r.Stale || !r.IPs[0].Equal(net.ParseIP("203.0.113.15")) {
		t.Errorf("resolution = %+v, want the stale address", r)
	}
	deadline := time.Now().Add(time.Second)
	for queries.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if queries.Load() < 2 {
		t.Error("no background refresh after serving expired addresses")
	}
}

// Гарантия против петли: список по умолчанию — только IP-литералы, иначе
// резолв имени апстрима снова уйдёт в системный резолвер.
func TestDefaultBootstrapServersAreIPLiterals(t *testing.T) {
	if len(DefaultBootstrapServers) == 0 {
		t.Fatal("DefaultBootstrapServers is empty")
	}
	for _, s := range DefaultBootstrapServers {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			t.Errorf("%q: %v", s, err)
			continue
		}
		if net.ParseIP(host) == nil {
			t.Errorf("%q: host must be an IP literal", s)
		}
		if port == "" {
			t.Errorf("%q: no port", s)
		}
	}
}
