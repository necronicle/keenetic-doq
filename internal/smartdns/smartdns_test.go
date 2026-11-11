package smartdns

import (
	"context"
	"fmt"
	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
	cached "github.com/necronicle/keenetic-doq/internal/resolver"
	"github.com/necronicle/keenetic-doq/internal/server"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type nextFunc func(context.Context, *dns.Msg) (*dns.Msg, error)

func (f nextFunc) Resolve(c context.Context, q *dns.Msg) (*dns.Msg, error)  { return f(c, q) }
func (f nextFunc) Address() string                                          { return "test" }
func (f nextFunc) Exchange(c context.Context, q *dns.Msg) (*dns.Msg, error) { return f(c, q) }
func file(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hosts")
	write(t, p, s)
	return p
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func query(r *Resolver, n string, typ uint16) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(n, typ)
	m, e := r.Resolve(context.Background(), q)
	if e != nil {
		panic(e)
	}
	return m
}
func upstreamAnswer(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetReply(q)
	m.AuthenticatedData = true
	rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN A 203.0.113.99")
	m.Answer = []dns.RR{rr}
	return m, nil
}
func TestParserAndLocalAnswers(t *testing.T) {
	p := file(t, "# comment\n192.0.2.1 Example.COM alias.example # aliases\n192.0.2.1 example.com.\n192.0.2.2 example.com\n2001:db8::1 example.com\n")
	table, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if table.Count() != 2 {
		t.Fatal(table.Count())
	}
	r, e := New(p, nextFunc(upstreamAnswer))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		n     string
		typ   uint16
		count int
	}{{"ExAmPlE.CoM.", dns.TypeA, 2}, {"example.com.", dns.TypeAAAA, 1}, {"alias.example.", dns.TypeAAAA, 0}, {"example.com.", dns.TypeHTTPS, 0}, {"example.com.", dns.TypeSVCB, 0}, {"example.com.", dns.TypeCNAME, 0}, {"example.com.", dns.TypeANY, 3}} {
		m := query(r, tc.n, tc.typ)
		if m.Rcode != dns.RcodeSuccess || m.AuthenticatedData || len(m.Answer) != tc.count {
			t.Fatalf("%+v: %v", tc, m)
		}
		for _, rr := range m.Answer {
			if rr.Header().Ttl != 60 {
				t.Fatal(rr)
			}
		}
	}
	if m := query(r, "sub.example.com.", dns.TypeA); m.Answer[0].(*dns.A).A.String() != "203.0.113.99" {
		t.Fatal(m)
	}
	if m := query(r, "example.com.", dns.TypeTXT); !m.Response {
		t.Fatal(m)
	}
}
func TestInvalidHosts(t *testing.T) {
	for _, s := range []string{"bad example.com", "192.0.2.1", "192.0.2.1 *.com", "192.0.2.1 *", "192.0.2.1 *.", "192.0.2.1 a.*.example.com", "192.0.2.1 *example.com", "192.0.2.1 **.example.com", "192.0.2.1 *.*.example.com", "192.0.2.1 bad..example", "192.0.2.1 https://example.com", "192.0.2.1 -bad.example", "192.0.2.1 example.com extra!"} {
		if _, e := Load(file(t, s)); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if _, e := Load("/nonexistent/hosts"); e == nil {
		t.Fatal("missing accepted")
	}
}
func TestBuiltin(t *testing.T) {
	b := Builtin()
	table, e := Load("builtin")
	if e != nil {
		t.Fatal(e)
	}
	if table.Count() != 405 || table.Wildcards() != 234 {
		t.Errorf("got %d names and %d masks, want 405 and 234", table.Count(), table.Wildcards())
	}
	r, e := New("builtin", nextFunc(upstreamAnswer))
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"chatgpt.com", "auth.openai.com", "cdn.oaistatic.com", "ios.chat.openai.com", "api.openai.com", "chat.openai.com", "files.oaiusercontent.com", "oaistatic.com"} {
		t.Run(name, func(t *testing.T) {
			answer := query(r, name+".", dns.TypeA).Answer
			if len(answer) != 1 {
				t.Fatalf("got %d A answers, want 1", len(answer))
			}
			a, ok := answer[0].(*dns.A)
			if !ok || a.A.String() != "217.60.179.6" {
				t.Fatalf("got %v, want 217.60.179.6", answer[0])
			}
		})
	}
	if answer := query(r, "game.clashroyaleapp.com.", dns.TypeA).Answer; len(answer) != 1 || answer[0].(*dns.A).A.String() != "81.17.20.82" {
		t.Fatalf("clash royale game: %v", answer)
	}
	for _, name := range []string{"game-assets.clashroyaleapp.com", "accounts.supercell.com", "store.supercell.com"} {
		answer := query(r, name+".", dns.TypeA).Answer
		if len(answer) != 2 || answer[0].(*dns.A).A.String() != "5.175.161.3" || answer[1].(*dns.A).A.String() != "5.175.161.40" {
			t.Fatalf("%s: %v", name, answer)
		}
	}
	if len(query(r, "releases.openai.com.", dns.TypeA).Answer) != 2 {
		t.Fatal("release missing")
	}
	// Masks cover unlisted subdomains, but not the bare domain.
	for _, name := range []string{"ws.chatgpt.com", "a.b.naukri.com", "new.claude.ai"} {
		answer := query(r, name+".", dns.TypeA).Answer
		if len(answer) != 2 || answer[0].(*dns.A).A.String() != "5.175.161.3" {
			t.Fatalf("%s: %v", name, answer)
		}
	}
	if query(r, "unlisted.google.com.", dns.TypeA).Answer[0].(*dns.A).A.String() != "203.0.113.99" {
		t.Fatal("google.com must not be masked")
	}
	for _, name := range []string{"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com", "notebooklm.google.com", "content-push.googleapis.com"} {
		answer := query(r, name+".", dns.TypeA).Answer
		if len(answer) != 2 || answer[0].(*dns.A).A.String() != "5.175.161.3" || answer[1].(*dns.A).A.String() != "5.175.161.40" {
			t.Fatalf("%s: %v", name, answer)
		}
	}
	b[0] = '!'
	if _, e := Load("builtin"); e != nil {
		t.Fatal(e)
	}
}
func TestReloadBeforeCacheAndFailureRetention(t *testing.T) {
	p := file(t, "")
	c := cache.New(10, time.Second, time.Hour)
	r, e := New(p, cached.New(c, nextFunc(upstreamAnswer)))
	if e != nil {
		t.Fatal(e)
	}
	query(r, "example.com.", dns.TypeA)
	write(t, p, "192.0.2.1 example.com\n")
	if e = r.Reload(); e != nil {
		t.Fatal(e)
	}
	if got := query(r, "example.com.", dns.TypeA).Answer[0].(*dns.A).A.String(); got != "192.0.2.1" {
		t.Fatal(got)
	}
	write(t, p, "invalid")
	if e = r.Reload(); e == nil {
		t.Fatal("invalid reload succeeded")
	}
	if got := query(r, "example.com.", dns.TypeA).Answer[0].(*dns.A).A.String(); got != "192.0.2.1" {
		t.Fatal(got)
	}
	write(t, p, "")
	if e = r.Reload(); e != nil {
		t.Fatal(e)
	}
	if got := query(r, "example.com.", dns.TypeA).Answer[0].(*dns.A).A.String(); got != "203.0.113.99" {
		t.Fatal(got)
	}
}
func TestCNAMEChainAndInflightReload(t *testing.T) {
	p := file(t, "")
	started, finish := make(chan struct{}), make(chan struct{})
	r, e := New(p, nextFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
		close(started)
		<-finish
		m := new(dns.Msg)
		m.SetReply(q)
		m.AuthenticatedData = true
		for _, s := range []string{"alias.example. 300 IN CNAME example.com.", "example.com. 300 IN CNAME direct.example.", "direct.example. 300 IN A 203.0.113.99"} {
			rr, _ := dns.NewRR(s)
			m.Answer = append(m.Answer, rr)
		}
		return m, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan *dns.Msg, 1)
	go func() { done <- query(r, "alias.example.", dns.TypeA) }()
	<-started
	write(t, p, "192.0.2.1 example.com")
	if e = r.Reload(); e != nil {
		t.Fatal(e)
	}
	close(finish)
	m := <-done
	if m.AuthenticatedData || len(m.Answer) != 2 || m.Answer[1].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatal(m)
	}
}
func TestConcurrentReloadAndUDPAndTCP(t *testing.T) {
	p := file(t, "192.0.2.1 example.com")
	r, e := New(p, nextFunc(upstreamAnswer))
	if e != nil {
		t.Fatal(e)
	}
	s := server.New("127.0.0.1:0", r)
	if e = s.Start(); e != nil {
		t.Fatal(e)
	}
	defer s.Shutdown()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if e := r.Reload(); e != nil {
				t.Error(e)
			}
		}
	}()
	for _, network := range []string{"udp", "tcp"} {
		for _, typ := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeHTTPS, dns.TypeSVCB} {
			q := new(dns.Msg)
			q.SetQuestion("EXAMPLE.com.", typ)
			m, _, e := (&dns.Client{Net: network}).Exchange(q, s.Addr())
			if e != nil {
				t.Fatal(e)
			}
			want := 0
			if typ == dns.TypeA {
				want = 1
			}
			if len(m.Answer) != want || m.AuthenticatedData || m.Rcode != 0 {
				t.Fatal(m)
			}
		}
	}
	wg.Wait()
}

