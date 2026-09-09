package packs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// EnvPackCache overrides the directory git pack sources are checked out into.
const EnvPackCache = "BATON_PACKS_CACHE"

// cloneTimeout caps a single checkout of a pack source.
const cloneTimeout = 2 * time.Minute

// Source says where a pack comes from (docs/ru/spec.md, section 7.4.1).
type Source struct {
	// From is a local directory, absolute or relative to BaseDir, or a git
	// source with a version pin: github.com/org/baton-apis@v1.3.0.
	From string

	// Pack is the pack name inside the source.
	Pack string

	// SHA256 pins the content of the pack and is required for git sources.
	// It is the hex digest described on Digest.
	SHA256 string

	// BaseDir resolves relative sources, and is normally the directory of
	// the scenario.
	BaseDir string

	// CacheDir holds the checkouts of git sources. Empty means the default:
	// BATON_PACKS_CACHE, or <user cache>/baton/packs.
	CacheDir string
}

// versionPattern is the shape of a version pin. Tags and branches may carry
// slashes, but a pin must not look like a git option or escape the cache.
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// resolve returns the directory the pack is read from, checking out a git
// source if it is not in the cache yet. Pinned reports a git source, which
// must carry a checksum.
func (s Source) resolve() (dir string, pinned bool, err error) {
	repo, version, err := parseSource(s.From)
	if err != nil {
		return "", false, err
	}
	if version == "" {
		local := repo
		if !filepath.IsAbs(local) {
			local = filepath.Join(s.BaseDir, local)
		}
		return local, false, nil
	}
	dir, err = s.checkout(repo, version)
	return dir, true, err
}

// parseSource splits a pack source into a repository and a version pin. A
// source without "@" is a local directory; a source that names a host or a
// URL must carry a pin.
func parseSource(from string) (repo, version string, err error) {
	if from == "" {
		return "", "", fmt.Errorf("from is required: a local directory or <source>@<tag>")
	}
	if idx := strings.LastIndex(from, "@"); idx > 0 {
		repo, version = from[:idx], from[idx+1:]
		if !versionPattern.MatchString(version) || strings.Contains(version, "..") {
			return "", "", fmt.Errorf("from %q: %q is not a version pin", from, version)
		}
		return repo, version, nil
	}
	if remoteLike(from) {
		return "", "", fmt.Errorf("from %q: a version pin is required, use %s@<tag>", from, from)
	}
	return from, "", nil
}

// remoteLike reports a source that cannot be a local directory: a URL, an
// scp-style git address, or a path whose first segment is a host name.
func remoteLike(from string) bool {
	switch {
	case strings.Contains(from, "://"), strings.HasPrefix(from, "git@"):
		return true
	case strings.HasPrefix(from, "."), filepath.IsAbs(from):
		return false
	}
	head, _, _ := strings.Cut(from, "/")
	return strings.Contains(head, ".")
}

// checkout returns the cached checkout of repo at version, cloning it on the
// first use. A checkout is never refreshed: the pin names an immutable tag,
// so a present directory is the answer.
func (s Source) checkout(repo, version string) (string, error) {
	root := s.CacheDir
	if root == "" {
		root = defaultPackCache()
	}
	if root == "" {
		return "", fmt.Errorf("from %s@%s: no cache directory, set %s", repo, version, EnvPackCache)
	}
	dir := filepath.Join(root, cacheSlug(repo, version))
	if occupied(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("pack cache: %w", err)
	}
	staging, err := os.MkdirTemp(root, ".clone-")
	if err != nil {
		return "", fmt.Errorf("pack cache: %w", err)
	}
	defer os.RemoveAll(staging)

	target := filepath.Join(staging, "pack")
	if err := gitClone(cloneURL(repo, s.BaseDir), version, target); err != nil {
		return "", fmt.Errorf("from %s@%s: %w", repo, version, err)
	}
	if err := os.Rename(target, dir); err != nil {
		// Another run may have finished the same checkout first, which is
		// as good as ours.
		if occupied(dir) {
			return dir, nil
		}
		return "", fmt.Errorf("pack cache: %w", err)
	}
	return dir, nil
}

// gitClone checks out one version of a repository, shallow and without ever
// asking for credentials interactively.
func gitClone(url, version, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, "git", "clone", "--quiet", "--depth", "1",
		"--single-branch", "--branch", version, "--", url, target)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("git clone timed out after %s", cloneTimeout)
	}
	return fmt.Errorf("git clone failed: %s", messageTail(output, err))
}

// messageTail is the last line git said, which carries the reason, or the
// process error when git said nothing.
func messageTail(output []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return err.Error()
}

// occupied reports a directory that already holds a checkout.
func occupied(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// cloneURL is the address git is given. A bare host path is https; anything
// else is passed through, and a relative path is resolved for git's benefit.
func cloneURL(repo, baseDir string) string {
	switch {
	case strings.Contains(repo, "://"), strings.HasPrefix(repo, "git@"):
		return repo
	case filepath.IsAbs(repo):
		return repo
	case strings.HasPrefix(repo, "."):
		return filepath.Join(baseDir, repo)
	default:
		return "https://" + repo
	}
}

// cacheSlug names the checkout directory of a source. It keeps the source
// readable and cannot escape the cache root.
func cacheSlug(repo, version string) string {
	slug := strings.TrimSuffix(repo, ".git") + "@" + version
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_', r == '@':
			return r
		default:
			return '_'
		}
	}, slug)
	// A slug is one path element: it must not be a dot name either.
	return strings.TrimLeft(strings.ReplaceAll(mapped, "..", "__"), ".")
}

// defaultPackCache is where checkouts live when the scenario says nothing.
func defaultPackCache() string {
	if dir := os.Getenv(EnvPackCache); dir != "" {
		return dir
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "baton", "packs")
}

// Digest is the checksum a scenario pins with sha256. For a pack that is a
// single file it is the sha256 of that file. For a pack that is a directory
// it is the sha256 of the digests of the files inside, so that the queries
// and examples shipped beside the pack are covered too.
func Digest(location string) (string, error) {
	info, err := os.Stat(location)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fileDigest(location)
	}
	sum := sha256.New()
	err = filepath.WalkDir(location, func(entry string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(location, entry)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if relative != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		digest, err := fileDigest(entry)
		if err != nil {
			return err
		}
		fmt.Fprintf(sum, "%s\x00%s\n", path.Clean(filepath.ToSlash(relative)), digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// fileDigest is the sha256 of one file, in hex.
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// checkDigest compares the content of a pack with the pin from the scenario.
// A git source without a pin is an error; a local directory may go without.
func checkDigest(got, location, want string, pinned bool) error {
	want = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(want)), "sha256:")
	if want == "" {
		if pinned {
			return fmt.Errorf("sha256 is required for a git source")
		}
		return nil
	}
	if got != want {
		return fmt.Errorf("sha256 mismatch: %s has %s, the scenario pins %s", location, got, want)
	}
	return nil
}
