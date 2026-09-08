package packs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// demoPack is the pack the git source tests publish.
const demoPack = `pack: demo
version: 1
description: Fixture pack for git source tests
ops:
  ping:
    get: /ping
    readonly: true
`

// gitRepo publishes files in a repository tagged with tag and returns its
// path. The test is skipped when git is not installed.
func gitRepo(t *testing.T, tag string, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	commands := [][]string{
		{"init", "--quiet", "--initial-branch", "main"},
		{"config", "user.email", "packs@example.com"},
		{"config", "user.name", "packs"},
		{"add", "."},
		{"commit", "--quiet", "-m", "packs"},
		{"tag", tag},
	}
	for _, argv := range commands {
		command := exec.Command("git", argv...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(argv, " "), err, output)
		}
	}
	return dir
}

// mustDigest is the checksum of a path, for pinning in a test.
func mustDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := Digest(path)
	if err != nil {
		t.Fatalf("Digest(%s): %v", path, err)
	}
	return digest
}

// TestLoadFromGitSource checks that a pinned git source is checked out and
// read, and that the checkout lands in the cache directory.
func TestLoadFromGitSource(t *testing.T) {
	repo := gitRepo(t, "v1.0.0", map[string]string{"demo.yaml": demoPack})
	cache := t.TempDir()

	pack, err := Load(Source{
		From:     repo + "@v1.0.0",
		Pack:     "demo",
		SHA256:   mustDigest(t, filepath.Join(repo, "demo.yaml")),
		CacheDir: cache,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if pack.Pack != "demo" {
		t.Errorf("Load().Pack = %q, want demo", pack.Pack)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("ReadDir(cache): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache holds %d entries, want 1 checkout", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), "@v1.0.0") {
		t.Errorf("checkout = %q, want a name ending in the version pin", entries[0].Name())
	}
}

// TestLoadReusesTheCheckout checks that a second load reads the cache
// instead of talking to the source again.
func TestLoadReusesTheCheckout(t *testing.T) {
	repo := gitRepo(t, "v1", map[string]string{"demo.yaml": demoPack})
	cache := t.TempDir()
	src := Source{
		From:     repo + "@v1",
		Pack:     "demo",
		SHA256:   mustDigest(t, filepath.Join(repo, "demo.yaml")),
		CacheDir: cache,
	}
	if _, err := Load(src); err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("RemoveAll(repo): %v", err)
	}
	if _, err := Load(src); err != nil {
		t.Fatalf("second Load() error = %v, want the cached checkout to be enough", err)
	}
}

// TestLoadGitSourceNeedsAChecksum checks that a git source without sha256 is
// rejected.
func TestLoadGitSourceNeedsAChecksum(t *testing.T) {
	repo := gitRepo(t, "v1", map[string]string{"demo.yaml": demoPack})

	_, err := Load(Source{From: repo + "@v1", Pack: "demo", CacheDir: t.TempDir()})
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "sha256 is required") {
		t.Errorf("Load() error = %q, want mention that sha256 is required", err)
	}
}

// TestLoadChecksumMismatch checks that content which disagrees with the pin
// is rejected, and that the message shows both digests.
func TestLoadChecksumMismatch(t *testing.T) {
	repo := gitRepo(t, "v1", map[string]string{"demo.yaml": demoPack})
	want := strings.Repeat("ab", 32)

	_, err := Load(Source{From: repo + "@v1", Pack: "demo", SHA256: want, CacheDir: t.TempDir()})
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") || !strings.Contains(err.Error(), want) {
		t.Errorf("Load() error = %q, want a mismatch mentioning the pin", err)
	}
}

// TestLoadMissingTag checks that a pin git cannot resolve is reported with
// git's own message.
func TestLoadMissingTag(t *testing.T) {
	repo := gitRepo(t, "v1", map[string]string{"demo.yaml": demoPack})

	_, err := Load(Source{From: repo + "@v2", Pack: "demo", SHA256: "x", CacheDir: t.TempDir()})
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "git clone failed") {
		t.Errorf("Load() error = %q, want mention that the clone failed", err)
	}
}

// TestLoadLocalSourceChecksIt checks that a checksum on a local directory is
// verified as well, although it is not required there.
func TestLoadLocalSourceChecksIt(t *testing.T) {
	if _, err := Load(Source{From: "testdata", Pack: "demo", SHA256: strings.Repeat("cd", 32)}); err == nil {
		t.Fatalf("Load() error = nil, want a mismatch")
	}
	digest := mustDigest(t, filepath.Join("testdata", "demo.yaml"))
	if _, err := Load(Source{From: "testdata", Pack: "demo", SHA256: "sha256:" + strings.ToUpper(digest)}); err != nil {
		t.Errorf("Load() error = %v, want the pin to be accepted in any case and with a prefix", err)
	}
}

// TestDigestCoversTheWholePackDirectory checks that a pack laid out as a
// directory is pinned together with the files shipped beside it.
func TestDigestCoversTheWholePackDirectory(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(packDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}
	writeFile("pack.yaml", demoPack)
	writeFile("query.graphql", "query { viewer { login } }")

	before := mustDigest(t, packDir)
	if _, err := Load(Source{From: dir, Pack: "demo", SHA256: before}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	writeFile("query.graphql", "query { viewer { name } }")
	if after := mustDigest(t, packDir); after == before {
		t.Errorf("Digest is %s before and after a sibling file changed, want a different digest", after)
	}
	if _, err := Load(Source{From: dir, Pack: "demo", SHA256: before}); err == nil {
		t.Errorf("Load() error = nil, want a mismatch after a sibling file changed")
	}
}

// TestCacheSlugIsContained checks that a source cannot steer the checkout
// out of the cache root.
func TestCacheSlugIsContained(t *testing.T) {
	slug := cacheSlug("../../etc/ssh", "v1")
	if strings.ContainsAny(slug, "/\\") || strings.Contains(slug, "..") {
		t.Errorf("cacheSlug() = %q, want a single contained name", slug)
	}
}

// TestCloneURL checks the addresses git is handed for each source form.
func TestCloneURL(t *testing.T) {
	cases := []struct{ repo, base, want string }{
		{"github.com/org/baton-apis", "/scn", "https://github.com/org/baton-apis"},
		{"https://example.com/packs.git", "/scn", "https://example.com/packs.git"},
		{"git@github.com:org/packs.git", "/scn", "git@github.com:org/packs.git"},
		{"/srv/packs", "/scn", "/srv/packs"},
		{"./packs", "/scn", "/scn/packs"},
	}
	for _, c := range cases {
		if got := cloneURL(c.repo, c.base); got != c.want {
			t.Errorf("cloneURL(%q, %q) = %q, want %q", c.repo, c.base, got, c.want)
		}
	}
}

// TestDefaultPackCacheFollowsTheEnvironment checks the cache override.
func TestDefaultPackCacheFollowsTheEnvironment(t *testing.T) {
	t.Setenv(EnvPackCache, "/tmp/baton-packs")
	if got := defaultPackCache(); got != "/tmp/baton-packs" {
		t.Errorf("defaultPackCache() = %q, want the value of %s", got, EnvPackCache)
	}
}
