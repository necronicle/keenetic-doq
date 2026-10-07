package upstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

// startTestDoQServer поднимает минимальный DoQ-сервер на 127.0.0.1:0.
// Возвращает адрес и функцию-снимок DNS ID, увиденных на проводе.
func startTestDoQServer(t *testing.T, handler func(q *dns.Msg) *dns.Msg) (string, func() []uint16) {
	t.Helper()
	pkey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &pkey.PublicKey, pkey)
	if err != nil {
		t.Fatal(err)
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: pkey}},
		NextProtos:   []string{"doq"},
	}
	ln, err := quic.ListenAddr("127.0.0.1:0", tlsConf, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var ids []uint16
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func(conn quic.Connection) {
				for {
					stream, err := conn.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func(s quic.Stream) {
						defer s.Close()
						var lb [2]byte
						if _, err := io.ReadFull(s, lb[:]); err != nil {
							return
						}
						buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
						if _, err := io.ReadFull(s, buf); err != nil {
							return
						}
						var q dns.Msg
						if q.Unpack(buf) != nil {
							return
						}
						mu.Lock()
						ids = append(ids, q.Id)
						mu.Unlock()
						resp := handler(&q)
						if resp == nil { // nil — сбросить поток, как geohide с кодом 5
							s.CancelRead(5)
							s.CancelWrite(5)
							return
						}
						out, _ := resp.Pack()
						var ob [2]byte
						binary.BigEndian.PutUint16(ob[:], uint16(len(out)))
						s.Write(ob[:])
						s.Write(out)
					}(stream)
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() []uint16 {
		mu.Lock()
		defer mu.Unlock()
		return append([]uint16(nil), ids...)
	}
}

func testHandler(q *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(q)
	rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp
}

func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"doq"}}
}

