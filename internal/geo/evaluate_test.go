package geo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// probeAnswers — ответы, где каждый пробный домен подменён на ip.
func probeAnswers(ip string) map[string][]string {
	m := map[string][]string{}
	for _, d := range ProbeDomains {
		m[d] = []string{ip}
	}
	return m
}

// realAnswers — «настоящие» адреса пробных доменов (разные у каждого).
func realAnswers() map[string][]string {
	m := map[string][]string{}
	for i, d := range ProbeDomains {
		m[d] = []string{fmt.Sprintf("7.7.7.%d", i+1)}
	}
	return m
}

func newEvaluator(ref *fakeEx, p *fakeProber) *Evaluator {
	e := &Evaluator{Prober: p, Probes: ProbeDomains, Attempts: 3, Timeout: 5 * time.Second}
	if ref != nil {
		e.Reference = ref
	}
	return e
}

func TestEvaluateRanksByProxyTLS(t *testing.T) {
	ref := newFake("ref", realAnswers())
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	g2 := newFake("g2", probeAnswers("2.2.2.2"))
	p := &fakeProber{lat: map[string]time.Duration{"1.1.1.1": 900 * time.Millisecond, "2.2.2.2": 100 * time.Millisecond}}
	rs := newEvaluator(ref, p).Run(context.Background(), []Server{{URL: "g1", Ex: g1}, {URL: "g2", Ex: g2}})
	if len(rs) != 2 || rs[0].URL != "g2" {
		t.Fatalf("ranking = %+v", rs)
	}
	if rs[0].Coverage != len(ProbeDomains) || rs[0].Alive != 1 || rs[0].Total != 1 || rs[0].MedianTLSMs != 100 {
		t.Fatalf("g2 result = %+v", rs[0])
	}
	if !reflect.DeepEqual(rs[0].PoolIPs, []string{"2.2.2.2"}) || rs[0].SNI["2.2.2.2"] != "chatgpt.com" {
		t.Fatalf("g2 pool = %v sni = %v", rs[0].PoolIPs, rs[0].SNI)
	}
	if !rs[1].Pool().Matches(aMsg("x.", "1.1.1.1")) {
		t.Fatal("Result.Pool must rebuild the fingerprint")
	}
}

func TestEvaluateNotSubstitutedGivesNoCoverage(t *testing.T) {
	ref := newFake("ref", realAnswers())
	g1 := newFake("g1", realAnswers())
	rs := newEvaluator(ref, &fakeProber{}).Run(context.Background(), []Server{{URL: "g1", Ex: g1}})
	if rs[0].Coverage != 0 || len(rs[0].PoolIPs) != 0 {
		t.Fatalf("real addresses are not a proxy pool: %+v", rs[0])
	}
}

func TestEvaluateDeadProxies(t *testing.T) {
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	p := &fakeProber{}
	p.setDead("1.1.1.1", true)
	rs := newEvaluator(nil, p).Run(context.Background(), []Server{{URL: "g1", Ex: g1}})
	if rs[0].Coverage != 0 || rs[0].Alive != 0 || rs[0].Total != 1 || rs[0].MedianTLSMs != 0 {
		t.Fatalf("dead pool = %+v", rs[0])
	}
}

func TestEvaluateServerDown(t *testing.T) {
	g1 := newFake("g1", nil)
	g1.setErr(errors.New("timeout"))
	rs := newEvaluator(nil, &fakeProber{}).Run(context.Background(), []Server{{URL: "g1", Ex: g1}})
	if rs[0].Coverage != 0 || rs[0].Err == "" {
		t.Fatalf("down server = %+v", rs[0])
	}
}

