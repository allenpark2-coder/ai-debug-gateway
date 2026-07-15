package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriterHasIndependentSequenceAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w := NewWriter(path)

	first, err := w.Append(Record{Board: "b1", Session: "s1", Transport: "uart", Kind: "proposal", Detail: "prop-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.Append(Record{Board: "b1", Session: "s1", Transport: "uart", Kind: "approval", Detail: "prop-1"})
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
	if len(got) != 2 || got[0].Kind != "proposal" || got[1].Kind != "approval" {
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

func TestAuditAndTranscriptFilesAreIndependent(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	otherPath := filepath.Join(dir, "transcript.jsonl")

	w := NewWriter(auditPath)
	if _, err := w.Append(Record{Kind: "state-transition", Detail: "READY"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(otherPath); !os.IsNotExist(err) {
		t.Fatalf("audit writer must not touch %q", otherPath)
	}
}
