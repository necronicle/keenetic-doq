// Package upstream — DoQ-клиенты (RFC 9250) и выбор апстрима.
package upstream

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

const defaultDoQPort = "853"

type DoQ struct {
	addr      string // host:port для dial
	host      string // SNI
	port      string
	TLSConfig *tls.Config

	bootstrap Bootstrap
	// dialAddr подменяется в тестах; в бою — quic.DialAddr.
	dialAddr func(ctx context.Context, addr string, tlsConf *tls.Config, conf *quic.Config) (quic.Connection, error)

	mu        sync.Mutex
	conn      quic.Connection
	dialCount atomic.Int64
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
	return &DoQ{
		addr:      net.JoinHostPort(host, port),
		host:      host,
		port:      port,
		TLSConfig: &tls.Config{ServerName: host, NextProtos: []string{"doq"}},
		bootstrap: NewBootstrap(nil),
		dialAddr:  quic.DialAddr,
	}, nil
}

// SetBootstrap задаёт резолвер имени апстрима.
func (u *DoQ) SetBootstrap(b Bootstrap) { u.bootstrap = b }

func (u *DoQ) Address() string { return u.addr }

// dialTarget всегда возвращает IP:порт. Передать сюда имя значит отдать резолв
// системному резолверу — на Keenetic это петля doqd → ndnproxy → doqd.
func (u *DoQ) dialTarget(ctx context.Context) (string, error) {
	if net.ParseIP(u.host) != nil {
		return u.addr, nil
	}
	ip, err := u.bootstrap.LookupIP(ctx, u.host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), u.port), nil
}

func (u *DoQ) getConn(ctx context.Context) (quic.Connection, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil && u.conn.Context().Err() == nil {
		return u.conn, nil
	}
	u.conn = nil
	conf := &quic.Config{
		MaxIdleTimeout:  90 * time.Second,
		KeepAlivePeriod: 20 * time.Second,
	}
	target, err := u.dialTarget(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := u.dialAddr(ctx, target, u.TLSConfig, conf)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", u.addr, err)
	}
	u.dialCount.Add(1)
	u.conn = conn
	return conn, nil
}

func (u *DoQ) dropConn(conn quic.Connection) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn == conn {
		u.conn = nil
	}
	conn.CloseWithError(0, "")
}

func exchangeOnConn(ctx context.Context, conn quic.Connection, payload []byte) (*dns.Msg, error) {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		stream.SetDeadline(dl)
	}
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(payload)))
	if _, err := stream.Write(lb[:]); err != nil {
		return nil, err
	}
	if _, err := stream.Write(payload); err != nil {
		return nil, err
	}
	stream.Close() // FIN на запись — сигнал "запрос целиком отправлен"
	if _, err := io.ReadFull(stream, lb[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(stream, buf); err != nil {
		return nil, err
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(buf); err != nil {
		return nil, err
	}
	return resp, nil
}

func (u *DoQ) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	wire := m.Copy()
	wire.Id = 0 // RFC 9250 §4.2.1
	payload, err := wire.Pack()
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := u.getConn(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := exchangeOnConn(ctx, conn, payload)
		if err != nil {
			u.dropConn(conn)
			lastErr = err
			continue
		}
		resp.Id = m.Id
		return resp, nil
	}
	return nil, lastErr
}

func (u *DoQ) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil {
		u.conn.CloseWithError(0, "")
		u.conn = nil
	}
	return nil
}
