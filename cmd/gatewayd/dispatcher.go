package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/transcript"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
	sshtransport "github.com/allenpark2-coder/ai-debug-gateway/internal/transport/ssh"
)

// defaultRetentionLimit bounds how many durable records records.export
// returns per store. Full disk-level pruning of older records is
// future hardening work; this bounds what one export call returns.
const defaultRetentionLimit = 5000

// dispatcher wires the v1 protocol to one board's Coordinator, plus
// the durable audit/transcript stores and the open-transaction set
// used to recover from a crash.
type dispatcher struct {
	board      string
	profileDir string
	loginCfg   gateway.LoginConfig
	coord      *gateway.Coordinator
	open       *openSet
	aw         *audit.Writer
	tw         *transcript.Writer

	retentionLimit int

	// listPorts/openPort are overridden in tests to avoid touching real
	// hardware; production code leaves them nil and falls back to the
	// real serial package.
	listPorts func() ([]serial.Port, error)
	openPort  func(serial.Port, serial.LineSettings) (transport.Stream, error)

	// dialSSH is overridden in tests to avoid a real known_hosts file
	// and network dial; production code leaves it nil and falls back
	// to a real host-key verifier, a real auth factory, and ssh.Open.
	dialSSH func(prof *profile.SSHConfig, auth sshtransport.HumanAuth) (transport.Stream, error)
}

func newDispatcher(board, profileDir string, coord *gateway.Coordinator, open *openSet, aw *audit.Writer, tw *transcript.Writer, loginCfg gateway.LoginConfig) *dispatcher {
	return &dispatcher{
		board:          board,
		profileDir:     profileDir,
		loginCfg:       loginCfg,
		coord:          coord,
		open:           open,
		aw:             aw,
		tw:             tw,
		retentionLimit: defaultRetentionLimit,
	}
}

func (d *dispatcher) doListPorts() ([]serial.Port, error) {
	if d.listPorts != nil {
		return d.listPorts()
	}
	return serial.List()
}

func (d *dispatcher) doOpenPort(p serial.Port, line serial.LineSettings) (transport.Stream, error) {
	if d.openPort != nil {
		return d.openPort(p, line)
	}
	return serial.Open(p, line)
}

func (d *dispatcher) doDialSSH(prof *profile.SSHConfig, auth sshtransport.HumanAuth) (transport.Stream, error) {
	if d.dialSSH != nil {
		return d.dialSSH(prof, auth)
	}
	verifier, err := sshtransport.NewHostKeyVerifier(prof.KnownHostsFile)
	if err != nil {
		return nil, err
	}
	return sshtransport.Open(context.Background(), prof, verifier, sshtransport.NewAuthFactory(), auth)
}

func badPayload(err error) *v1.ProtocolError {
	return &v1.ProtocolError{Code: v1.ErrCodeInvalidPayload, Message: err.Error()}
}

func internalErr(err error) *v1.ProtocolError {
	return &v1.ProtocolError{Code: v1.ErrCodeInternal, Message: err.Error()}
}

// Dispatch implements ipc.Dispatcher. The server has already checked
// the protocol version and the role's operation allowlist before
// calling this.
func (d *dispatcher) Dispatch(role ipc.Role, req v1.Request) (any, *v1.ProtocolError) {
	switch req.Operation {
	case v1.OpPortsList:
		return d.portsList()
	case v1.OpSessionStart:
		return d.sessionStart(role, req.Payload)
	case v1.OpSessionStatus:
		return d.sessionStatus()
	case v1.OpSessionEnd:
		return d.sessionEnd()
	case v1.OpOutputRead:
		return d.outputRead(req.Payload)
	case v1.OpCommandPropose:
		return d.commandPropose(req.Payload)
	case v1.OpCommandList:
		return d.commandList(req.Payload)
	case v1.OpRecordsExport:
		return d.recordsExport(req.Payload)
	case v1.OpCommandApprove:
		return d.commandApprove(req.Payload)
	case v1.OpCommandReject:
		return d.commandReject(req.Payload)
	case v1.OpCommandEdit:
		return d.commandEdit(req.Payload)
	case v1.OpTransportWrite:
		return d.transportWrite(req.Payload)
	case v1.OpRetryUART:
		return d.retryUART()
	case v1.OpRetrySSH:
		return d.retrySSH()
	case v1.OpTakeover:
		return d.takeover()
	case v1.OpSecretBegin:
		return d.secretBegin()
	case v1.OpSecretDone:
		return d.secretDone()
	default:
		return nil, &v1.ProtocolError{Code: v1.ErrCodeUnknownOperation, Message: req.Operation}
	}
}

func (d *dispatcher) portsList() (any, *v1.ProtocolError) {
	ports, err := d.doListPorts()
	if err != nil {
		return nil, internalErr(err)
	}
	return map[string]any{"ports": ports}, nil
}

