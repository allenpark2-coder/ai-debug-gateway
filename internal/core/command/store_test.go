package command

import (
	"errors"
	"testing"
	"time"
)

func TestApproveSnapshotsIntoTransaction(t *testing.T) {
	s := NewStore()
	p, err := s.Propose(Input{SessionID: "s1", Text: "pwd", Purpose: "cwd", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Approve(p.ID)
	if err != nil || tx.ID == p.ID || tx.SourceProposalID != p.ID {
		t.Fatal(tx, err)
	}
	if _, err = s.Approve(p.ID); !errors.Is(err, ErrNotPending) {
		t.Fatal(err)
	}
}

func TestRejectInvalidCommandText(t *testing.T) {
	s := NewStore()
	if _, err := s.Propose(Input{SessionID: "s1", Text: "sleep 1 &"}); err == nil {
		t.Fatal("expected invalid managed command to be rejected at Propose")
	}
}

func TestProposalExpires(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := NewStore(WithClock(func() time.Time { return clock() }), WithProposalTTL(time.Minute))

	p, err := s.Propose(Input{SessionID: "s1", Text: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Approve(p.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("got %v, want ErrExpired", err)
	}
}

func TestEditReplacesProposal(t *testing.T) {
	s := NewStore()
	p, err := s.Propose(Input{SessionID: "s1", Text: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Edit(p.ID, "uname -a", "check kernel")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == p.ID {
		t.Fatal("edit must produce a new proposal ID")
	}
	if next.ReplacesID != p.ID {
		t.Fatalf("got ReplacesID=%q, want %q", next.ReplacesID, p.ID)
	}
	if _, err := s.Approve(p.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("original proposal must no longer be approvable, got %v", err)
	}
	if _, err := s.Approve(next.ID); err != nil {
		t.Fatalf("replacement proposal must be approvable, got %v", err)
	}
}

func TestInvalidateSessionExpiresPendingOnly(t *testing.T) {
	s := NewStore()
	pending, err := s.Propose(Input{SessionID: "s1", Text: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.Propose(Input{SessionID: "s1", Text: "uname -a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(approved.ID); err != nil {
		t.Fatal(err)
	}

	s.InvalidateSession("s1")

	if got := s.PendingForSession("s1"); len(got) != 0 {
		t.Fatalf("expected no pending proposals after invalidation, got %+v", got)
	}
	if _, err := s.Approve(pending.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("invalidated proposal must not be approvable, got %v", err)
	}
}

func TestAdvisoryWarningForKnownInteractiveCommand(t *testing.T) {
	s := NewStore()
	p, err := s.Propose(Input{SessionID: "s1", Text: "vi /etc/hosts"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Advisory == "" {
		t.Fatal("expected an advisory warning for a known interactive command")
	}

	p2, err := s.Propose(Input{SessionID: "s1", Text: "uname -a"})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Advisory != "" {
		t.Fatalf("did not expect an advisory warning, got %q", p2.Advisory)
	}
}

func TestCompleteTransactionRecordsResultOnce(t *testing.T) {
	s := NewStore()
	p, err := s.Propose(Input{SessionID: "s1", Text: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	res := Result{TransactionID: tx.ID, Status: StatusCompleted, ExitCode: &code, Duration: 5 * time.Millisecond}
	if err := s.CompleteTransaction(res); err != nil {
		t.Fatal(err)
	}
	got, err := s.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceProposalID != p.ID {
		t.Fatalf("result must carry the source proposal ID, got %q", got.SourceProposalID)
	}
	if err := s.CompleteTransaction(res); !errors.Is(err, ErrAlreadyCompleted) {
		t.Fatalf("got %v, want ErrAlreadyCompleted", err)
	}
}
