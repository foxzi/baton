package tools

import (
	"path"
	"strings"
)

// denied reports whether a workspace-relative slash path matches any pattern
// in the deny list of spec section 7.3. Every ancestor of the path is
// checked too, so that a pattern written for a directory (vendor/**) also
// covers a file two levels under it without the caller having to walk the
// tree itself.
func denied(patterns []string, target string) bool {
	segments := strings.Split(target, "/")
	for _, pattern := range patterns {
		for i := 1; i <= len(segments); i++ {
			if matchGlob(pattern, strings.Join(segments[:i], "/")) {
				return true
			}
		}
	}
	return false
}

// matchGlob matches one pattern against a slash path: * matches within one
// segment, ** matches across segments, everything else is literal.
func matchGlob(pattern, target string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(target, "/"))
}

// MatchGlob is matchGlob for callers outside this package: the file step of
// a scenario matches its pattern the same way fs.glob does.
func MatchGlob(pattern, target string) bool { return matchGlob(pattern, target) }

// matchSegments matches a pattern's segments against a path's segments. A **
// segment matches zero or more path segments, which is what lets .git/**
// cover the directory itself (zero segments) as well as anything under it.
func matchSegments(pattern, target []string) bool {
	if len(pattern) == 0 {
		return len(target) == 0
	}
	if pattern[0] == "**" {
		if matchSegments(pattern[1:], target) {
			return true
		}
		if len(target) == 0 {
			return false
		}
		return matchSegments(pattern, target[1:])
	}
	if len(target) == 0 {
		return false
	}
	// path.Match never matches a "/", which is exactly right here: each
	// segment is matched on its own, and a * cannot cross into the next.
	ok, err := path.Match(pattern[0], target[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], target[1:])
}