type sessionStartPayload struct {
	Board     string `json:"board"`
	Transport string `json:"transport,omitempty"` // "uart" or "ssh"; required only when a profile configures both

	// SSH-only: never persisted, only ever entered interactively for
	// this one connection attempt.
	SSHPassword       string            `json:"ssh_password,omitempty"`
	SSHKeyPassphrases map[string]string `json:"ssh_key_passphrases,omitempty"`
	// SSHAcceptHost is honored only on an attach (human) connection; a
	// control (AI) connection can never accept a new host key, per
	// internal/transport/ssh's HumanToken model.
	SSHAcceptHost bool `json:"ssh_accept_host,omitempty"`
}

func (d *dispatcher) sessionStart(role ipc.Role, payload json.RawMessage) (any, *v1.ProtocolError) {
	var p sessionStartPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badPayload(err)
		}
	}
	if p.Board == "" {
		p.Board = d.board
	}

	prof, err := profile.Load(d.profileDir, p.Board)
	if err != nil {
		return nil, badPayload(fmt.Errorf("loading profile %q: %w", p.Board, err))
	}

	kind := p.Transport
	switch {
	case kind == "" && prof.UART != nil && prof.SSH == nil:
		kind = "uart"
	case kind == "" && prof.SSH != nil && prof.UART == nil:
		kind = "ssh"
	case kind == "":
		return nil, badPayload(fmt.Errorf("profile %q configures both UART and SSH; specify which transport to start", p.Board))
	}

	switch kind {
	case "uart":
		return d.startUARTSession(prof)
	case "ssh":
		return d.startSSHSession(role, prof, p)
	default:
		return nil, badPayload(fmt.Errorf("unknown transport %q", kind))
	}
}

func (d *dispatcher) startUARTSession(prof profile.Profile) (any, *v1.ProtocolError) {
	if prof.UART == nil {
		return nil, badPayload(fmt.Errorf("profile %q has no UART configuration", prof.Name))
	}

	opener := func() (transport.Stream, error) {
		ports, err := d.doListPorts()
		if err != nil {
			return nil, err
		}
		m := serial.Match(prof.UART.Identity, ports)
		if m.NeedsHumanSelection || m.Port == nil {
			return nil, gateway.ErrHumanSelectionRequired
		}
		return d.doOpenPort(*m.Port, prof.UART.Line)
	}

	stream, err := opener()
	if err != nil {
		return nil, internalErr(err)
	}
	if err := d.coord.StartUART(stream, d.loginCfg, opener); err != nil {
		stream.Close()
		return nil, internalErr(err)
	}

	return map[string]string{"session_id": d.coord.SessionID(), "state": string(d.coord.State())}, nil
}

func (d *dispatcher) startSSHSession(role ipc.Role, prof profile.Profile, p sessionStartPayload) (any, *v1.ProtocolError) {
	if prof.SSH == nil {
		return nil, badPayload(fmt.Errorf("profile %q has no SSH configuration", prof.Name))
	}

	secrets := sshtransport.HumanSecrets{}
	if p.SSHPassword != "" {
		secrets.Password = []byte(p.SSHPassword)
	}
	if len(p.SSHKeyPassphrases) > 0 {
		secrets.KeyPassphrases = make(map[string][]byte, len(p.SSHKeyPassphrases))
		for path, pass := range p.SSHKeyPassphrases {
			secrets.KeyPassphrases[path] = []byte(pass)
		}
	}

	auth := sshtransport.HumanAuth{Secrets: secrets}
	if role == ipc.RoleAttach && p.SSHAcceptHost {
		auth.AcceptHost = true
		auth.Token = sshtransport.GrantHumanToken()
	}

	opener := func() (transport.Stream, error) {
		return d.doDialSSH(prof.SSH, auth)
	}

	stream, err := opener()
	if err != nil {
		return nil, internalErr(err)
	}
	if err := d.coord.StartSSH(stream, opener); err != nil {
		stream.Close()
		return nil, internalErr(err)
	}

	return map[string]string{"session_id": d.coord.SessionID(), "state": string(d.coord.State())}, nil
}

func (d *dispatcher) sessionStatus() (any, *v1.ProtocolError) {
	return map[string]string{"session_id": d.coord.SessionID(), "state": string(d.coord.State())}, nil
}

func (d *dispatcher) sessionEnd() (any, *v1.ProtocolError) {
	if err := d.coord.EndSession(); err != nil {
		return nil, internalErr(err)
	}
	return map[string]string{"state": string(d.coord.State())}, nil
}

type outputReadPayload struct {
	After uint64 `json:"after"`
	Max   int    `json:"max"`
}

func (d *dispatcher) outputRead(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p outputReadPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badPayload(err)
		}
	}
	if p.Max <= 0 {
		p.Max = 64 * 1024
	}
	chunk := d.coord.ReadAfter(p.After, p.Max)
	return map[string]any{
		"start": chunk.Start,
		"next":  chunk.Next,
		"data":  chunk.Data,
		"gap":   chunk.Gap,
	}, nil
}

type commandProposePayload struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	Purpose   string `json:"purpose"`
	TimeoutMS int64  `json:"timeout_ms"`
}

func (d *dispatcher) commandPropose(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p commandProposePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, badPayload(err)
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	prop, err := d.coord.Propose(p.SessionID, p.Text, p.Purpose, timeout)
	if err != nil {
		return nil, badPayload(err)
	}
	return prop, nil
}

