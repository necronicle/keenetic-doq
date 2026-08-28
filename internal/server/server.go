// Package server — обычный DNS-слушатель (UDP+TCP) поверх Resolver.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type Resolver interface {
	Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}

const queryTimeout = 10 * time.Second

// Границы паузы между попытками занять адрес.
const (
	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
)

type Server struct {
	listen   string
	resolver Resolver
	addr     string
	udp      *dns.Server
	tcp      *dns.Server

	retryMin, retryMax time.Duration
	// listenPacket подменяется в тестах; в бою — net.ListenPacket.
	listenPacket func(network, address string) (net.PacketConn, error)
}

func New(listen string, r Resolver) *Server {
	return &Server{
		listen:       listen,
		resolver:     r,
		retryMin:     defaultRetryMin,
		retryMax:     defaultRetryMax,
		listenPacket: net.ListenPacket,
	}
}

func (s *Server) handle(w dns.ResponseWriter, req *dns.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	resp, err := s.resolver.Resolve(ctx, req)
	if err != nil {
		slog.Warn("resolve failed", "err", err)
		fail := new(dns.Msg)
		fail.SetRcode(req, dns.RcodeServerFailure)
		w.WriteMsg(fail)
		return
	}
	w.WriteMsg(resp)
}

func (s *Server) Start() error {
	pc, err := s.listenPacket("udp", s.listen)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		pc.Close()
		return err
	}
	s.addr = pc.LocalAddr().String()
	h := dns.HandlerFunc(s.handle)
	s.udp = &dns.Server{PacketConn: pc, Handler: h}
	s.tcp = &dns.Server{Listener: l, Handler: h}
	go s.udp.ActivateAndServe()
	go s.tcp.ActivateAndServe()
	return nil
}

// StartWait занимает адрес, дожидаясь его появления. Одиночной попытки мало:
// на роутере /opt монтируется раньше, чем интерфейс получает адрес, и bind
// падает с EADDRNOTAVAIL — а Entware-шный rc.func запускает init-скрипт один
// раз и после неудачи демона не поднимает. Всё остальное (занятый порт,
// отсутствие прав) ожиданием не лечится — такие ошибки возвращаются сразу.
func (s *Server) StartWait(ctx context.Context) error {
	delay := s.retryMin
	for {
		err := s.Start()
		if err == nil || !addrNotAvail(err) {
			return err
		}
		slog.Warn("listen address is not up yet, waiting for it",
			"addr", s.listen, "retry_in", delay, "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		if delay *= 2; delay > s.retryMax {
			delay = s.retryMax
		}
	}
}

// addrNotAvail — «адреса нет на интерфейсах», то есть он ещё (или уже) не
// поднят: единственная ошибка bind, которую имеет смысл переждать.
func addrNotAvail(err error) bool { return errors.Is(err, syscall.EADDRNOTAVAIL) }

func (s *Server) Addr() string { return s.addr }

func (s *Server) Shutdown() {
	if s.udp != nil {
		s.udp.Shutdown()
	}
	if s.tcp != nil {
		s.tcp.Shutdown()
	}
}
