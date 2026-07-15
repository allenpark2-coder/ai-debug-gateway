// Package command implements the managed-command lifecycle: a mutable
// proposal, an immutable approved transaction snapshot, and the single
// terminal result produced by executing it.
package command

import "time"

// ProposalState is the lifecycle state of a Proposal.
type ProposalState string

const (
	ProposalPending  ProposalState = "pending"
	ProposalRejected ProposalState = "rejected"
	ProposalExpired  ProposalState = "expired"
	ProposalReplaced ProposalState = "replaced"
	ProposalApproved ProposalState = "approved"
)

// Proposal is mutable, unapproved intent created by an AI or by a
// human edit.
type Proposal struct {
	ID        string
	SessionID string
	Transport string
	Board     string
	Text      string
	Purpose   string
	Timeout   time.Duration
	CreatedAt time.Time
	ExpiresAt time.Time
	State     ProposalState
	// ReplacesID is the ID of the proposal this one replaced via edit,
	// or empty if this proposal was not created by an edit.
	ReplacesID string
	// Advisory is a non-empty, non-enforcing warning when the command
	// text matches a configured pattern for known interactive programs.
	Advisory string
}

// Transaction is an immutable snapshot of one approved proposal.
type Transaction struct {
	ID               string
	SourceProposalID string
	SessionID        string
	Transport        string
	Board            string
	Text             string
	Timeout          time.Duration
	ApprovedAt       time.Time
}

// Status is the terminal outcome of executing a transaction.
type Status string

const (
	StatusCompleted         Status = "completed"
	StatusTimeout           Status = "timeout"
	StatusDisconnected      Status = "disconnected"
	StatusInterruptedByUser Status = "interrupted-by-user"
	StatusProtocolError     Status = "protocol-error"
	StatusTargetRebooted    Status = "target-rebooted"
	StatusDaemonRestarted   Status = "daemon-restarted"
)

// Result is the single terminal outcome of executing a transaction.
// Transactions are never retried implicitly, so at most one Result
// ever exists per transaction.
type Result struct {
	TransactionID    string
	SourceProposalID string
	Status           Status
	// ExitCode is set only when the shell completion marker was
	// observed; a nil ExitCode is never fabricated.
	ExitCode *int
	Duration time.Duration
	// Output is the combined console output for the execution interval.
	Output []byte
	// OutputTruncatedStart reports that the transcript ring had already
	// overwritten the beginning of this transaction's output.
	OutputTruncatedStart bool
	// ContextBefore and ContextAfter are bounded transcript context
	// surrounding execution, filled in by the coordinator.
	ContextBefore []byte
	ContextAfter  []byte
	CompletedAt   time.Time
}
