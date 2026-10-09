package geo

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func probeAnswers(ip string) map[string][]string {
	return map[string][]string{"chatgpt.com.": {ip}, "claude.ai.": {ip}, "gemini.google.com.": {ip}}
}

func newEvaluator(ref *fakeEx, p *fakeProber) *Evaluator {
	e := &Evaluator{Prober: p, Probes: ProbeDomains, Attempts: 3, Timeout: 5 * time.Second}
	if ref != nil {
		e.Reference = ref
	}
	return e
}

func TestEvaluateRanksByProxyTLS(t *testing.T) {
	ref := newFake("ref", map[string][]string{"chatgpt.com.": {"7.7.7.7"}, "claude.ai.": {"7.7.7.8"},
		"gemini.google.com.": {"7.7.7.9"}})
	g1 := newFake("g1", probeAnswers("1.1.1.1"))
	g2 := newFake("g2", probeAnswers("2.2.2.2"))
	p := &fakeProber{lat: map[string]time.Duration{"1.1.1.1": 900 * time.Millisecond, "2.2.2.2": 100 * time.Millisecond}}
	rs := newEvaluator(ref, p).Run(context.Background(), []Server{{URL: "g1", Ex: g1}, {URL: "g2", Ex: g2}})
	if len(rs) != 2 || rs[0].URL != "g2" {
		t.Fatalf("ranking = %+v", rs)
	}
	if rs[0].Coverage != 3 || rs[0].Alive != 1 || rs[0].Total != 1 || rs[0].MedianTLSMs != 100 {
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
	ref := newFake("ref", probeAnswers("7.7.7.7"))
	g1 := newFake("g1", probeAnswers("7.7.7.7"))
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
