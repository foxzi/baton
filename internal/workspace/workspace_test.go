package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo writes a git repository with the given config file and returns its
// root.
func repo(t *testing.T, config string) string {
	t.Helper()

	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	return root
}

// configOf reads back the config of a prepared repository.
func configOf(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

func TestPrepareRejectsAMissingDirectory(t *testing.T) {
	if _, err := Prepare(""); err == nil {
		t.Error("Prepare(\"\"): error = nil, want an error")
	}
	if _, err := Prepare(filepath.Join(t.TempDir(), "not-there")); err == nil {
		t.Error("Prepare of a missing directory: error = nil, want an error")
	}
}

func TestPrepareWithoutARepository(t *testing.T) {
	dir := t.TempDir()

	info, err := Prepare(dir)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if info.Git {
		t.Error("Git = true, want false")
	}
	if info.Root != dir {
		t.Errorf("Root = %q, want %q", info.Root, dir)
	}
	if len(info.Changed) != 0 {
		t.Errorf("Changed = %v, want nothing", info.Changed)
	}
}

func TestPrepareFindsTheRepositoryRootAbove(t *testing.T) {
	root := repo(t, "[core]\n\tbare = false\n")
	nested := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	info, err := Prepare(nested)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !info.Git || info.Root != root {
		t.Errorf("Root = %q, Git = %v, want %q and true", info.Root, info.Git, root)
	}
}

func TestPrepareStripsHTTPCredentials(t *testing.T) {
	root := repo(t, `[remote "origin"]
	url = https://oauth2:glpat-secret@gitlab.example.com/team/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`)

	info, err := Prepare(root)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	config := configOf(t, root)
	if strings.Contains(config, "glpat-secret") || strings.Contains(config, "oauth2") {
		t.Errorf("the credential survived:\n%s", config)
	}
	if !strings.Contains(config, "url = https://gitlab.example.com/team/repo.git") {
		t.Errorf("the remote lost its address:\n%s", config)
	}
	if !strings.Contains(config, "fetch = +refs/heads/*") {
		t.Errorf("an unrelated setting was touched:\n%s", config)
	}
	if len(info.Changed) != 1 || info.Changed[0] != "remote.origin.url" {
		t.Errorf("Changed = %v, want [remote.origin.url]", info.Changed)
	}
	if strings.Contains(strings.Join(info.Changed, " "), "glpat") {
		t.Error("the report repeats the credential")
	}
}

func TestPrepareStripsATokenUsedAsTheUserName(t *testing.T) {
	root := repo(t, "[remote \"origin\"]\n\turl = https://ghp_tokentokentoken@github.com/org/repo.git\n")

	if _, err := Prepare(root); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if config := configOf(t, root); strings.Contains(config, "ghp_") {
		t.Errorf("the token survived:\n%s", config)
	}
}

func TestPrepareStripsPushURLs(t *testing.T) {
	root := repo(t, "[remote \"origin\"]\n\tpushurl = https://user:pass@example.com/repo.git\n")

	info, err := Prepare(root)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if config := configOf(t, root); strings.Contains(config, "pass") {
		t.Errorf("the credential survived:\n%s", config)
	}
	if len(info.Changed) != 1 || info.Changed[0] != "remote.origin.pushurl" {
		t.Errorf("Changed = %v, want [remote.origin.pushurl]", info.Changed)
	}
}

func TestPrepareKeepsAnSSHUserName(t *testing.T) {
	config := "[remote \"origin\"]\n\turl = ssh://git@example.com/org/repo.git\n"
	root := repo(t, config)

	info, err := Prepare(root)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(info.Changed) != 0 {
		t.Errorf("Changed = %v, want nothing: git@ is a login, not a credential", info.Changed)
	}
	if got := configOf(t, root); got != config {
		t.Errorf("config = %q, want it untouched", got)
	}
}

func TestPrepareKeepsScpStyleRemotes(t *testing.T) {
	config := "[remote \"origin\"]\n\turl = git@example.com:org/repo.git\n"
	root := repo(t, config)

	if _, err := Prepare(root); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := configOf(t, root); got != config {
		t.Errorf("config = %q, want it untouched", got)
	}
}

func TestPrepareStripsAnSSHPassword(t *testing.T) {
	root := repo(t, "[remote \"origin\"]\n\turl = ssh://git:hunter2@example.com/org/repo.git\n")

	if _, err := Prepare(root); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if config := configOf(t, root); strings.Contains(config, "hunter2") {
		t.Errorf("the password survived:\n%s", config)
	}
}

func TestPrepareRemovesAuthorizationHeaders(t *testing.T) {
	root := repo(t, `[http]
	extraheader = Authorization: Basic Z2l0OnRva2Vu
	extraheader = X-Trace: on
[http "https://example.com"]
	extraheader = PRIVATE-TOKEN: glpat-secret
	sslVerify = true
`)

	info, err := Prepare(root)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	config := configOf(t, root)
	for _, gone := range []string{"Authorization", "Z2l0OnRva2Vu", "PRIVATE-TOKEN", "glpat-secret"} {
		if strings.Contains(config, gone) {
			t.Errorf("%q survived:\n%s", gone, config)
		}
	}
	if !strings.Contains(config, "X-Trace: on") {
		t.Errorf("a header without a credential was removed:\n%s", config)
	}
	if !strings.Contains(config, "sslVerify = true") {
		t.Errorf("an unrelated setting was removed:\n%s", config)
	}
	if len(info.Changed) != 2 {
		t.Errorf("Changed = %v, want the two authorization headers", info.Changed)
	}
}

func TestPrepareLeavesACleanConfigAlone(t *testing.T) {
	config := `# comment
[core]
	bare = false
[remote "origin"]
	url = https://example.com/org/repo.git
`
	root := repo(t, config)

	before, err := os.Stat(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	info, err := Prepare(root)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(info.Changed) != 0 {
		t.Errorf("Changed = %v, want nothing", info.Changed)
	}
	if got := configOf(t, root); got != config {
		t.Errorf("config = %q, want %q", got, config)
	}

	after, err := os.Stat(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a clean config was rewritten anyway")
	}
}

func TestPrepareKeepsTheConfigMode(t *testing.T) {
	root := repo(t, "[remote \"origin\"]\n\turl = https://u:p@example.com/repo.git\n")
	path := filepath.Join(root, ".git", "config")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := Prepare(root); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestPrepareWithoutAConfigFile(t *testing.T) {
	root := repo(t, "")

	info, err := Prepare(root)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !info.Git {
		t.Error("Git = false, want true")
	}
	if info.Config != "" {
		t.Errorf("Config = %q, want empty", info.Config)
	}
}

func TestPrepareFollowsAWorktreePointer(t *testing.T) {
	main := repo(t, "[remote \"origin\"]\n\turl = https://u:p@example.com/repo.git\n")
	mainGit := filepath.Join(main, ".git")

	// A linked worktree: .git is a file, and the remotes live in the common
	// directory it shares with the main checkout.
	worktreeGit := filepath.Join(mainGit, "worktrees", "feature")
	if err := os.MkdirAll(worktreeGit, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeGit, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+worktreeGit+"\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	info, err := Prepare(linked)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if info.Config != filepath.Join(mainGit, "config") {
		t.Errorf("Config = %q, want the shared config %q", info.Config, filepath.Join(mainGit, "config"))
	}
	if config := configOf(t, main); strings.Contains(config, "u:p@") {
		t.Errorf("the credential survived:\n%s", config)
	}
}