func TestRank(t *testing.T) {
	order := []string{"g1", "g2", "g3"}
	got := Rank([]Result{
		{URL: "g1", Coverage: 3, MedianTLSMs: 120},
		{URL: "g2", Coverage: 3, MedianTLSMs: 100},
		{URL: "g3", Coverage: 2, MedianTLSMs: 50},
	}, order)
	if got[0].URL != "g1" || got[1].URL != "g2" || got[2].URL != "g3" {
		t.Fatalf("within 20%% the config order wins, coverage beats speed: %v", got)
	}
	got = Rank([]Result{
		{URL: "g1", Coverage: 3, MedianTLSMs: 130},
		{URL: "g2", Coverage: 3, MedianTLSMs: 100},
		{URL: "g3", Coverage: 3},
	}, order)
	if got[0].URL != "g2" || got[1].URL != "g1" || got[2].URL != "g3" {
		t.Fatalf("over 20%% the faster wins, no TLS data goes last: %v", got)
	}
}

// Баг 0.4.1: dns-ai подменяет только часть хоста сервиса; полный охват у geohide
// должен побеждать, даже если у dns-ai TLS намного быстрее.
func TestEvaluatePrefersFullProbeCoverage(t *testing.T) {
	ref := newFake("ref", realAnswers())
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	partial := realAnswers()
	for _, d := range []string{"chatgpt.com.", "auth.openai.com.", "claude.ai.", "gemini.google.com."} {
		partial[d] = []string{"2.2.2.2"}
	}
	g2 := newFake("g2", partial)
	p := &fakeProber{lat: map[string]time.Duration{"1.1.1.1": 900 * time.Millisecond, "2.2.2.2": 20 * time.Millisecond}}
	// Остальные адреса g2 совпадают с ref, поэтому не считаются подменой и
	// пропускаются; охват g2 — только четыре подменённых хоста.
	rs := newEvaluator(ref, p).Run(context.Background(), []Server{{URL: "g2", Ex: g2}, {URL: "g1", Ex: g1}})
	if rs[0].URL != "g1" || rs[0].Coverage != len(ProbeDomains) || rs[1].Coverage != 4 {
		t.Fatalf("full coverage must beat speed: %+v", rs)
	}
	if len(rs[0].Covered) != len(ProbeDomains) || !reflect.DeepEqual(rs[1].Covered,
		[]string{"chatgpt.com", "auth.openai.com", "claude.ai", "gemini.google.com"}) {
		t.Fatalf("covered lists: %v / %v", rs[0].Covered, rs[1].Covered)
	}
}

func TestProbeDomainsCoverNeededHosts(t *testing.T) {
	want := []string{"chatgpt.com.", "sentinel.openai.com.", "tcr9i.chat.openai.com.", "auth.openai.com.",
		"claude.ai.", "assets-proxy.anthropic.com.", "gemini.google.com."}
	if !reflect.DeepEqual(ProbeDomains, want) {
		t.Fatalf("ProbeDomains = %v", ProbeDomains)
	}
}

// Reference не ответил по одному хосту: этот хост не засчитывается никому.
func TestEvaluateReferenceFailureSkipsProbe(t *testing.T) {
	refAns := realAnswers()
	delete(refAns, "sentinel.openai.com.")
	ref := newFake("ref", refAns)
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	rs := newEvaluator(ref, &fakeProber{}).Run(context.Background(), []Server{{URL: "g1", Ex: g1}})
	if rs[0].Coverage != len(ProbeDomains)-1 {
		t.Fatalf("a probe without a reference answer must count for nobody: %+v", rs[0])
	}
	for _, c := range rs[0].Covered {
		if c == "sentinel.openai.com" {
			t.Fatal("sentinel must not be covered")
		}
	}
}

// Reference не ответил ни по одному хосту — оценка безрезультатна.
func TestEvaluateReferenceDownIsInconclusive(t *testing.T) {
	ref := newFake("ref", realAnswers())
	ref.setErr(errors.New("down"))
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	rs := newEvaluator(ref, &fakeProber{}).Run(context.Background(), []Server{{URL: "g1", Ex: g1}})
	if rs[0].Coverage != 0 {
		t.Fatalf("no reference at all: nobody gets coverage: %+v", rs[0])
	}
}
