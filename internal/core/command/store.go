package command

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/id"
)

var (
	ErrNotFound         = errors.New("command: proposal or transaction not found")
	ErrNotPending       = errors.New("command: proposal is not pending")
	ErrExpired          = errors.New("command: proposal has expired")
	ErrAlreadyCompleted = errors.New("command: transaction already has a result")
)

// defaultInteractivePatterns lists program names that commonly enter a
// full-screen interactive mode. Matching is advisory only: it warns a
// human before approval and is never a security boundary, since it
// cannot identify arbitrary scripts that enter an interactive mode.
var defaultInteractivePatterns = []string{
	"vi", "vim", "nano", "top", "htop", "less", "more", "man", "menuconfig",
}

// Input is the AI- or human-supplied content of a new proposal.
type Input struct {
	SessionID string
	Transport string
	Board     string
	Text      string
	Purpose   string
	Timeout   time.Duration
}

// Store holds proposals, their approved transaction snapshots, and
// each transaction's single result. It is safe for concurrent use.
type Store struct {
	mu           sync.Mutex
	proposals    map[string]*Proposal
	transactions map[string]*Transaction
	results      map[string]*Result

	now                 func() time.Time
	proposalTTL         time.Duration
	interactivePatterns []string
}

// Option configures a Store at construction.
type Option func(*Store)

// WithClock overrides the store's time source, for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// WithProposalTTL overrides the default proposal expiration window.
func WithProposalTTL(d time.Duration) Option {
	return func(s *Store) { s.proposalTTL = d }
}

// WithInteractivePatterns overrides the default advisory command-name
// pattern list.
func WithInteractivePatterns(patterns ...string) Option {
	return func(s *Store) { s.interactivePatterns = patterns }
}

// NewStore constructs an empty Store.
func NewStore(opts ...Option) *Store {
	s := &Store{
		proposals:           make(map[string]*Proposal),
		transactions:        make(map[string]*Transaction),
		results:             make(map[string]*Result),
		now:                 time.Now,
		proposalTTL:         5 * time.Minute,
		interactivePatterns: defaultInteractivePatterns,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Store) advisoryFor(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	name := fields[0]
	for _, pat := range s.interactivePatterns {
		if name == pat {
			return "\"" + name + "\" commonly runs as a full-screen interactive " +
				"program; this warning is advisory only, cannot detect arbitrary " +
				"interactive scripts, and forced takeover remains the recovery " +
				"mechanism."
		}
	}
	return ""
}

// Propose validates text against the managed-command contract and, if
// valid, creates a new pending proposal.
func (s *Store) Propose(in Input) (*Proposal, error) {
	if err := ValidateManaged(in.Text); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	p := &Proposal{
		ID:        id.New("prop"),
		SessionID: in.SessionID,
		Transport: in.Transport,
		Board:     in.Board,
		Text:      in.Text,
		Purpose:   in.Purpose,
		Timeout:   in.Timeout,
		CreatedAt: now,
		ExpiresAt: now.Add(s.proposalTTL),
		State:     ProposalPending,
		Advisory:  s.advisoryFor(in.Text),
	}
	s.proposals[p.ID] = p
	return p, nil
}

// Edit marks proposalID as replaced and creates a new pending proposal
// with the given text and purpose. Editing does not approve or execute
// anything.
func (s *Store) Edit(proposalID, text, purpose string) (*Proposal, error) {
	if err := ValidateManaged(text); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.proposals[proposalID]
	if !ok {
		return nil, ErrNotFound
	}
	if old.State != ProposalPending {
		return nil, ErrNotPending
	}

	old.State = ProposalReplaced

	now := s.now()
	next := &Proposal{
		ID:         id.New("prop"),
		SessionID:  old.SessionID,
		Transport:  old.Transport,
		Board:      old.Board,
		Text:       text,
		Purpose:    purpose,
		Timeout:    old.Timeout,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.proposalTTL),
		State:      ProposalPending,
		ReplacesID: old.ID,
		Advisory:   s.advisoryFor(text),
	}
	s.proposals[next.ID] = next
	return next, nil
}

// Reject marks a pending proposal as rejected.
func (s *Store) Reject(proposalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.proposals[proposalID]
	if !ok {
		return ErrNotFound
	}
	if p.State != ProposalPending {
		return ErrNotPending
	}
	p.State = ProposalRejected
	return nil
}

// Approve snapshots a pending, unexpired proposal into a new immutable
// Transaction. Approval is single-use: a second Approve of the same
// proposal returns ErrNotPending.
func (s *Store) Approve(proposalID string) (*Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.proposals[proposalID]
	if !ok {
		return nil, ErrNotFound
	}
	if p.State != ProposalPending {
		return nil, ErrNotPending
	}
	if s.now().After(p.ExpiresAt) {
		p.State = ProposalExpired
		return nil, ErrExpired
	}

	p.State = ProposalApproved
	tx := &Transaction{
		ID:               id.New("txn"),
		SourceProposalID: p.ID,
		SessionID:        p.SessionID,
		Transport:        p.Transport,
		Board:            p.Board,
		Text:             p.Text,
		Timeout:          p.Timeout,
		ApprovedAt:       s.now(),
	}
	s.transactions[tx.ID] = tx
	return tx, nil
}

// InvalidateSession expires every pending proposal for sessionID. It
// does not touch already-approved transactions; the coordinator is
// responsible for finalizing an active transaction's Result (for
// example as disconnected, target-rebooted, or daemon-restarted).
func (s *Store) InvalidateSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range s.proposals {
		if p.SessionID == sessionID && p.State == ProposalPending {
			p.State = ProposalExpired
		}
	}
}

// PendingForSession returns every currently pending proposal for
// sessionID.
func (s *Store) PendingForSession(sessionID string) []*Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*Proposal
	for _, p := range s.proposals {
		if p.SessionID == sessionID && p.State == ProposalPending {
			out = append(out, p)
		}
	}
	return out
}

// CompleteTransaction records the single terminal Result for
// res.TransactionID. A second call for the same transaction returns
// ErrAlreadyCompleted.
func (s *Store) CompleteTransaction(res Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.transactions[res.TransactionID]
	if !ok {
		return ErrNotFound
	}
	if _, done := s.results[tx.ID]; done {
		return ErrAlreadyCompleted
	}
	res.SourceProposalID = tx.SourceProposalID
	s.results[tx.ID] = &res
	return nil
}

// Result returns the recorded result for transactionID, if any.
func (s *Store) Result(transactionID string) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.results[transactionID]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}
