package geo

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestPoolMatchesByIPAndCNAME(t *testing.T) {
	p := NewPool()
	m := aMsg("chatgpt.com.", "95.81.98.64")
	m.Answer = append([]dns.RR{&dns.CNAME{
		Hdr:    dns.RR_Header{Name: "chatgpt.com.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60},
		Target: "AI-Pool.comss.one.",
	}}, m.Answer...)
	p.Add(m)
	if !p.Matches(aMsg("x.example.", "95.81.98.64")) {
		t.Error("pool IP must match")
	}
	viaCNAME := new(dns.Msg)
	viaCNAME.Answer = []dns.RR{&dns.CNAME{
		Hdr:    dns.RR_Header{Name: "y.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60},
		Target: "ai-pool.comss.one.",
	}}
	if !p.Matches(viaCNAME) {
		t.Error("pool CNAME target must match, case-insensitively")
	}
	if p.Matches(aMsg("z.example.", "104.18.32.47")) {
		t.Error("foreign IP must not match")
	}
}

func TestFingerprintsMatchAny(t *testing.T) {
	f := NewFingerprints()
	f.Set("g1", poolOf("1.1.1.1"))
	f.AddTo("g2", aMsg("chatgpt.com.", "2.2.2.2"))
	if !f.MatchAny(aMsg("a.", "2.2.2.2")) || !f.MatchAny(aMsg("a.", "1.1.1.1")) {
		t.Fatal("both pools must match")
	}
	if f.MatchAny(aMsg("a.", "3.3.3.3")) || f.MatchAny(nil) {
		t.Fatal("unknown IP / nil must not match")
	}
}

func TestClassifier(t *testing.T) {
	c := NewClassifier(NewMatcher([]string{"openai.com"}))
	now := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return now }
	if c.Lookup("auth.openai.com.") != Geo {
		t.Fatal("static list must give Geo")
	}
	if c.Lookup("example.org.") != Unknown {
		t.Fatal("unseen name must be Unknown")
	}
	c.Learn("Example.ORG.", Plain)
	c.Learn("proxied.example.", Geo)
	if c.Lookup("example.org") != Plain || c.Lookup("proxied.example.") != Geo {
		t.Fatal("learned verdicts lost")
	}
	if c.GeoCount() != 1 {
		t.Fatalf("GeoCount = %d, want 1", c.GeoCount())
	}
	now = now.Add(6*time.Hour + time.Second)
	if c.Lookup("example.org.") != Unknown {
		t.Fatal("Plain must expire after 6h for a re-check")
	}
	if c.Lookup("proxied.example.") != Geo {
		t.Fatal("Geo must not expire")
	}
}

func TestClassifierEvictsOldest(t *testing.T) {
	c := NewClassifier(NewMatcher())
	c.max = 2
	c.Learn("a.example.", Geo)
	c.Learn("b.example.", Geo)
	c.Lookup("a.example.") // a — свежее b
	c.Learn("c.example.", Geo)
	if c.Lookup("b.example.") != Unknown || c.Lookup("a.example.") != Geo || c.Lookup("c.example.") != Geo {
		t.Fatal("LRU eviction must drop the least recently used name")
	}
}

func TestPlainNeverReplacesGeo(t *testing.T) {
	c := NewClassifier(NewMatcher())
	c.Learn("a.example.", Geo)
	c.Learn("a.example.", Plain)
	if c.Lookup("a.example.") != Geo {
		t.Fatal("geo verdict must be sticky")
	}
	c.Learn("b.example.", Plain)
	c.Learn("b.example.", Geo)
	if c.Lookup("b.example.") != Geo {
		t.Fatal("geo must replace plain")
	}
}
