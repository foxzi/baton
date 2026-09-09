package values_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// revealWhitelist are the package directories allowed to read plaintext
// secrets, per docs/ru/spec.md, section 2 (including internal/provider,
// which normalizes messages for agent adapters), plus internal/values
// itself, which defines the type.
var revealWhitelist = []string{
	"internal/values",
	"internal/secrets",
	"internal/httpx",
	"internal/gateway",
	"internal/notify",
	"internal/engine",
	"internal/provider",
	// internal/tools puts a command's declared secrets into the child
	// process environment (spec section 7.5).
	"internal/tools",
}

// TestRevealCallersAreWhitelisted is the import restriction that section 5.3
// of the specification asks for: it fails when a package outside the
// whitelist reads the contents of a secret.
//
// Test files are skipped: they do not ship, and they legitimately construct
// and inspect secrets to check redaction.
func TestRevealCallersAreWhitelisted(t *testing.T) {
	root := repoRoot(t)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name != "." && (strings.HasPrefix(name, ".") || name == "docs") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(source), ".Reveal()") {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !whitelisted(filepath.ToSlash(filepath.Dir(relative))) {
			offenders = append(offenders, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}

	for _, offender := range offenders {
		t.Errorf("%s reads plaintext secrets; only %s may call Secret.Reveal", offender, strings.Join(revealWhitelist, ", "))
	}
}

func whitelisted(dir string) bool {
	for _, allowed := range revealWhitelist {
		if dir == allowed || strings.HasPrefix(dir, allowed+"/") {
			return true
		}
	}
	return false
}

// repoRoot returns the module root, found by walking up to go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}
