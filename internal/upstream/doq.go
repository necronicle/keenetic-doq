// Package upstream — DoQ-клиенты (RFC 9250) и выбор апстрима.
package upstream

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

const defaultDoQPort = "853"

// Коды ошибок DoQ (RFC 9250 §8.4).
const doqRequestCancelled = 0x3

var (
	// dialTimeout — на весь дозвон: bootstrap плюс рукопожатия со всеми
	// адресами. Дозвон идёт в своей горутине и не зависит от запроса, который
	// его начал: если тот сдался раньше, соединение достанется следующему.
	dialTimeout = 8 * time.Second
	// dialStagger — через сколько, не дождавшись рукопожатия с одним адресом,
	// начать параллельно следующий. У сервера может быть несколько адресов, и
	// часть из них бывает мертва.
	dialStagger = time.Second
	// dialFailHold — сколько после неудачного дозвона сразу отвечать ошибкой,
	// не пытаясь снова. Иначе каждый ждущий запрос заново стучится в мёртвый
	// сервер, и все они по очереди сжигают своё время.
	dialFailHold = 5 * time.Second
	// livenessTimeout — сколько ждать ответа на проверку живости соединения,
	// по которому запрос не дождался ответа.
	livenessTimeout = 2 * time.Second
)

// maxLivenessWait — потолок ожидания проверки живости на очень медленном канале.
const maxLivenessWait = 8 * time.Second

// livenessWait — сколько ждать проверку живости: не меньше base и не меньше
// 4× обычного времени обмена на этом соединении, чтобы на медленном канале
// пара потерянных пакетов не закрыла живое соединение.
func livenessWait(base, rtt time.Duration) time.Duration {
	return min(max(base, 4*rtt), maxLivenessWait)
}

type DoQ struct {
	addr      string // host:port — имя для логов и ошибок
	host      string // SNI
	port      string
	TLSConfig *tls.Config

	bootstrap Bootstrap
	// dialAddr подменяется в тестах; в бою — quic.DialAddr.
	dialAddr func(ctx context.Context, addr string, tlsConf *tls.Config, conf *quic.Config) (quic.Connection, error)
	// OnDialAttempt, если задан, узнаёт исход рукопожатия с каждым адресом —
	// для `doqd test`.
	OnDialAttempt func(addr string, took time.Duration, err error)

	// Тайминги копируются при создании: горутины дозвона переживают вызовы.
	dialTimeout, dialStagger, dialFailHold, livenessTimeout time.Duration

	closeCtx    context.Context
	cancelClose context.CancelFunc

	mu           sync.Mutex
	conn         *liveConn
	dialing      *dialCall
	dialErr      error
	dialErrUntil time.Time
	preferred    string // адрес, с которым в последний раз удалось соединиться
	dialCount    atomic.Int64
}

// liveConn — соединение плюс время последнего удачного обмена и идущая
// проверка живости.
type liveConn struct {
	quic.Connection
	lastOK atomic.Int64 // UnixNano
	rtt    atomic.Int64 // EWMA времени обмена, нс
	check  atomic.Pointer[livenessCheck]
}

type livenessCheck struct{ done chan struct{} }

func (c *liveConn) markOK() { c.lastOK.Store(time.Now().UnixNano()) }

func (c *liveConn) observe(rtt time.Duration) {
	if old := c.rtt.Load(); old == 0 {
		c.rtt.Store(int64(rtt))
	} else {
		c.rtt.Store((old*7 + int64(rtt)) / 8)
	}
}

type dialCall struct {
	done chan struct{}
	conn *liveConn
	err  error
}

