package upstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
	"sync"
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
