package main

import "testing"

func TestFlagValue(t *testing.T) {
	args := []string{"--board", "b1", "--text", "uname -a"}
	if got := flagValue(args, "--board"); got != "b1" {
		t.Fatalf("got %q, want b1", got)
	}
	if got := flagValue(args, "--text"); got != "uname -a" {
		t.Fatalf("got %q, want %q", got, "uname -a")
	}
	if got := flagValue(args, "--missing"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := flagValue([]string{"--board"}, "--board"); got != "" {
		t.Fatalf("a flag with no following value must not panic or return garbage, got %q", got)
	}
}

func TestParseUintAndParseInt(t *testing.T) {
	if got := parseUint("42"); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	if got := parseUint(""); got != 0 {
		t.Fatalf("got %d, want 0 for empty input", got)
	}
	if got := parseInt("-5"); got != -5 {
		t.Fatalf("got %d, want -5", got)
	}
}
