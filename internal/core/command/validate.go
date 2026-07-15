package command

import "errors"

// Sentinel errors returned by ValidateManaged. Callers may use
// errors.Is to distinguish rejection reasons.
var (
	ErrContainsNUL      = errors.New("command: contains a NUL byte")
	ErrMultiline        = errors.New("command: must be one physical line")
	ErrUnbalancedQuotes = errors.New("command: unbalanced shell quotes")
	ErrHeredocOperator  = errors.New("command: heredoc operator is not allowed")
	ErrBackgroundList   = errors.New("command: unquoted background/async operator is not allowed")
)

// ValidateManaged applies the first-release managed-command shell
// contract: one physical line, no NUL, balanced quotes, no here-document
// operator, and no unquoted asynchronous/background list. Sequential
// and conditional lists using ";", "&&", and "||" are allowed. This is
// a restricted lexer, not a full shell grammar: it tracks quoting and
// escaping only well enough to reject the forbidden forms, because a
// completion marker appended on the same shell line would otherwise
// describe job launch rather than job finish for a backgrounded or
// heredoc-shaped command.
func ValidateManaged(text string) error {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case 0:
			return ErrContainsNUL
		case '\n', '\r':
			return ErrMultiline
		}
	}

	var quote byte // 0, '\'', or '"'
	for i := 0; i < len(text); i++ {
		c := text[i]

		if quote == '\'' {
			if c == '\'' {
				quote = 0
			}
			continue
		}
		if quote == '"' {
			if c == '\\' && i+1 < len(text) {
				i++
				continue
			}
			if c == '"' {
				quote = 0
			}
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
		case '\\':
			if i+1 < len(text) {
				i++
			}
		case '&':
			if i+1 < len(text) && text[i+1] == '&' {
				i++
				continue
			}
			return ErrBackgroundList
		case '<':
			if i+1 < len(text) && text[i+1] == '<' {
				return ErrHeredocOperator
			}
		}
	}
	if quote != 0 {
		return ErrUnbalancedQuotes
	}
	return nil
}
