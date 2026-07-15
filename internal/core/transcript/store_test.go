package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriterAssignsMonotonicSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w := NewWriter(path)

	first, err := w.Append(Record{Board: "b1", Session: "s1", Transport: "uart", Direction: "from-target", Source: "daemon", Data: []byte("a")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.Append(Record{Board: "b1", Session: "s1", Transport: "uart", Direction: "from-target", Source: "daemon", Data: []byte("b")})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("got seq %d, %d, want 1, 2", first.Seq, second.Seq)
	}

	got, err := w.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0].Data) != "a" || string(got[1].Data) != "b" {
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
