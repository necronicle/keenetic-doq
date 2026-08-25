package cli

import "testing"

func TestRunUnknownCommand(t *testing.T) {
	if got := Run([]string{"frobnicate"}); got != 2 {
		t.Fatalf("Run(unknown) = %d, want 2", got)
	}
}

func TestRunHelp(t *testing.T) {
	if got := Run([]string{"help"}); got != 0 {
		t.Fatalf("Run(help) = %d, want 0", got)
	}
}
