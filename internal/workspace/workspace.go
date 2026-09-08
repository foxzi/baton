// Package workspace prepares the directory an agent works in (spec section
// 7.1). Preparation is about credentials, not about files: nothing is
// deleted, and the deny lists are enforced by the tools instead.
package workspace

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Info is what preparation found and what it had to change.
type Info struct {
	// Root is the repository root, or the directory itself when it is not a
	// repository.
	Root string

	// Git says whether Root is a git repository.
	Git bool

	// Config is the .git/config that was inspected, empty when there is
	// none.
	Config string

	// Changed names what was rewritten, in the form the run log can show:
	// the setting, never its value.
	Changed []string
}

// Prepare finds the repository root above dir and takes the credentials out
// of its git configuration: an agent that can run git must not be able to
// read the token that git pushes with.
func Prepare(dir string) (Info, error) {
	if dir == "" {
		return Info{}, errors.New("workspace: no directory")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return Info{}, fmt.Errorf("workspace: %w", err)
	}
	if info, err := os.Stat(root); err != nil {
		return Info{}, fmt.Errorf("workspace: %w", err)
	} else if !info.IsDir() {
		return Info{}, fmt.Errorf("workspace: %s is not a directory", root)
	}

	repo, ok := findRepo(root)
	if !ok {
		return Info{Root: root}, nil
	}

	config, err := configPath(repo)
	if err != nil {
		return Info{}, err
	}
	result := Info{Root: repo, Git: true}
	if config == "" {
		return result, nil
	}
	result.Config = config

	changed, err := sanitizeConfig(config)
	if err != nil {
		return Info{}, err
	}
	result.Changed = changed
	return result, nil
}

// findRepo walks up from dir looking for the working tree that contains it.
func findRepo(dir string) (string, bool) {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// configPath resolves the configuration file of the repository at root. A
// worktree keeps its .git as a file pointing elsewhere, and the remotes live
// in the common directory it shares with the main checkout.
func configPath(root string) (string, error) {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}

	gitDir := gitPath
	if !info.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return "", fmt.Errorf("workspace: %w", err)
		}
		pointer := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
		if pointer == "" {
			return "", fmt.Errorf("workspace: %s names no git directory", gitPath)
		}
		if !filepath.IsAbs(pointer) {
			pointer = filepath.Join(root, pointer)
		}
		gitDir = pointer

		if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			shared := strings.TrimSpace(string(common))
			if !filepath.IsAbs(shared) {
				shared = filepath.Join(gitDir, shared)
			}
			gitDir = filepath.Clean(shared)
		}
	}

	config := filepath.Join(gitDir, "config")
	if _, err := os.Stat(config); err != nil {
		if os.IsNotExist(err) {
			// A repository without a configuration file has no remote to
			// take a credential from.
			return "", nil
		}
		return "", fmt.Errorf("workspace: %w", err)
	}
	return config, nil
}

// sanitizeConfig rewrites the configuration in place and reports what it
// touched. The file is left alone when there is nothing to change, so an
// untouched repository keeps its modification time.
func sanitizeConfig(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}

	lines, changed, err := sanitizeLines(string(data))
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return nil, nil
	}

	// Written next to the original and renamed over it, so that a crash
	// halfway through cannot leave the repository without a configuration.
	temp, err := os.CreateTemp(filepath.Dir(path), ".baton-config-*")
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	defer os.Remove(temp.Name())

	if _, err := temp.WriteString(strings.Join(lines, "\n")); err != nil {
		temp.Close()
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err := temp.Close(); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err := os.Chmod(temp.Name(), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return changed, nil
}

// sanitizeLines is the whole of the rewriting, kept separate from the file
// so that it can be read and tested on its own. Git's configuration is
// edited line by line rather than parsed: an unknown construct is then left
// exactly as it was.
func sanitizeLines(text string) ([]string, []string, error) {
	var (
		lines   []string
		changed []string
		section string
	)

	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "[") {
			section = sectionName(trimmed)
			lines = append(lines, line)
			continue
		}

		key, value, ok := setting(trimmed)
		if !ok {
			lines = append(lines, line)
			continue
		}

		switch {
		case key == "extraheader" && hasAuthorization(value):
			// The line goes rather than being emptied: an empty
			// extraheader makes git refuse to talk to the remote at all.
			changed = append(changed, section+".extraheader (removed)")
			continue

		case key == "url" || key == "pushurl":
			clean, stripped := stripCredentials(value)
			if !stripped {
				lines = append(lines, line)
				continue
			}
			lines = append(lines, replaceValue(line, value, clean))
			changed = append(changed, section+"."+key)

		default:
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("workspace: read config: %w", err)
	}

	// Scanning drops the final newline; keep the file ending in one, the way
	// git writes it.
	if strings.HasSuffix(text, "\n") {
		lines = append(lines, "")
	}
	return lines, changed, nil
}

// sectionName turns a [remote "origin"] header into remote.origin, which is
// how the run log names the setting.
func sectionName(header string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(header, "["), "]")
	inner = strings.TrimSpace(inner)
	if name, sub, ok := strings.Cut(inner, " "); ok {
		return name + "." + strings.Trim(strings.TrimSpace(sub), `"`)
	}
	return inner
}

// setting splits a configuration line into its key and value, skipping
// comments.
func setting(line string) (string, string, bool) {
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", "", false
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value), true
}

// hasAuthorization reports whether an extraheader carries a credential. Git
// uses the setting for ordinary headers too, and those may stay.
func hasAuthorization(value string) bool {
	lowered := strings.ToLower(value)
	return strings.Contains(lowered, "authorization:") ||
		strings.Contains(lowered, "private-token:") ||
		strings.Contains(lowered, "job-token:")
}

// stripCredentials takes the user information out of a remote URL. An ssh
// user name is kept: git@host is who to log in as, not a secret. Anything in
// an http URL goes, because a token is commonly the user name there.
func stripCredentials(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		// Not a URL git can carry a credential in: scp-like remotes such as
		// git@host:org/repo have no place for a password.
		return raw, false
	}

	_, hasPassword := parsed.User.Password()
	switch {
	case hasPassword:
	case parsed.Scheme == "http" || parsed.Scheme == "https":
	default:
		return raw, false
	}

	parsed.User = nil
	return parsed.String(), true
}

// replaceValue rewrites the value of a line while leaving its spacing and
// any trailing comment alone.
func replaceValue(line, old, clean string) string {
	index := strings.LastIndex(line, old)
	if index < 0 {
		return line
	}
	return line[:index] + clean + line[index+len(old):]
}
