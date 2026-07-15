package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
)

func TestAcquireLockRejectsSecondInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gatewayd.lock")

	f1, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(f1)

	if _, err := acquireLock(path); err == nil {
		t.Fatal("expected a second instance to fail to acquire the lock")
	}

	releaseLock(f1)
	f2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("expected to acquire the lock after release, got %v", err)
	}
	releaseLock(f2)
}

func TestOpenSetPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.json")

	s1, err := loadOpenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.add("txn-1"); err != nil {
		t.Fatal(err)
	}
	if err := s1.add("txn-2"); err != nil {
		t.Fatal(err)
	}
	if err := s1.remove("txn-1"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want 0600", info.Mode().Perm())
	}

	s2, err := loadOpenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.list()
	if len(got) != 1 || got[0] != "txn-2" {
		t.Fatalf("got %+v, want [txn-2]", got)
	}
}

func TestRecoverIncompleteTransactionsFinalizesAndClearsOpenSet(t *testing.T) {
	dir := t.TempDir()
	open, err := loadOpenSet(filepath.Join(dir, "open.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := open.add("txn-stale"); err != nil {
		t.Fatal(err)
	}

	aw := audit.NewWriter(filepath.Join(dir, "audit.jsonl"))
	if err := recoverIncompleteTransactions(open, aw); err != nil {
		t.Fatal(err)
	}

	if got := open.list(); len(got) != 0 {
		t.Fatalf("expected the open set to be cleared, got %+v", got)
	}

	records, err := aw.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.Kind == "result" && strings.Contains(r.Detail, "txn-stale") && strings.Contains(r.Detail, "daemon-restarted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a daemon-restarted result record for txn-stale, got %+v", records)
	}
}