type commandListPayload struct {
	SessionID string `json:"session_id"`
}

func (d *dispatcher) commandList(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p commandListPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badPayload(err)
		}
	}
	if p.SessionID == "" {
		p.SessionID = d.coord.SessionID()
	}
	return map[string]any{"pending": d.coord.PendingForSession(p.SessionID)}, nil
}

type commandIDPayload struct {
	ProposalID string `json:"proposal_id"`
}

func (d *dispatcher) commandApprove(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p commandIDPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, badPayload(err)
	}
	tx, err := d.coord.Approve(p.ProposalID)
	if err != nil {
		return nil, badPayload(err)
	}
	if d.aw != nil {
		_, _ = d.aw.Append(audit.Record{Kind: "transaction", Detail: tx.ID})
	}
	if d.open != nil {
		_ = d.open.add(tx.ID)
	}
	return tx, nil
}

func (d *dispatcher) commandReject(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p commandIDPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, badPayload(err)
	}
	if err := d.coord.Reject(p.ProposalID); err != nil {
		return nil, badPayload(err)
	}
	return map[string]string{"proposal_id": p.ProposalID, "state": "rejected"}, nil
}

type commandEditPayload struct {
	ProposalID string `json:"proposal_id"`
	Text       string `json:"text"`
	Purpose    string `json:"purpose"`
}

func (d *dispatcher) commandEdit(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p commandEditPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, badPayload(err)
	}
	next, err := d.coord.Edit(p.ProposalID, p.Text, p.Purpose)
	if err != nil {
		return nil, badPayload(err)
	}
	return next, nil
}

type transportWritePayload struct {
	Data []byte `json:"data"`
}

// ctrlC is the single byte the human's Ctrl-C keystroke sends.
const ctrlC = 0x03

func (d *dispatcher) transportWrite(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p transportWritePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, badPayload(err)
	}

	// While an approved AI transaction is executing, normal human text
	// input is paused to preserve output attribution; the human
	// retains Ctrl-C and (via a separate takeover operation) forced
	// takeover.
	isCtrlC := len(p.Data) == 1 && p.Data[0] == ctrlC
	if d.coord.State() == session.RunningCommand && !isCtrlC {
		return nil, &v1.ProtocolError{
			Code:    v1.ErrCodePermissionDenied,
			Message: "human input is paused while an approved transaction is executing; use takeover to regain control",
		}
	}

	n, err := d.coord.WriteHuman(p.Data)
	if err != nil {
		return nil, internalErr(err)
	}
	return map[string]int{"written": n}, nil
}

func (d *dispatcher) takeover() (any, *v1.ProtocolError) {
	if err := d.coord.Takeover(); err != nil {
		return nil, internalErr(err)
	}
	return map[string]string{"state": string(d.coord.State())}, nil
}

func (d *dispatcher) secretBegin() (any, *v1.ProtocolError) {
	d.coord.BeginSecret()
	return map[string]string{"state": string(d.coord.State())}, nil
}

func (d *dispatcher) secretDone() (any, *v1.ProtocolError) {
	d.coord.EndSecret()
	return map[string]string{"state": string(d.coord.State())}, nil
}

func (d *dispatcher) retryUART() (any, *v1.ProtocolError) {
	if err := d.coord.RetryUART(); err != nil {
		return nil, internalErr(err)
	}
	return map[string]string{"session_id": d.coord.SessionID(), "state": string(d.coord.State())}, nil
}

func (d *dispatcher) retrySSH() (any, *v1.ProtocolError) {
	if err := d.coord.RetrySSH(); err != nil {
		return nil, internalErr(err)
	}
	return map[string]string{"session_id": d.coord.SessionID(), "state": string(d.coord.State())}, nil
}

type recordsExportPayload struct {
	AfterTranscript uint64 `json:"after_transcript"`
	AfterAudit      uint64 `json:"after_audit"`
}

func (d *dispatcher) recordsExport(payload json.RawMessage) (any, *v1.ProtocolError) {
	var p recordsExportPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badPayload(err)
		}
	}

	transcriptRecords, err := d.tw.ReadAll()
	if err != nil {
		return nil, internalErr(err)
	}
	transcriptRecords = applyRetention(transcriptRecords, d.retentionLimit)

	auditRecords, err := d.aw.ReadAll()
	if err != nil {
		return nil, internalErr(err)
	}
	auditRecords = applyRetention(auditRecords, d.retentionLimit)

	var filteredTranscript []transcript.Record
	for _, r := range transcriptRecords {
		if r.Seq > p.AfterTranscript {
			filteredTranscript = append(filteredTranscript, r)
		}
	}
	var filteredAudit []audit.Record
	for _, r := range auditRecords {
		if r.Seq > p.AfterAudit {
			filteredAudit = append(filteredAudit, r)
		}
	}

	return map[string]any{
		"transcript": filteredTranscript,
		"audit":      filteredAudit,
	}, nil
}

// applyRetention keeps only the most recent limit records, so
// records.export never returns an unbounded amount of durable history
// in one call.
func applyRetention[T any](records []T, limit int) []T {
	if limit <= 0 || len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}
