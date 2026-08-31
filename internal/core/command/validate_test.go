package command

import "testing"

func TestValidateManaged(t *testing.T) {
	tests := []struct {
		text string
		ok   bool
	}{
		{"uname -a", true},
		{"cd /tmp && pwd", true},
		{"echo a; echo b", true},
		{"test -f /tmp/x || echo missing", true},
		{"echo 'a & b'", true},
		{"echo \"a && b\"", true},
		{"pwd\nid", false},
		{"printf 'oops", false},
		{"cat <<EOF", false},
		{"cat <<-EOF", false},
		{"sleep 1 &", false},
		{"sleep 1 & echo hi", false},
		{"ls /nonexistent 2>&1", true},
		{"echo warn >&2", true},
		{"cat /var/log/messages 2>&1 | tail -3", true},
		{"sleep 1 2>&1 &", false},
		{"sleep 1 2>&1 & echo hi", false},
		{"ls &> /tmp/all", false},
		{"printf 'a\x00b'", false},
	}
	for _, tt := range tests {
		err := ValidateManaged(tt.text)
		if (err == nil) != tt.ok {
			t.Errorf("ValidateManaged(%q) = %v, want ok=%v", tt.text, err, tt.ok)
		}
	}
}
