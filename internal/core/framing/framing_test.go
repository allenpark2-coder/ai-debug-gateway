package framing

import (
	"os"
	"path/filepath"
	"testing"
)

type sample struct {
	Seq  int    `json:"seq"`
	Text string `json:"text"`
}

func TestAppendAndReadAllRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")

	for i := 1; i <= 3; i++ {
		if err := AppendJSONL(path, sample{Seq: i, Text: "line"}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ReadAllJSONL[sample](path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Seq != 1 || got[2].Seq != 3 {
		t.Fatalf("%+v", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want 0600", info.Mode().Perm())
	}
}

func TestReadAllJSONLMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	got, err := ReadAllJSONL[sample](path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}
