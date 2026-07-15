package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
)

// openSet durably tracks transactions that have been approved but have
// not yet produced a result, mapping each transaction ID to the session
// it ran under so the terminal audit record can be attributed. If
// gatewayd crashes with entries still in the set, the next startup's
// recoverIncompleteTransactions finalizes each one as daemon-restarted
// rather than leaving it open forever.
type openSet struct {
	mu   sync.Mutex
	path string
	ids  map[string]string // transaction ID -> session ID
}

// loadOpenSet loads path, treating a missing file as an empty set. It
// also accepts the legacy on-disk format (a bare JSON array of
// transaction IDs), loading those entries with an unknown session.
func loadOpenSet(path string) (*openSet, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &openSet{path: path, ids: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	if err := json.Unmarshal(data, &ids); err != nil {
		var legacy []string
		if legacyErr := json.Unmarshal(data, &legacy); legacyErr != nil {
			return nil, err
		}
		for _, id := range legacy {
			ids[id] = ""
		}
	}
	return &openSet{path: path, ids: ids}, nil
}

func (s *openSet) add(id, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = session
	return s.saveLocked()
}

func (s *openSet) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.ids))
	for id := range s.ids {
		out = append(out, id)
	}
	return out
}

// finalize durably writes the terminal "result" audit record for id and
// clears it from the set, exactly once: the open-set entry is the claim,
// so whichever of the dispatcher and the reconciler gets here first
// writes the record and the loser sees ok=false and writes nothing. The
// audit append happens before the entry is removed, so a failure leaves
// the entry in place to be retried on the next reconciler tick.
func (s *openSet) finalize(aw *audit.Writer, board, id, detail string) (ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, open := s.ids[id]
	if !open {
		return false, nil
	}
	if aw != nil {
		if _, err := aw.Append(audit.Record{Board: board, Session: session, Kind: "result", Detail: detail}); err != nil {
			return false, err
		}
	}
	delete(s.ids, id)
	return true, s.saveLocked()
}

func (s *openSet) saveLocked() error {
	data, err := json.Marshal(s.ids)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".open-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// resultDetail is the one shared format for a terminal "result" audit
// line, used by every finalization path so the same transaction can
// never be recorded two different ways.
func resultDetail(txID string, res *command.Result) string {
	detail := fmt.Sprintf("transaction=%s status=%s", txID, res.Status)
	if res.ExitCode != nil {
		detail += fmt.Sprintf(" exit_code=%d", *res.ExitCode)
	}
	return detail
}

// openSetReconcileInterval bounds how long a transaction that finishes
// normally (completed, timed out, disconnected, target-rebooted,
// interrupted by the human) can remain in the open set before its real
// result is durably recorded and it is cleared. Short enough that even
// a crash shortly after completion still finalizes it correctly: a
// restart only ever marks daemon-restarted whatever is still in the
// open set, so anything this reconciler has already flushed out is
// safe from being relabeled.
const openSetReconcileInterval = 50 * time.Millisecond

// runOpenSetReconciler periodically flushes every open transaction
// that already has a result to the durable audit log and clears it
// from open, so it stops being a daemon-restarted candidate the moment
// its real outcome is known -- "preserve complete results" is a
// property of this reconciler having run, not of recoverIncompleteTransactions
// (which only ever sees whatever is still open at the next startup).
func runOpenSetReconciler(coord *gateway.Coordinator, open *openSet, aw *audit.Writer, board string, stop <-chan struct{}) {
	ticker := time.NewTicker(openSetReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			reconcileOpenTransactions(coord, open, aw, board)
		}
	}
}

func reconcileOpenTransactions(coord *gateway.Coordinator, open *openSet, aw *audit.Writer, board string) {
	for _, txID := range open.list() {
		res, err := coord.Result(txID)
		if err != nil {
			continue // still running; no result recorded yet
		}
		// A finalize error leaves the entry open to retry next tick
		// rather than losing the ID's tracking.
		_, _ = open.finalize(aw, board, txID, resultDetail(txID, res))
	}
}

// recoverIncompleteTransactions finalizes every transaction ID still
// in open as daemon-restarted and clears it from the set. It runs once
// at startup, before serving any connection, per the design spec:
// daemon restart or crash invalidates every pending proposal and marks
// every approved or running transaction as daemon-restarted, and
// neither is restored or replayed.
func recoverIncompleteTransactions(open *openSet, aw *audit.Writer, board string) error {
	for _, txID := range open.list() {
		detail := fmt.Sprintf("transaction=%s status=daemon-restarted", txID)
		if _, err := open.finalize(aw, board, txID, detail); err != nil {
			return err
		}
	}
	return nil
}
