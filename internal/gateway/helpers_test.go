package gateway

import "regexp"

func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
