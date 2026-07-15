package main

import "testing"

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"10MiB", 10 << 20},
		{"64MiB", 64 << 20},
		{"1KiB", 1 << 10},
		{"2GiB", 2 << 30},
		{"512B", 512},
		{"4096", 4096},
	}
	for _, c := range cases {
		got, err := parseByteSize(c.in)
		if err != nil {
			t.Errorf("parseByteSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseByteSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseByteSizeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "MiB", "10XiB", "-5MiB", "ten"} {
		if _, err := parseByteSize(in); err == nil {
			t.Errorf("parseByteSize(%q): want error, got nil", in)
		}
	}
}
