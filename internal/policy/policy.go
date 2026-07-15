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
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "diagnostic")
	if err != nil {
		return deny("syntax.parse", err.Error())
	}
	if len(file.Stmts) == 0 {
		return deny("syntax.empty", "empty shell input is not allowed")
	}

	decision := allow("common")
	callCount := 0
	fileRuleUsed := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil || !decision.Allowed {
			return false
		}
		switch n := node.(type) {
		case *syntax.File, *syntax.Comment, *syntax.Word, *syntax.Lit, *syntax.SglQuoted:
			return true
		case *syntax.Stmt:
			if n.Negated || n.Background || n.Coprocess || n.Disown || len(n.Redirs) != 0 {
				decision = deny("syntax.statement", "redirection, background, negation, and coprocess syntax are not allowed")
				return false
			}
			return true
		case *syntax.BinaryCmd:
			if n.Op != syntax.AndStmt && n.Op != syntax.OrStmt && n.Op != syntax.Pipe {
				decision = deny("syntax.operator", "only ;, &&, ||, and pipelines are allowed")
				return false
			}
			return true
		case *syntax.DblQuoted:
			for _, part := range n.Parts {
				if _, ok := part.(*syntax.Lit); !ok {
					decision = deny("syntax.expansion", "shell expansions are not allowed")
					return false
				}
			}
			return true
		case *syntax.CallExpr:
			callCount++
			if len(n.Assigns) != 0 {
				decision = deny("syntax.assignment", "shell assignments are not allowed")
				return false
			}
			argv, ok := literalArgv(n.Args)
			if !ok {
				decision = deny("syntax.expansion", "command arguments must be literal")
				return false
			}
			decision = p.evaluateArgv(argv)
			if decision.Allowed && decision.Rule == "file.allow" {
				fileRuleUsed = true
			}
			return decision.Allowed
		default:
			decision = deny("syntax.unsupported", fmt.Sprintf("unsupported shell syntax %T", node))
			return false
		}
	})
	if decision.Allowed && callCount == 0 {
		return deny("syntax.no-command", "input contains no command")
	}
	if decision.Allowed && fileRuleUsed && callCount != 1 {
		return deny("file.exact", "board policy rules must be used as a single exact command")
	}
	return decision
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
	if len(argv) == 2 && (argv[1] == "--help" || argv[1] == "--version") {
		return allow("command." + argv[0] + ".information")
	}
	return validate(argv)
}
