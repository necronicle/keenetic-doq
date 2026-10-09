package geo

import "testing"

func TestMatcherSuffixOnLabelBoundary(t *testing.T) {
	m := NewMatcher([]string{"openai.com", "Example.AI."})
	for name, want := range map[string]bool{
		"openai.com.":      true,
		"auth.openai.com.": true,
		"AUTH.OpenAI.com":  true,
		"notopenai.com.":   false,
		"openai.com.evil.": false,
		"x.example.ai":     true,
		"com.":             false,
		"":                 false,
	} {
		if got := m.Match(name); got != want {
			t.Errorf("Match(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestBuiltinCoversProbeDomains(t *testing.T) {
	m := NewMatcher(BuiltinDomains())
	for _, p := range ProbeDomains {
		if !m.Match(p) {
			t.Errorf("probe domain %s is not in the built-in list", p)
		}
	}
	if !m.Match("cdn.oaistatic.com.") || !m.Match("api.anthropic.com.") {
		t.Error("built-in list misses OpenAI/Anthropic hosts")
	}
}
