package upstream

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// startTestDNS поднимает обычный UDP DNS-сервер, отвечающий одним A.
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
		rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN A " + ip)
		resp.Answer = append(resp.Answer, rr)
		w.WriteMsg(resp)
	})
	srv := &dns.Server{PacketConn: pc, Handler: mux}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	return pc.LocalAddr().String()
}

func TestBootstrapLookupUsesConfiguredServer(t *testing.T) {
	addr := startTestDNS(t, "203.0.113.9")
	b := NewBootstrap([]string{addr})
	ip, err := b.LookupIP(context.Background(), "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(net.ParseIP("203.0.113.9")) {
		t.Errorf("ip = %v, want 203.0.113.9", ip)
	}
}

func TestBootstrapFailsOverToNextServer(t *testing.T) {
	live := startTestDNS(t, "203.0.113.10")
	b := NewBootstrap([]string{"127.0.0.1:1", live})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ip, err := b.LookupIP(ctx, "dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(net.ParseIP("203.0.113.10")) {
		t.Errorf("ip = %v, want 203.0.113.10", ip)
	}
}

func TestBootstrapErrorsWhenAllServersFail(t *testing.T) {
	b := NewBootstrap([]string{"127.0.0.1:1"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := b.LookupIP(ctx, "dns.example.test"); err == nil {
		t.Error("want error when no bootstrap server answers")
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
