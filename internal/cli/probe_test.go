package cli

import "testing"

func TestProbeBadURL(t *testing.T) {
	if r := probe("https://not-quic.example", nil); r.Err == nil {
		t.Fatal("non-quic scheme must fail")
	}
}

func TestRunTestUsage(t *testing.T) {
	if got := Run([]string{"test"}); got != 2 {
		t.Fatalf("test without args = %d, want 2", got)
	}
}
