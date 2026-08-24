package server

import (
	"context"
	"errors"
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