func NewDoQ(rawURL string) (*DoQ, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("upstream %q: %w", rawURL, err)
	}
	if u.Scheme != "quic" {
		return nil, fmt.Errorf("upstream %q: scheme must be quic://", rawURL)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return nil, fmt.Errorf("upstream %q: no host", rawURL)
	}
	if port == "" {
		port = defaultDoQPort
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DoQ{
		addr: net.JoinHostPort(host, port),
		host: host,
		port: port,
		// Кеш сессий даёт TLS-возобновление: при переподключении сервер не
		// шлёт цепочку сертификатов, и рукопожатие короче. 0-RTT не включаем:
		// с ним дозвон «удаётся» до первого ответа сервера, и мёртвый адрес
		// нельзя отличить от живого.
		TLSConfig: &tls.Config{
			ServerName:         host,
			NextProtos:         []string{"doq"},
			ClientSessionCache: tls.NewLRUClientSessionCache(4),
		},
		bootstrap:       NewBootstrap(nil),
		dialAddr:        quic.DialAddr,
		dialTimeout:     dialTimeout,
		dialStagger:     dialStagger,
		dialFailHold:    dialFailHold,
		livenessTimeout: livenessTimeout,
		closeCtx:        ctx,
		cancelClose:     cancel,
	}, nil
}

// SetBootstrap задаёт резолвер имени апстрима.
func (u *DoQ) SetBootstrap(b Bootstrap) { u.bootstrap = b }

func (u *DoQ) Address() string { return u.addr }

