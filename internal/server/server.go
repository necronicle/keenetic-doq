// Package server — обычный DNS-слушатель (UDP+TCP) поверх Resolver.
package server

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/miekg/dns"
)

type Resolver interface {
	Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}

const queryTimeout = 10 * time.Second

type Server struct {
	listen   string
	resolver Resolver
	addr     string
	udp      *dns.Server
	tcp      *dns.Server
}

func New(listen string, r Resolver) *Server {
	return &Server{listen: listen, resolver: r}
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
	pc, err := net.ListenPacket("udp", s.listen)
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

func (s *Server) Addr() string { return s.addr }

func (s *Server) Shutdown() {
	if s.udp != nil {
		s.udp.Shutdown()
	}
	if s.tcp != nil {
		s.tcp.Shutdown()
	}
}
