package main

import "strings"

// shellQuote quotes s for a POSIX shell if it contains characters a user
// would need to escape, so a path with a space or a quote in it can be
// pasted from a "next:" hint and run as-is.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()<>|;&~!#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
