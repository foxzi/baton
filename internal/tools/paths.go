package tools

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// checkWorkspacePath holds a workspace-relative path to what any of these
// tools may be asked to touch: no leading slash, no .. segment, no control
// character, at most 1024 bytes, and not on the deny list of spec section
// 7.3. This is the one place the rule is written; git.go and fs.go both
// call it instead of keeping their own copy, so the two cannot drift apart.
func checkWorkspacePath(name, value string, deny []string) (string, error) {
	switch {
	case value == "":
		return "", fmt.Errorf("argument %q is empty", name)
	case len(value) > 1024:
		return "", fmt.Errorf("argument %q is longer than 1024 bytes", name)
	case strings.HasPrefix(value, "/"):
		return "", fmt.Errorf("argument %q must not be an absolute path", name)
	case strings.ContainsAny(value, "\x00\n"):
		return "", fmt.Errorf("argument %q must be a single line", name)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("argument %q must not contain ..", name)
		}
	}

	cleaned := path.Clean(value)
	if denied(deny, cleaned) {
		return "", fmt.Errorf("path %q is denied", cleaned)
	}
	return cleaned, nil
}

// digitsPattern is what a numeric argument of these tools must look like:
// decimal digits, nothing else, since every argument arrives as a string
// (spec section 7.4). It keeps a value such as "-1" or "1e9" away from
// strconv, both in fs.grep's max_matches and in git.log's max_count.
var digitsPattern = regexp.MustCompile(`\A[0-9]+\z`)