func TestAliasOverrideDropsDirectTailFromAllSections(t *testing.T) {
	r, e := New(file(t, "192.0.2.1 example.com"), nextFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetReply(q)
		m.AuthenticatedData = true
		for _, s := range []string{"alias.example. 300 IN CNAME example.com.", "example.com. 300 IN CNAME direct.example.", "direct.example. 300 IN A 203.0.113.99"} {
			rr, _ := dns.NewRR(s)
			m.Answer = append(m.Answer, rr)
		}
		rr, _ := dns.NewRR("direct.example. 300 IN A 203.0.113.99")
		m.Extra = []dns.RR{rr}
		m.Ns = []dns.RR{dns.Copy(rr)}
		return m, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	m := query(r, "alias.example.", dns.TypeA)
	if len(m.Extra) != 0 || len(m.Ns) != 0 {
		t.Fatalf("direct tail leaked: %v", m)
	}
}

func TestInflightErrorAfterReloadUsesNewLocalAnswer(t *testing.T) {
	p := file(t, "")
	started, finish := make(chan struct{}), make(chan struct{})
	r, e := New(p, nextFunc(func(context.Context, *dns.Msg) (*dns.Msg, error) {
		close(started)
		<-finish
		return nil, context.DeadlineExceeded
	}))
	if e != nil {
		t.Fatal(e)
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	done := make(chan error, 1)
	go func() {
		m, e := r.Resolve(context.Background(), q)
		if e == nil && (len(m.Answer) != 1 || m.Answer[0].(*dns.A).A.String() != "192.0.2.1") {
			e = fmt.Errorf("wrong answer: %v", m)
		}
		done <- e
	}()
	<-started
	write(t, p, "192.0.2.1 example.com")
	if e = r.Reload(); e != nil {
		t.Fatal(e)
	}
	close(finish)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestLocalOverrideBeatsStaleCacheAndDoesNotPopulateIt(t *testing.T) {
	p := file(t, "192.0.2.1 example.com")
	c := cache.New(10, 0, 0)
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	old, _ := upstreamAnswer(context.Background(), q)
	c.Put(cache.KeyOf(q), old)
	if c.Get(cache.KeyOf(q)) != nil || c.GetStale(cache.KeyOf(q)) == nil {
		t.Fatal("fixture must be stale")
	}
	r, e := New(p, cached.New(c, nextFunc(func(context.Context, *dns.Msg) (*dns.Msg, error) { return nil, context.DeadlineExceeded })))
	if e != nil {
		t.Fatal(e)
	}
	if got := query(r, "example.com.", dns.TypeA).Answer[0].(*dns.A).A.String(); got != "192.0.2.1" {
		t.Fatal(got)
	}
	write(t, p, "")
	if e = r.Reload(); e != nil {
		t.Fatal(e)
	}
	m := query(r, "example.com.", dns.TypeA)
	if got := m.Answer[0].(*dns.A).A.String(); got != "203.0.113.99" {
		t.Fatalf("local answer persisted in cache: %v", m)
	}
}

func TestUnlistedAndOtherTypesPassThrough(t *testing.T) {
	r, e := New(file(t, "192.0.2.1 example.com"), nextFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetReply(q)
		m.AuthenticatedData = true
		rr, _ := dns.NewRR(q.Question[0].Name + " 300 IN TXT \"ordinary\"")
		m.Answer = []dns.RR{rr}
		return m, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"example.com.", "sub.example.com.", "unrelated.test."} {
		m := query(r, name, dns.TypeTXT)
		if !m.AuthenticatedData || len(m.Answer) != 1 || m.Answer[0].(*dns.TXT).Txt[0] != "ordinary" {
			t.Fatal(m)
		}
	}
}

func TestWildcardSuffix(t *testing.T) {
	p := file(t, "192.0.2.1 *.example.com\n192.0.2.2 exact.example.com\n192.0.2.3 *.deep.example.com\n2001:db8::1 *.Example.COM.\n192.0.2.4 *.cdn.test\n")
	table, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if table.Count() != 1 || table.Wildcards() != 3 {
		t.Fatalf("names=%d wildcards=%d", table.Count(), table.Wildcards())
	}
	r, e := New(p, nextFunc(upstreamAnswer))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		n    string
		typ  uint16
		want []string
	}{
		{"a.example.com.", dns.TypeA, []string{"192.0.2.1"}},
		{"A.B.C.Example.Com.", dns.TypeA, []string{"192.0.2.1"}},
		{"a.example.com.", dns.TypeAAAA, []string{"2001:db8::1"}},
		{"a.example.com.", dns.TypeHTTPS, nil},
		{"exact.example.com.", dns.TypeA, []string{"192.0.2.2"}},
		{"exact.example.com.", dns.TypeAAAA, nil},
		{"sub.exact.example.com.", dns.TypeA, []string{"192.0.2.1"}},
		{"deep.example.com.", dns.TypeA, []string{"192.0.2.1"}},
		{"x.deep.example.com.", dns.TypeA, []string{"192.0.2.3"}},
		{"x.y.deep.example.com.", dns.TypeA, []string{"192.0.2.3"}},
		{"example.com.", dns.TypeA, []string{"203.0.113.99"}},
		{"notexample.com.", dns.TypeA, []string{"203.0.113.99"}},
		{"x.cdn.test.", dns.TypeA, []string{"192.0.2.4"}},
	} {
		m := query(r, tc.n, tc.typ)
		var got []string
		for _, rr := range m.Answer {
			switch v := rr.(type) {
			case *dns.A:
				got = append(got, v.A.String())
			case *dns.AAAA:
				got = append(got, v.AAAA.String())
			}
		}
		if m.Rcode != dns.RcodeSuccess || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s %s: got %v want %v", tc.n, dns.TypeToString[tc.typ], got, tc.want)
		}
		for _, rr := range m.Answer {
			if rr.Header().Name != tc.n {
				t.Errorf("%s: owner %s", tc.n, rr.Header().Name)
			}
		}
	}
}

func TestWildcardCNAMETargetAndSectionFilter(t *testing.T) {
	r, e := New(file(t, "192.0.2.1 *.proxied.test"), nextFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetReply(q)
		for _, s := range []string{"alias.example. 300 IN CNAME edge.proxied.test.", "edge.proxied.test. 300 IN A 203.0.113.99"} {
			rr, _ := dns.NewRR(s)
			m.Answer = append(m.Answer, rr)
		}
		rr, _ := dns.NewRR("ns.proxied.test. 300 IN A 203.0.113.98")
		m.Extra = []dns.RR{rr}
		return m, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	m := query(r, "alias.example.", dns.TypeA)
	if len(m.Answer) != 2 || m.Answer[1].(*dns.A).A.String() != "192.0.2.1" || m.Answer[1].Header().Name != "edge.proxied.test." || len(m.Extra) != 0 {
		t.Fatalf("wildcard CNAME target not overridden: %v", m)
	}
}
