package command

import "testing"

// FuzzValidateManaged proves ValidateManaged never panics on arbitrary
// input, and never accepts a NUL byte or an embedded CR/LF -- the two
// hard requirements a completion marker appended on the same shell
// line depends on (see validate.go).
func FuzzValidateManaged(f *testing.F) {
	seeds := []string{
		"uname -a", "cd /tmp && pwd", "echo a; echo b", "test -f /tmp/x || echo missing",
		"echo 'a & b'", "echo \"a && b\"", "pwd\nid", "printf 'oops", "cat <<EOF", "cat <<-EOF",
		"sleep 1 &", "sleep 1 & echo hi", "printf 'a\x00b'",
		"", "\r", "\x00", "a\\", "'", "\"", "\\", "&", "&&", "<", "<<", "<<-",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		err := ValidateManaged(text) // must never panic
		if err != nil {
			return
		}
		for i := 0; i < len(text); i++ {
			switch text[i] {
			case 0:
				t.Fatalf("ValidateManaged(%q) accepted a NUL byte", text)
			case '\n', '\r':
				t.Fatalf("ValidateManaged(%q) accepted a multiline command", text)
			}
		}
	})
}
