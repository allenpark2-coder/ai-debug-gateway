package cli

import (
	"encoding/json"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/id"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

// Client is a thin, synchronous wrapper over one IPC connection. Which
// socket path it dials (control vs. attach) is the caller's choice;
// the daemon enforces the resulting capability boundary, not this
// type.
type Client struct {
	conn *ipc.Client
}

// Dial connects to the daemon socket at path.
func Dial(path string) (*Client, error) {
	conn, err := ipc.Dial(path)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call issues one request and decodes its result into out, if out is
// non-nil and the daemon returned a result.
func (c *Client) Call(operation string, payload, out any) error {
	var raw json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = data
	}
	resp, err := c.conn.Call(v1.Request{RequestID: id.New("req"), Operation: operation, Payload: raw})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// PortsList lists discovered serial ports.
func (c *Client) PortsList() (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpPortsList, nil, &out)
	return out, err
}

// SessionStart starts a session for board (empty uses the daemon's
// default board).
func (c *Client) SessionStart(board string) (json.RawMessage, error) {
	return c.SessionStartWithOptions(board, SessionStartOptions{})
}

// SessionStartOptions carries session.start fields beyond the board
// name: which transport to use when a profile configures both, and
// SSH-only secrets entered for this one connection attempt (never
// persisted). SSHAcceptHost is only ever honored by the daemon on an
// attach connection; a control connection cannot accept a new host
// key regardless of this field.
type SessionStartOptions struct {
	Transport         string
	SSHPassword       string
	SSHKeyPassphrases map[string]string
	SSHAcceptHost     bool
}

// SessionStartWithOptions starts a session for board with opts.
func (c *Client) SessionStartWithOptions(board string, opts SessionStartOptions) (json.RawMessage, error) {
	var out json.RawMessage
	payload := map[string]any{"board": board}
	if opts.Transport != "" {
		payload["transport"] = opts.Transport
	}
	if opts.SSHPassword != "" {
		payload["ssh_password"] = opts.SSHPassword
	}
	if len(opts.SSHKeyPassphrases) > 0 {
		payload["ssh_key_passphrases"] = opts.SSHKeyPassphrases
	}
	if opts.SSHAcceptHost {
		payload["ssh_accept_host"] = true
	}
	err := c.Call(v1.OpSessionStart, payload, &out)
	return out, err
}

// SessionStatus reports the current session state.
func (c *Client) SessionStatus() (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpSessionStatus, nil, &out)
	return out, err
}

// SessionEnd disconnects the active transport.
func (c *Client) SessionEnd() (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpSessionEnd, nil, &out)
	return out, err
}

// OutputRead reads console output after sequence, up to max bytes.
func (c *Client) OutputRead(after uint64, max int) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpOutputRead, map[string]any{"after": after, "max": max}, &out)
	return out, err
}

// CommandPropose creates a pending proposal.
func (c *Client) CommandPropose(sessionID, text, purpose string, timeoutMS int64) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpCommandPropose, map[string]any{
		"session_id": sessionID, "text": text, "purpose": purpose, "timeout_ms": timeoutMS,
	}, &out)
	return out, err
}

// CommandList lists pending proposals for sessionID.
func (c *Client) CommandList(sessionID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpCommandList, map[string]string{"session_id": sessionID}, &out)
	return out, err
}

// RecordsExport exports durable transcript/audit records after the
// given sequence numbers.
func (c *Client) RecordsExport(afterTranscript, afterAudit uint64) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpRecordsExport, map[string]uint64{
		"after_transcript": afterTranscript, "after_audit": afterAudit,
	}, &out)
	return out, err
}

// CommandApprove approves a pending proposal (attach connections only).
func (c *Client) CommandApprove(proposalID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpCommandApprove, map[string]string{"proposal_id": proposalID}, &out)
	return out, err
}

// CommandReject rejects a pending proposal (attach connections only).
func (c *Client) CommandReject(proposalID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpCommandReject, map[string]string{"proposal_id": proposalID}, &out)
	return out, err
}

// CommandEdit replaces a pending proposal with new text/purpose
// (attach connections only).
func (c *Client) CommandEdit(proposalID, text, purpose string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpCommandEdit, map[string]string{"proposal_id": proposalID, "text": text, "purpose": purpose}, &out)
	return out, err
}

// TransportWrite forwards raw bytes to the target (attach connections
// only, and only Ctrl-C while a transaction is running).
func (c *Client) TransportWrite(data []byte) error {
	return c.Call(v1.OpTransportWrite, map[string]any{"data": data}, nil)
}

// RetryUART is the human-approved UART reconnect (attach connections
// only).
func (c *Client) RetryUART() (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpRetryUART, nil, &out)
	return out, err
}

// RetrySSH is the human-approved SSH reconnect (attach connections
// only).
func (c *Client) RetrySSH() (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpRetrySSH, nil, &out)
	return out, err
}

// Takeover ends the active transaction as interrupted-by-user and
// restores manual control (attach connections only).
func (c *Client) Takeover() (json.RawMessage, error) {
	var out json.RawMessage
	err := c.Call(v1.OpTakeover, nil, &out)
	return out, err
}

// SecretBegin opens the secret redaction window (attach connections
// only).
func (c *Client) SecretBegin() error { return c.Call(v1.OpSecretBegin, nil, nil) }

// SecretDone closes the secret redaction window (attach connections
// only).
func (c *Client) SecretDone() error { return c.Call(v1.OpSecretDone, nil, nil) }
