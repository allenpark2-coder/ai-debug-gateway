package policy

import (
	"os"
	"strings"
	"testing"
)

func TestLoadUnsafeShellFileRequiresRiskAccepted(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{}`},
		{"false", `{"risk_accepted":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadUnsafeShellFile(writePolicyFile(t, 0o600, tt.body))
			if err == nil || !strings.Contains(err.Error(), "risk_accepted") {
				t.Fatalf("LoadUnsafeShellFile() error = %v, want risk_accepted error", err)
			}
		})
	}
}

func TestLoadUnsafeShellFileMergesDenials(t *testing.T) {
	name := writePolicyFile(t, 0o600, `{
		"risk_accepted": true,
		"deny_executables": ["reboot", "mkfs.ext4"],
		"deny_exact": [{"executable": "dd", "args": ["if=/dev/zero", "of=/dev/mtd0"]}]
	}`)
	p, err := LoadUnsafeShellFile(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"reboot", "mkfs.ext4 /dev/mtd0", "dd if=/dev/zero of=/dev/mtd0"} {
		if p.Evaluate(text).Allowed {
			t.Errorf("Evaluate(%q) = allowed, want denied", text)
		}
	}
	for _, text := range []string{"poweroff", "dd if=/dev/zero of=/tmp/scratch", "mount -o remount,rw /"} {
		if !p.Evaluate(text).Allowed {
			t.Errorf("Evaluate(%q) = denied, want allowed (denylist must not block unrelated commands)", text)
		}
	}
	// Hard denials are never affected by file content.
	if p.Evaluate("sh -c 'id'").Allowed {
		t.Fatal("hard denial bypassed by unsafe-shell file load")
	}
}

func TestLoadUnsafeShellFileRejectsInvalidFiles(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		body string
		want string
	}{
		{"group readable", 0o640, `{"risk_accepted":true}`, "permissions"},
		{"world readable", 0o604, `{"risk_accepted":true}`, "permissions"},
		{"malformed JSON", 0o600, `{`, "JSON"},
		{"unknown field", 0o600, `{"risk_accepted":true,"allow":[]}`, "unknown field"},
		{"wildcard executable", 0o600, `{"risk_accepted":true,"deny_executables":["opsis-*"]}`, "executable"},
		{"empty executable", 0o600, `{"risk_accepted":true,"deny_executables":[""]}`, "executable"},
		{"omitted exact args", 0o600, `{"risk_accepted":true,"deny_exact":[{"executable":"dd"}]}`, "args"},
		{"null exact args", 0o600, `{"risk_accepted":true,"deny_exact":[{"executable":"dd","args":null}]}`, "args"},
		{"syntax executable", 0o600, `{"risk_accepted":true,"deny_executables":["foo;id"]}`, "executable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadUnsafeShellFile(writePolicyFile(t, tt.mode, tt.body))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("LoadUnsafeShellFile() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadUnsafeShellFileHasNoAllowConcept(t *testing.T) {
	// The wire schema has no "allow" field at all -- this is the structural
	// guarantee that the file can only narrow, never widen, DenylistPolicy.
	name := writePolicyFile(t, 0o600, `{"risk_accepted":true,"allow":[{"executable":"sh","args":[]}]}`)
	if _, err := LoadUnsafeShellFile(name); err == nil {
		t.Fatal("an \"allow\" field must be rejected as an unknown field, not accepted")
	}
}