func TestExchange(t *testing.T) {
	addr, gotIDs := startTestDoQServer(t, testHandler)
	u, err := NewDoQ("quic://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	u.TLSConfig = insecureTLS()

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Id = 4242
	resp, err := u.Exchange(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Id != 4242 {
		t.Errorf("resp.Id = %d, want 4242 (restored)", resp.Id)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("Answer = %v", resp.Answer)
	}
	// RFC 9250 §4.2.1: на проводе ID обязан быть 0
	for _, id := range gotIDs() {
		if id != 0 {
			t.Errorf("wire ID = %d, want 0", id)
		}
	}
}

func TestConnectionReuse(t *testing.T) {
	addr, _ := startTestDoQServer(t, testHandler)
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	for i := 0; i < 3; i++ {
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		if _, err := u.Exchange(context.Background(), q); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
	}
	if u.dialCount.Load() != 1 {
		t.Errorf("dials = %d, want 1 (connection reuse)", u.dialCount.Load())
	}
}

func TestExchangeErrorWhenServerDown(t *testing.T) {
	u, _ := NewDoQ("quic://127.0.0.1:1") // никто не слушает
	defer u.Close()
	u.TLSConfig = insecureTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := u.Exchange(ctx, q); err == nil {
		t.Error("want error")
	}
}

func TestNewDoQParse(t *testing.T) {
	u, err := NewDoQ("quic://dns.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.Address() != "dns.example.com:853" {
		t.Errorf("Address = %q, want default port 853", u.Address())
	}
	if _, err := NewDoQ("tls://dns.example.com"); err == nil {
		t.Error("non-quic scheme must fail")
	}
}

type fakeBootstrap struct {
	ip    net.IP
	ips   []net.IP
	err   error
	calls int
}

func (f *fakeBootstrap) LookupIPs(ctx context.Context, host string) ([]net.IP, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.ips != nil {
		return f.ips, nil
	}
	return []net.IP{f.ip}, nil
}

// Повтор в Exchange — для умершего переиспользуемого соединения. Если адрес не
// нашёлся, повторять нечего: второй заход лишь перетирал настоящую ошибку
// мгновенной "dial udp ...: i/o timeout" на исчерпанном контексте.
func TestExchangeDoesNotRetryFailedBootstrap(t *testing.T) {
	u, err := NewDoQ("quic://dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	boot := &fakeBootstrap{err: errors.New("the real cause")}
	u.SetBootstrap(boot)
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	_, err = u.Exchange(context.Background(), q)
	if err == nil || !strings.Contains(err.Error(), "the real cause") {
		t.Errorf("err = %v, want the bootstrap error", err)
	}
	if boot.calls != 1 {
		t.Errorf("bootstrap calls = %d, want 1", boot.calls)
	}
}

// Ключевой инвариант: quic-дозвон получает IP-литерал, а не имя. Иначе адрес
// резолвит системный резолвер — на Keenetic это 127.0.0.1:53, тот самый
// ndnproxy, в списке серверов которого прописан сам doqd: запрос апстрима
// возвращается в doqd и DNS роутера встаёт колом.
func TestDialTargetIsBootstrapIPNotHostname(t *testing.T) {
	u, err := NewDoQ("quic://dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	boot := &fakeBootstrap{ip: net.ParseIP("192.0.2.7")}
	u.SetBootstrap(boot)
	var got string
	u.dialAddr = func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (quic.Connection, error) {
		got = addr
		return nil, errors.New("dial stopped by test")
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	u.Exchange(context.Background(), q)

	if got != "192.0.2.7:853" {
		t.Errorf("dial target = %q, want %q", got, "192.0.2.7:853")
	}
	if boot.calls == 0 {
		t.Error("bootstrap was not consulted")
	}
	if u.TLSConfig.ServerName != "dns.example.test" {
		t.Errorf("SNI = %q, want the hostname", u.TLSConfig.ServerName)
	}
}

func TestIPLiteralUpstreamNeedsNoBootstrap(t *testing.T) {
	u, err := NewDoQ("quic://192.0.2.8:853")
	if err != nil {
		t.Fatal(err)
	}
	boot := &fakeBootstrap{ip: net.ParseIP("203.0.113.1")}
	u.SetBootstrap(boot)
	var got string
	u.dialAddr = func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (quic.Connection, error) {
		got = addr
		return nil, errors.New("dial stopped by test")
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	u.Exchange(context.Background(), q)

	if got != "192.0.2.8:853" {
		t.Errorf("dial target = %q, want the literal address", got)
	}
	if boot.calls != 0 {
		t.Errorf("bootstrap calls = %d, want 0 for an IP upstream", boot.calls)
	}
}

// fastDial укорачивает тайминги дозвона для тестов.
func fastDial(t *testing.T) {
	t.Helper()
	oldT, oldS, oldH, oldL := dialTimeout, dialStagger, dialFailHold, livenessTimeout
	dialTimeout, dialStagger, dialFailHold, livenessTimeout = 3*time.Second, 100*time.Millisecond, time.Second, 300*time.Millisecond
	t.Cleanup(func() { dialTimeout, dialStagger, dialFailHold, livenessTimeout = oldT, oldS, oldH, oldL })
}

// remapDial направляет дозвон на тестовые адреса: dead — висит до отмены,
// всё остальное — на живой тестовый сервер.
func remapDial(u *DoQ, live string, dead map[string]bool, calls *atomic.Int64) {
	u.dialAddr = func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (quic.Connection, error) {
		if calls != nil {
			calls.Add(1)
		}
		if dead[addr] {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return quic.DialAddr(ctx, live, tc, qc)
	}
}

func newTestDoQ(t *testing.T, ips ...string) *DoQ {
	t.Helper()
	u, err := NewDoQ("quic://dns.example.test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	u.TLSConfig = insecureTLS()
	var list []net.IP
	for _, ip := range ips {
		list = append(list, net.ParseIP(ip))
	}
	u.SetBootstrap(&fakeBootstrap{ips: list})
	return u
}

func ask(u *DoQ, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	_, err := u.Exchange(ctx, q)
	return err
}

// Первый адрес апстрима мёртв — дозвон не ждёт его до конца, а лесенкой
// пробует следующий и запоминает победителя.
func TestDialRacesAddresses(t *testing.T) {
	fastDial(t)
	live, _ := startTestDoQServer(t, testHandler)
	u := newTestDoQ(t, "192.0.2.1", "192.0.2.2")
	remapDial(u, live, map[string]bool{"192.0.2.1:853": true}, nil)
	start := time.Now()
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v: dead address was waited out", d)
	}
	if u.preferred != "192.0.2.2:853" {
		t.Errorf("preferred = %q, want the address that answered", u.preferred)
	}
	targets, _ := u.dialTargets(context.Background())
	if targets[0] != "192.0.2.2:853" {
		t.Errorf("targets = %v, want the last good address first", targets)
	}
}

// doqd test должен видеть и адрес, который не успел ответить.
func TestDialReportsOvertakenAddress(t *testing.T) {
	fastDial(t)
	live, _ := startTestDoQServer(t, testHandler)
	u := newTestDoQ(t, "192.0.2.1", "192.0.2.2")
	remapDial(u, live, map[string]bool{"192.0.2.1:853": true}, nil)
	var mu sync.Mutex
	seen := map[string]error{}
	u.OnDialAttempt = func(addr string, took time.Duration, err error) {
		mu.Lock()
		seen[addr] = err
		mu.Unlock()
	}
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if err, ok := seen["192.0.2.1:853"]; !ok || !errors.Is(err, ErrOvertaken) {
		t.Errorf("dead address reported as %v (seen=%v), want ErrOvertaken", err, ok)
	}
	if err, ok := seen["192.0.2.2:853"]; !ok || err != nil {
		t.Errorf("live address reported as %v (seen=%v), want success", err, ok)
	}
}

// Пачка одновременных запросов ждёт одного общего дозвона.
func TestConcurrentQueriesShareOneDial(t *testing.T) {
	fastDial(t)
	live, _ := startTestDoQServer(t, testHandler)
	u := newTestDoQ(t, "192.0.2.3")
	var calls atomic.Int64
	remapDial(u, live, nil, &calls)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ask(u, "example.com.", 2*time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("dial attempts = %d, want 1", n)
	}
}

// После неудачного дозвона запросы какое-то время получают ошибку сразу, а не
// стучатся в мёртвый сервер каждый заново.
func TestFailedDialFailsFastForAWhile(t *testing.T) {
	fastDial(t)
	u := newTestDoQ(t, "192.0.2.4")
	var calls atomic.Int64
	u.dialAddr = func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (quic.Connection, error) {
		calls.Add(1)
		return nil, errors.New("handshake failed")
	}
	if err := ask(u, "example.com.", time.Second); err == nil {
		t.Fatal("want error")
	}
	start := time.Now()
	err := ask(u, "example.com.", time.Second)
	if err == nil || !strings.Contains(err.Error(), "handshake failed") {
		t.Errorf("err = %v, want the remembered dial error", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("second query took %v, want an immediate failure", d)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("dial attempts = %d, want 1", n)
	}
}

// Запрос, у которого кончилось время, сдаётся сам, а дозвон продолжается —
// и соединение достаётся следующему запросу.
func TestDialOutlivesImpatientQuery(t *testing.T) {
	fastDial(t)
	live, _ := startTestDoQServer(t, testHandler)
	u := newTestDoQ(t, "192.0.2.5")
	var calls atomic.Int64
	u.dialAddr = func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (quic.Connection, error) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		return quic.DialAddr(ctx, live, tc, qc)
	}
	start := time.Now()
	if err := ask(u, "example.com.", 100*time.Millisecond); err == nil {
		t.Fatal("want timeout")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("impatient query took %v", d)
	}
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("dial attempts = %d, want 1", n)
	}
}

// Сервер сбросил один поток — соединение живо, остальные запросы на нём не
// должны обрываться.
func TestStreamResetKeepsConnection(t *testing.T) {
	addr, _ := startTestDoQServer(t, func(q *dns.Msg) *dns.Msg {
		if q.Question[0].Name == "reset.test." {
			return nil
		}
		return testHandler(q)
	})
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := ask(u, "reset.test.", 2*time.Second); err == nil {
		t.Fatal("want error for a reset stream")
	}
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := u.dialCount.Load(); n != 1 {
		t.Errorf("dials = %d, want 1 (connection kept)", n)
	}
}

// Медленный запрос, пока другие проходят, — не повод рвать соединение.
func TestSlowQueryKeepsLiveConnection(t *testing.T) {
	addr, _ := startTestDoQServer(t, func(q *dns.Msg) *dns.Msg {
		if q.Question[0].Name == "slow.test." {
			time.Sleep(time.Second)
		}
		return testHandler(q)
	})
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- ask(u, "slow.test.", 400*time.Millisecond) }()
	time.Sleep(100 * time.Millisecond)
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("want timeout for the slow query")
	}
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := u.dialCount.Load(); n != 1 {
		t.Errorf("dials = %d, want 1 (connection kept)", n)
	}
}

// startRelay — UDP-посредник между клиентом и сервером, который умеет молча
// глотать пакеты в обе стороны: так выглядит канал после смены адреса WAN.
func startRelay(t *testing.T, target string) (addr string, silent *atomic.Bool) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	raddr, _ := net.ResolveUDPAddr("udp", target)
	silent = new(atomic.Bool)
	var mu sync.Mutex
	peers := map[string]*net.UDPConn{}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if silent.Load() {
				continue
			}
			mu.Lock()
			up, ok := peers[from.String()]
			if !ok {
				up, err = net.DialUDP("udp", nil, raddr)
				if err != nil {
					mu.Unlock()
					continue
				}
				peers[from.String()] = up
				t.Cleanup(func() { up.Close() })
				go func(up *net.UDPConn, from net.Addr) {
					b := make([]byte, 2048)
					for {
						n, err := up.Read(b)
						if err != nil {
							return
						}
						if !silent.Load() {
							pc.WriteTo(b[:n], from)
						}
					}
				}(up, from)
			}
			mu.Unlock()
			up.Write(buf[:n])
		}
	}()
	return pc.LocalAddr().String(), silent
}

// Канал замолчал целиком: запрос не дождался ответа, проверка живости тоже —
// соединение закрывается, и следующий запрос идёт по новому.
func TestSilentConnectionIsDropped(t *testing.T) {
	fastDial(t)
	server, _ := startTestDoQServer(t, testHandler)
	relay, silent := startRelay(t, server)
	u, _ := NewDoQ("quic://" + relay)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	silent.Store(true)
	if err := ask(u, "example.com.", 300*time.Millisecond); err == nil {
		t.Fatal("want timeout on a silent link")
	}
	time.Sleep(2 * livenessTimeout) // проверка живости тоже не дождалась ответа
	silent.Store(false)
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := u.dialCount.Load(); n != 2 {
		t.Errorf("dials = %d, want 2 (silent connection replaced)", n)
	}
}

// Медленный ответ на простаивающем соединении (других запросов нет) — не
// повод его рвать: проверка живости отвечает, соединение остаётся.
func TestSlowAnswerKeepsIdleConnection(t *testing.T) {
	fastDial(t)
	addr, _ := startTestDoQServer(t, func(q *dns.Msg) *dns.Msg {
		if q.Question[0].Name == "slow.test." {
			time.Sleep(time.Second)
		}
		return testHandler(q)
	})
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := ask(u, "slow.test.", 300*time.Millisecond); err == nil {
		t.Fatal("want timeout for the slow query")
	}
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := u.dialCount.Load(); n != 1 {
		t.Errorf("dials = %d, want 1 (live connection kept)", n)
	}
}

// Время, которое ExchangeTimed отдаёт выбору апстрима, — без дозвона.
func TestExchangeTimedExcludesDial(t *testing.T) {
	fastDial(t)
	live, _ := startTestDoQServer(t, testHandler)
	u := newTestDoQ(t, "192.0.2.6")
	u.dialAddr = func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (quic.Connection, error) {
		time.Sleep(300 * time.Millisecond)
		return quic.DialAddr(ctx, live, tc, qc)
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	_, rtt, err := u.ExchangeTimed(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if rtt >= 300*time.Millisecond {
		t.Errorf("rtt = %v includes the 300 ms dial", rtt)
	}
}

// Отменённый запрос (проигравший в гонке апстримов) не трогает соединение и
// не висит до дедлайна.
func TestCancelledQueryReturnsAndKeepsConnection(t *testing.T) {
	addr, _ := startTestDoQServer(t, func(q *dns.Msg) *dns.Msg {
		if q.Question[0].Name == "slow.test." {
			time.Sleep(time.Second)
		}
		return testHandler(q)
	})
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig = insecureTLS()
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	q := new(dns.Msg)
	q.SetQuestion("slow.test.", dns.TypeA)
	start := time.Now()
	if _, err := u.Exchange(ctx, q); err == nil {
		t.Fatal("want error for a cancelled query")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("cancelled query returned after %v", d)
	}
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := u.dialCount.Load(); n != 1 {
		t.Errorf("dials = %d, want 1", n)
	}
}

// Переподключение использует TLS-возобновление: короче рукопожатие.
func TestReconnectResumesTLSSession(t *testing.T) {
	addr, _ := startTestDoQServer(t, testHandler)
	u, _ := NewDoQ("quic://" + addr)
	defer u.Close()
	u.TLSConfig.InsecureSkipVerify = true
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // тикет сессии приходит после рукопожатия
	u.mu.Lock()
	old := u.conn
	u.mu.Unlock()
	u.dropConn(old)
	if err := ask(u, "example.com.", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	resumed := u.conn.ConnectionState().TLS.DidResume
	u.mu.Unlock()
	if !resumed {
		t.Error("second connection did not resume the TLS session")
	}
}

// На медленном канале проверка живости ждёт дольше: иначе пара потерянных
// пакетов закрыла бы живое соединение.
func TestLivenessWaitScalesWithRTT(t *testing.T) {
	if got := livenessWait(2*time.Second, 100*time.Millisecond); got != 2*time.Second {
		t.Errorf("fast link: %v, want the 2 s floor", got)
	}
	if got := livenessWait(2*time.Second, time.Second); got != 4*time.Second {
		t.Errorf("1 s exchanges: %v, want 4 s", got)
	}
	if got := livenessWait(2*time.Second, 10*time.Second); got != maxLivenessWait {
		t.Errorf("very slow link: %v, want the %v cap", got, maxLivenessWait)
	}
}
