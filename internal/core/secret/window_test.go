package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/transcript"
)

func TestActiveTogglesWithBeginAndFinish(t *testing.T) {
	w := NewWindow()
	if w.Active() {
		t.Fatal("new window must not be active")
	}
	w.Begin()
	if !w.Active() {
		t.Fatal("expected active after Begin")
	}
	w.Finish()
	if w.Active() {
		t.Fatal("expected inactive after Finish")
	}
}

func TestFilterTargetPassesThroughWhenInactive(t *testing.T) {
	w := NewWindow()
	data := []byte("hello prompt")
	if got := w.FilterTarget(data); !bytes.Equal(got, data) {
		t.Fatalf("got %q, want unchanged %q", got, data)
	}
}

func randomSecret(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return []byte(hex.EncodeToString(b))
}

// TestSecretNeverReachesDurableStoresOrAIOutput submits a random secret
// while the window is active, feeds a fragmented echo of that exact
// secret back as if from a misconfigured remote terminal, and asserts
// the raw secret bytes never appear in the transcript store, the audit
// store, or the bytes that would be shown to an AI client.
func TestSecretNeverReachesDurableStoresOrAIOutput(t *testing.T) {
	dir := t.TempDir()
	tw := transcript.NewWriter(filepath.Join(dir, "transcript.jsonl"))
	aw := audit.NewWriter(filepath.Join(dir, "audit.jsonl"))

	secretBytes := randomSecret(t, 16)

	w := NewWindow()
	w.Begin()
	w.Submit(secretBytes)

	if _, err := aw.Append(audit.Record{Kind: "secret-begin", Detail: "profile login"}); err != nil {
		t.Fatal(err)
	}

	var aiOutput []byte
	// Fragment the echoed secret across several target reads, as a
	// misconfigured remote echo might.
	frags := [][]byte{secretBytes[:5], secretBytes[5:11], secretBytes[11:]}
	for _, frag := range frags {
		filtered := w.FilterTarget(frag)
		tw.Append(transcript.Record{Direction: "from-target", Source: "daemon", Data: filtered})
		aiOutput = append(aiOutput, filtered...)
	}

	w.Finish()
	if _, err := aw.Append(audit.Record{Kind: "secret-done", Detail: "profile login"}); err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(aiOutput, secretBytes) {
		t.Fatalf("secret leaked into AI output: %q", aiOutput)
	}

	records, err := tw.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if bytes.Contains(r.Data, secretBytes) {
			t.Fatalf("secret leaked into transcript store: %+v", r)
		}
	}

	auditRecords, err := aw.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range auditRecords {
		if bytes.Contains([]byte(r.Detail), secretBytes) {
			t.Fatalf("secret leaked into audit store: %+v", r)
		}
	}
}
