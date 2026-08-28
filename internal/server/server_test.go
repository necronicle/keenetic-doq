package server

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeResolver struct{ err error }

func (f *fakeResolver) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.err != nil {
		return nil, f.err
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	rr, _ := dns.NewRR(req.Question[0].Name + " 300 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp, nil
}

func startServer(t *testing.T, r Resolver) *Server {
	t.Helper()
	s := New("127.0.0.1:0", r)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	return s
}

func TestUDPAndTCP(t *testing.T) {
	s := startServer(t, &fakeResolver{})
	for _, netw := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: netw, Timeout: 2 * time.Second}
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		resp, _, err := c.Exchange(q, s.Addr())
		if err != nil {
			t.Fatalf("%s: %v", netw, err)
		}
		if len(resp.Answer) != 1 {
			t.Errorf("%s: Answer = %v", netw, resp.Answer)
		}
	}
}

func TestServfailOnResolverError(t *testing.T) {
	s := startServer(t, &fakeResolver{err: errors.New("boom")})
	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	resp, _, err := c.Exchange(q, s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Errorf("Rcode = %d, want SERVFAIL", resp.Rcode)
	}
}

// Загрузка роутера: /opt монтируется раньше, чем интерфейс получает адрес,
// и bind падает с EADDRNOTAVAIL. Демон обязан дождаться адреса, а не выйти:
// rc.func запускает init-скрипт один раз и повторять не будет.
func TestStartWaitRetriesUntilAddressAppears(t *testing.T) {
	s := New("127.0.0.1:0", &fakeResolver{})
	s.retryMin, s.retryMax = time.Millisecond, time.Millisecond
	var attempts int
	s.listenPacket = func(network, addr string) (net.PacketConn, error) {
		attempts++
		if attempts < 3 {
			return nil, &net.OpError{Op: "listen", Net: network, Err: syscall.EADDRNOTAVAIL}
		}
		return net.ListenPacket(network, addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.StartWait(ctx); err != nil {
		t.Fatalf("StartWait: %v", err)
	}
	t.Cleanup(s.Shutdown)
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, _, err := c.Exchange(q, s.Addr()); err != nil {
		t.Fatalf("сервер не отвечает после ожидания адреса: %v", err)
	}
}

// Занятый порт — не «адрес ещё не поднялся»: ждать нечего, и молча крутиться
// вместо выхода нельзя, иначе rc.func отрапортует «alive» о немом демоне.
func TestStartWaitFailsFastOnOtherErrors(t *testing.T) {
	busy := New("127.0.0.1:0", &fakeResolver{})
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(busy.Shutdown)

	s := New(busy.Addr(), &fakeResolver{})
	s.retryMin, s.retryMax = time.Millisecond, time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.StartWait(ctx)
	if err == nil {
		t.Fatal("ожидалась ошибка на занятом адресе")
	}
	if ctx.Err() != nil {
		t.Error("StartWait крутился до истечения контекста вместо быстрого выхода")
	}
}

// Пока адреса нет, демон должен уходить по SIGTERM, а не висеть в ретраях.
func TestStartWaitStopsOnContextCancel(t *testing.T) {
	s := New("127.0.0.1:0", &fakeResolver{})
	s.retryMin, s.retryMax = 10*time.Millisecond, 10*time.Millisecond
	s.listenPacket = func(network, addr string) (net.PacketConn, error) {
		return nil, &net.OpError{Op: "listen", Net: network, Err: syscall.EADDRNOTAVAIL}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	done := make(chan error, 1)
	go func() { done <- s.StartWait(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("StartWait вернул nil, хотя адрес так и не появился")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StartWait не завершился после отмены контекста")
	}
}
