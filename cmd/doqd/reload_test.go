package main

import (
	"context"
	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/smartdns"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReloadHostsSignalAndCancellation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	if e := os.WriteFile(p, []byte("192.0.2.1 example.com"), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := smartdns.New(p, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() { reloadHosts(ctx, r, signals); close(done) }()
	if e = os.WriteFile(p, []byte("192.0.2.2 example.com"), 0600); e != nil {
		t.Fatal(e)
	}
	signals <- syscall.SIGHUP
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	deadline := time.Now().Add(time.Second)
	for {
		m, e := r.Resolve(ctx, q)
		if e != nil {
			t.Fatal(e)
		}
		if m.Answer[0].(*dns.A).A.String() == "192.0.2.2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("signal did not reload")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload goroutine failed to stop")
	}
}
