package cli

import "testing"

func TestRunUnknownCommand(t *testing.T) {
	if got := Run([]string{"frobnicate"}); got != 2 {
		t.Fatalf("Run(unknown) = %d, want 2", got)
	}
}

func TestIsSubcommand(t *testing.T) {
	if IsSubcommand("frobnicate") {
		t.Fatal("frobnicate must not be a subcommand")
	}
	if !IsSubcommand("help") {
		t.Fatal("help must be a subcommand")
	}
}
