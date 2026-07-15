package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePolicyFile(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "board.json")
	if err := os.WriteFile(name, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLoadFileAddsExactRulesWithoutOverridingDenials(t *testing.T) {
	name := writePolicyFile(t, 0o600, `{
		"allow":[{"executable":"opsis-inspect","args":["status"]},{"executable":"ps","args":["--write"]}],
		"deny":[{"executable":"ps","args":["-ef"]}]
	}`)
	base := Common()
	got, err := LoadFile(name, base)
	if err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"opsis-inspect status", "ps -e"} {
		if decision := got.Evaluate(command); !decision.Allowed {
			t.Errorf("Evaluate(%q) = %+v, want allowed", command, decision)
		}
	}
	for _, command := range []string{
		"opsis-inspect", "opsis-inspect status extra", "opsis-inspect status; id",
		"sh status", "ps -ef", "ps --write",
	} {
		if decision := got.Evaluate(command); decision.Allowed {
			t.Errorf("Evaluate(%q) = %+v, want denied", command, decision)
		}
	}
	if decision := base.Evaluate("ps -ef"); !decision.Allowed {
		t.Errorf("base policy was mutated: %+v", decision)
	}
}

func TestLoadFileRejectsInvalidPolicies(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		body string
		want string
	}{
		{"group readable", 0o640, `{}`, "permissions"},
		{"world readable", 0o604, `{}`, "permissions"},
		{"malformed JSON", 0o600, `{`, "JSON"},
		{"unknown field", 0o600, `{"extra":[]}`, "unknown field"},
		{"wildcard executable", 0o600, `{"allow":[{"executable":"opsis-*","args":["status"]}]}`, "executable"},
		{"empty argv", 0o600, `{"allow":[{"executable":"","args":[]}]}`, "executable"},
		{"contradictory duplicate", 0o600, `{"allow":[{"executable":"opsis-inspect","args":["status"]}],"deny":[{"executable":"opsis-inspect","args":["status"]}]}`, "contradict"},
		{"common forbidden executable", 0o600, `{"allow":[{"executable":"sh","args":["-c","id"]}]}`, "forbidden"},
		{"mutator", 0o600, `{"allow":[{"executable":"rm","args":["--version"]}]}`, "forbidden"},
		{"syntax executable", 0o600, `{"allow":[{"executable":"foo;id","args":["status"]}]}`, "executable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFile(writePolicyFile(t, tt.mode, tt.body), Common())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("LoadFile() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}
