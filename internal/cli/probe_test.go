package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

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

func TestBootstrapHint(t *testing.T) {
	silent := fmt.Errorf("x: %w", upstream.ErrBootstrapNoAnswer)
	if h := bootstrapHint([]probeResult{{Err: errors.New("dial: handshake failed")}, {}}); h != "" {
		t.Errorf("hint for a non-bootstrap failure: %q", h)
	}
	h := bootstrapHint([]probeResult{{}, {Err: silent}})
	if !strings.Contains(h, "77.88.8.8:1253") {
		t.Errorf("hint %q does not suggest the alternative port", h)
	}
}