// dialTargets всегда возвращает IP:порт, последний удачный адрес — первым.
// Передать дозвону имя значит отдать резолв системному резолверу — на Keenetic
// это петля doqd → ndnproxy → doqd.
func (u *DoQ) dialTargets(ctx context.Context) ([]string, error) {
	if net.ParseIP(u.host) != nil {
		return []string{u.addr}, nil
	}
	ips, err := u.bootstrap.LookupIPs(ctx, u.host)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	pref := u.preferred
	u.mu.Unlock()
	var targets []string
	for _, ip := range ips {
		t := net.JoinHostPort(ip.String(), u.port)
		if t == pref {
			targets = append([]string{t}, targets...)
		} else {
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// getConn отдаёт живое соединение или ждёт общего дозвона. fresh — соединение
// только что установлено, повторять запрос на новом смысла нет.
func (u *DoQ) getConn(ctx context.Context) (conn *liveConn, fresh bool, err error) {
	u.mu.Lock()
	if u.conn != nil && u.conn.Context().Err() == nil {
		c := u.conn
		u.mu.Unlock()
		// Соединение под подозрением — дождаться вердикта: если оно мертво,
		// запрос на нём провисел бы до своего таймаута.
		if chk := c.check.Load(); chk != nil {
			select {
			case <-chk.done:
				return u.getConn(ctx)
			case <-ctx.Done():
				return nil, false, fmt.Errorf("dial %s: %w", u.addr, ctx.Err())
			}
		}
		return c, false, nil
	}
	u.conn = nil
	if u.dialing == nil && time.Now().Before(u.dialErrUntil) {
		err := u.dialErr
		u.mu.Unlock()
		return nil, false, err
	}
	call := u.dialing
	if call == nil {
		call = &dialCall{done: make(chan struct{})}
		u.dialing = call
		go u.dial(call)
	}
	u.mu.Unlock()
	select {
	case <-call.done:
		return call.conn, true, call.err
	case <-ctx.Done():
		return nil, false, fmt.Errorf("dial %s: %w", u.addr, ctx.Err())
	}
}

func (u *DoQ) dial(call *dialCall) {
	ctx, cancel := context.WithTimeout(u.closeCtx, u.dialTimeout)
	defer cancel()
	conn, winner, err := u.connect(ctx)
	u.mu.Lock()
	u.dialing = nil
	switch {
	case err != nil:
		u.dialErr, u.dialErrUntil = err, time.Now().Add(u.dialFailHold)
	case u.closeCtx.Err() != nil: // Close успел раньше
		conn.CloseWithError(0, "")
		conn, err = nil, fmt.Errorf("dial %s: upstream closed", u.addr)
	default:
		u.conn, u.preferred = conn, winner
		u.dialErr, u.dialErrUntil = nil, time.Time{}
		u.dialCount.Add(1)
	}
	u.mu.Unlock()
	call.conn, call.err = conn, err
	close(call.done)
}

// ErrOvertaken — адрес не ответил, пока другой успел: для отчёта doqd test.
var ErrOvertaken = errors.New("no answer yet when another address answered")

type dialResult struct {
	addr string
	conn quic.Connection
	took time.Duration
	err  error
}

// connect дозванивается до всех адресов апстрима лесенкой: следующий адрес
// стартует, если предыдущий не ответил за dialStagger или уже отказал.
// Побеждает первое завершённое рукопожатие.
func (u *DoQ) connect(ctx context.Context) (*liveConn, string, error) {
	targets, err := u.dialTargets(ctx)
	if err != nil {
		return nil, "", err
	}
	conf := &quic.Config{
		MaxIdleTimeout:  90 * time.Second,
		KeepAlivePeriod: 20 * time.Second,
	}
	stagger := u.dialStagger
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan dialResult, len(targets))
	started := map[string]time.Time{} // ещё не завершённые рукопожатия
	launch := func(addr string) {
		started[addr] = time.Now()
		go func() {
			start := time.Now()
			c, err := u.dialAddr(ctx, addr, u.TLSConfig, conf)
			out <- dialResult{addr: addr, conn: c, took: time.Since(start), err: err}
		}()
	}
	if len(targets) == 0 {
		return nil, "", fmt.Errorf("dial %s: no addresses", u.addr)
	}
	launch(targets[0])
	next, pending := 1, 1
	timer := time.NewTimer(stagger)
	defer timer.Stop()
	var failed []dialResult
	for {
		var tick <-chan time.Time
		if next < len(targets) {
			tick = timer.C
		}
		select {
		case r := <-out:
			pending--
			delete(started, r.addr)
			if u.OnDialAttempt != nil {
				u.OnDialAttempt(r.addr, r.took, r.err)
			}
			if r.err == nil {
				if u.OnDialAttempt != nil {
					for addr, at := range started {
						u.OnDialAttempt(addr, time.Since(at), ErrOvertaken)
					}
				}
				// Проигравшие рукопожатия отменятся по cancel; те, что успеют
				// завершиться, закроет drainDials.
				go drainDials(out, pending)
				return &liveConn{Connection: r.conn}, r.addr, nil
			}
			failed = append(failed, r)
			if next < len(targets) { // отказал — следующий адрес без ожидания
				launch(targets[next])
				next++
				pending++
				timer.Reset(stagger)
			} else if pending == 0 {
				return nil, "", dialError(u.addr, failed)
			}
		case <-tick:
			launch(targets[next])
			next++
			pending++
			timer.Reset(stagger)
		}
	}
}

func drainDials(out <-chan dialResult, n int) {
	for ; n > 0; n-- {
		if r := <-out; r.err == nil {
			r.conn.CloseWithError(0, "")
		}
	}
}

func dialError(addr string, failed []dialResult) error {
	if len(failed) == 1 {
		return fmt.Errorf("dial %s: %w", addr, failed[0].err)
	}
	parts := make([]string, 0, len(failed))
	for _, r := range failed {
		host, _, _ := net.SplitHostPort(r.addr)
		parts = append(parts, host+": "+shortReason(r.err))
	}
	return fmt.Errorf("dial %s: no address answered (%s)", addr, strings.Join(parts, "; "))
}

func (u *DoQ) dropConn(conn *liveConn) {
	u.mu.Lock()
	if u.conn == conn {
		u.conn = nil
	}
	u.mu.Unlock()
	conn.CloseWithError(0, "")
}

// suspect проверяет соединение, по которому запрос не дождался ответа: был
// ли это медленный ответ (у рекурсора апстрима бывает и 5 с) или канал
// умер — так выглядит соединение после смены адреса WAN, которое QUIC сам
// похоронит лишь по idle-таймауту. Проверка — короткий запрос ". NS" по тому
// же соединению; не ответил и за это время ничего не прошло — соединение
// закрывается. Сразу закрывать нельзя: это оборвало бы все параллельные
// запросы на живом соединении из-за одного медленного.
func (u *DoQ) suspect(conn *liveConn, since time.Time) {
	if conn.lastOK.Load() >= since.UnixNano() {
		return // после начала запроса по соединению что-то прошло — живо
	}
	chk := &livenessCheck{done: make(chan struct{})}
	if !conn.check.CompareAndSwap(nil, chk) {
		return // уже проверяется
	}
	go func() {
		defer func() {
			conn.check.Store(nil)
			close(chk.done)
		}()
		wait := livenessWait(u.livenessTimeout, time.Duration(conn.rtt.Load()))
		ctx, cancel := context.WithTimeout(u.closeCtx, wait)
		defer cancel()
		probe := new(dns.Msg)
		probe.SetQuestion(".", dns.TypeNS)
		probe.Id = 0
		payload, _ := probe.Pack()
		start := time.Now()
		if _, err := exchangeOnConn(ctx, conn, payload); err == nil {
			conn.markOK()
			return
		}
		if conn.lastOK.Load() < start.UnixNano() {
			u.dropConn(conn)
		}
	}()
}

func exchangeOnConn(ctx context.Context, conn quic.Connection, payload []byte) (*dns.Msg, error) {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	// Дедлайн или отмена запроса прерывают и чтение из потока: иначе
	// отменённый запрос висел бы до таймаута.
	stop := context.AfterFunc(ctx, func() {
		stream.CancelRead(doqRequestCancelled)
		stream.CancelWrite(doqRequestCancelled)
	})
	defer stop()
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(payload)))
	if _, err := stream.Write(append(lb[:], payload...)); err != nil {
		return nil, ctxErr(ctx, err)
	}
	stream.Close() // FIN на запись — сигнал "запрос целиком отправлен"
	if _, err := io.ReadFull(stream, lb[:]); err != nil {
		return nil, ctxErr(ctx, err)
	}
	buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(stream, buf); err != nil {
		return nil, ctxErr(ctx, err)
	}
	// Ответ прочитан целиком. FIN сервера может прийти отдельным кадром, а
	// quic-go освобождает поток, только когда FIN прочитан или чтение
	// отменено: без отмены потоки копились бы до закрытия соединения.
	stream.CancelRead(0)
	resp := new(dns.Msg)
	if err := resp.Unpack(buf); err != nil {
		return nil, err
	}
	return resp, nil
}

