package policy

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// walkOptions parameterizes the one shell-line contract shared by every
// policy mode: exactly one physical line, no NUL, balanced quotes, no
// heredoc, and no command/process substitution, environment assignment,
// negation, coprocess, or disowned/background statement. allowRedirection
// and classify are the only two axes on which modes differ.
type walkOptions struct {
	allowRedirection bool
	classify         func(argv []string) Decision
}

// evaluateShellLine parses text as one shell file and walks it under opts,
// denying any construct opts does not explicitly permit before classify
// ever sees an argv.
func evaluateShellLine(text string, opts walkOptions) Decision {
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
			if n.Negated || n.Background || n.Coprocess || n.Disown {
				decision = deny("syntax.statement", "negation, background, and coprocess syntax are not allowed")
				return false
			}
			if !opts.allowRedirection && len(n.Redirs) != 0 {
				decision = deny("syntax.statement", "redirection, background, negation, and coprocess syntax are not allowed")
				return false
			}
			return true
		case *syntax.Redirect:
			if !opts.allowRedirection {
				decision = deny("syntax.unsupported", fmt.Sprintf("unsupported shell syntax %T", n))
				return false
			}
			if n.Op == syntax.Hdoc || n.Op == syntax.DashHdoc {
				decision = deny("syntax.heredoc", "here-document redirection is not allowed")
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
			decision = opts.classify(argv)
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
