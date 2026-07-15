// Package v1 is the versioned, newline-delimited JSON wire protocol
// between cmd/gateway and gatewayd. It carries no transport-specific
// assumptions, so it can be reused unchanged behind a future Windows
// Named Pipe IPC implementation.
package v1

import "encoding/json"

// Version is the protocol version this package implements. A daemon
// rejects any request whose Version does not match exactly.
const Version = "1"

// MaxFrameBytes bounds one newline-delimited request or response
// frame.
const MaxFrameBytes = 1 << 20 // 1 MiB

// Operation names. Approval, secret entry, transport writes, retry,
// takeover, and host-key acceptance are deliberately absent from the
// AI-capable control surface; only an attach connection may use them.
const (
	OpPortsList       = "ports.list"
	OpSessionStart    = "session.start"
	OpSessionStatus   = "session.status"
	OpSessionEnd      = "session.end"
	OpOutputRead      = "output.read"
	OpCommandPropose  = "command.propose"
	OpCommandList     = "command.list"
	OpRecordsExport   = "records.export"
	OpDiagnoseExecute = "diagnose.execute"
	// OpUnsafeShellExecute is reachable only on a RoleUnsafeShell
	// connection, itself created only when gatewayd is started with
	// --unsafe-auto-shell for a board whose unsafe-shell file has been
	// loaded. It is otherwise absent from the AI-capable control
	// surface, same as approval and the other attach-only operations
	// below.
	OpUnsafeShellExecute = "unsafeshell.execute"

	OpCommandApprove = "command.approve"
	OpCommandReject  = "command.reject"
	OpCommandEdit    = "command.edit"
	OpSecretBegin    = "secret.begin"
	OpSecretDone     = "secret.done"
	OpTransportWrite = "transport.write"
	OpRetryUART      = "retry.uart"
	OpRetrySSH       = "retry.ssh"
	OpTakeover       = "takeover"
	OpHostKeyAccept  = "hostkey.accept"
)

// Protocol error codes.
const (
	ErrCodeUnknownVersion   = "unknown_version"
	ErrCodeFrameTooLarge    = "frame_too_large"
	ErrCodePermissionDenied = "permission_denied"
	ErrCodeUnknownOperation = "unknown_operation"
	ErrCodeInvalidPayload   = "invalid_payload"
	ErrCodeInternal         = "internal"
)

// Request is one client-to-daemon call.
type Request struct {
	Version   string          `json:"version"`
	RequestID string          `json:"request_id"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// ProtocolError is a machine-readable failure reason.
type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ProtocolError) Error() string { return e.Code + ": " + e.Message }

// Response is one daemon-to-client reply. Exactly one of Result or
// Error is set.
type Response struct {
	Version   string          `json:"version"`
	RequestID string          `json:"request_id"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *ProtocolError  `json:"error,omitempty"`
}