// ctxErr: поток, сброшенный нашей же отменой, — это таймаут или отмена
// запроса, а не ошибка сервера.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return err
}

func (u *DoQ) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	resp, _, err := u.ExchangeTimed(ctx, m)
	return resp, err
}

// ExchangeTimed — как Exchange, плюс время самого обмена без дозвона: по нему
// выбор апстрима судит о скорости сервера, а doqd test показывает этапы.
func (u *DoQ) ExchangeTimed(ctx context.Context, m *dns.Msg) (*dns.Msg, time.Duration, error) {
	wire := m.Copy()
	wire.Id = 0 // RFC 9250 §4.2.1
	payload, err := wire.Pack()
	if err != nil {
		return nil, 0, err
	}
	for attempt := 0; ; attempt++ {
		// Не нашёлся адрес или не удался дозвон — повторять нечего.
		conn, fresh, err := u.getConn(ctx)
		if err != nil {
			return nil, 0, err
		}
		started := time.Now()
		resp, err := exchangeOnConn(ctx, conn, payload)
		if err == nil {
			rtt := time.Since(started)
			conn.markOK()
			conn.observe(rtt)
			resp.Id = m.Id
			return resp, rtt, nil
		}
		switch {
		case conn.Context().Err() != nil: // соединение закрыто — точно мертво
			u.dropConn(conn)
			// Повтор — только если умерло переиспользуемое соединение.
			if !fresh && attempt == 0 && ctx.Err() == nil {
				continue
			}
		case ctx.Err() != nil: // не дождались: медленный ответ или мёртвый канал
			u.suspect(conn, started)
		}
		// Сброс потока сервером — дело одного запроса, соединение живо.
		return nil, 0, err
	}
}

func (u *DoQ) Close() error {
	u.cancelClose()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil {
		u.conn.CloseWithError(0, "")
		u.conn = nil
	}
	return nil
}
