package policy

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type Decision struct {
	Allowed bool   `json:"allowed"`
	Rule    string `json:"rule"`
	Reason  string `json:"reason"`
}

type validator func([]string) Decision

type Policy struct {
	commands map[string]validator
}

func deny(rule, reason string) Decision {
	return Decision{Allowed: false, Rule: rule, Reason: reason}
}

func allow(rule string) Decision {
	return Decision{Allowed: true, Rule: rule, Reason: "command is explicitly read-only"}
}

func (p *Policy) Evaluate(text string) Decision {
	return evaluateShellLine(text, walkOptions{
		allowRedirection: false,
		classify:         p.evaluateArgv,
	})
}

func literalArgv(words []*syntax.Word) ([]string, bool) {
	argv := make([]string, 0, len(words))
	for _, word := range words {
		var value strings.Builder
		for _, part := range word.Parts {
			switch part := part.(type) {
			case *syntax.Lit:
				value.WriteString(part.Value)
			case *syntax.SglQuoted:
				value.WriteString(part.Value)
			case *syntax.DblQuoted:
				for _, quoted := range part.Parts {
					lit, ok := quoted.(*syntax.Lit)
					if !ok {
						return nil, false
					}
					value.WriteString(lit.Value)
				}
			default:
				return nil, false
			}
		}
		argv = append(argv, value.String())
	}
	return argv, true
}

func (p *Policy) evaluateArgv(argv []string) Decision {
	if len(argv) == 0 {
		return deny("command.empty", "empty commands are not allowed")
	}
	validate, ok := p.commands[argv[0]]
	if !ok {
		return deny("command.unknown", fmt.Sprintf("executable %q is not allowlisted", argv[0]))
	}
	return validate(argv)
}
