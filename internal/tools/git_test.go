package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
)

// gitAvailable skips a test when there is no git binary to shell out to.
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// runGit shells out to git in dir, failing the test on a non-zero exit.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newRepo builds a repository in a fresh temp directory, with one committed
// file, and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)

	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-q", "-m", "first")
	return dir
}

// callGit finds a tool by name in the set and calls it.
func callGit(t *testing.T, g *Git, name, args string) (any, error) {
	t.Helper()
	var handler gateway.Handler
	for _, tool := range g.Tools() {
		if tool.Name == name {
			handler = tool.Handler
		}
	}
	if handler == nil {
		t.Fatalf("tool %q is not in the set", name)
	}
	return handler(context.Background(), json.RawMessage(args))
}

// callGitOK calls a tool and requires it to succeed, returning its Response.
func callGitOK(t *testing.T, g *Git, name, args string) Response {
	t.Helper()
	out, err := callGit(t, g, name, args)
	if err != nil {
		t.Fatalf("%s: call error = %v", name, err)
	}
	resp, ok := out.(Response)
	if !ok {
		t.Fatalf("%s: call returned %T, want Response", name, out)
	}
	return resp
}

// 1. A policy with neither read nor commit yields no tools, and no error.
func TestNewGitNoAccessGivesNoTools(t *testing.T) {
	g, err := NewGit(GitOptions{Workspace: t.TempDir(), Policy: agent.Policy{}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}
	if tools := g.Tools(); len(tools) != 0 {
		t.Fatalf("Tools() = %v, want none for a step without git access", tools)
	}
}

// 2. Read-only lists exactly status/diff/log/show, in that order; commit
// appends git.commit last.
func TestNewGitToolLists(t *testing.T) {
	dir := t.TempDir()

	read, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit(read) error = %v", err)
	}
	names := toolNames(read.Tools())
	want := []string{"git.status", "git.diff", "git.log", "git.show", "git.blame"}
	if !equalNames(names, want) {
		t.Fatalf("read-only Tools() = %v, want %v", names, want)
	}

	commit, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitCommit: true}})
	if err != nil {
		t.Fatalf("NewGit(commit) error = %v", err)
	}
	names = toolNames(commit.Tools())
	want = append(want, "git.commit")
	if !equalNames(names, want) {
		t.Fatalf("commit Tools() = %v, want %v", names, want)
	}
}

func toolNames(tools []gateway.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}

func equalNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// 3. git.status sees a modified file: exit code 0, the file name in stdout.
func TestGitStatusSeesAModifiedFile(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.status", `{}`)
	if resp.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", resp.ExitCode)
	}
	if !strings.Contains(resp.Stdout, "a.txt") {
		t.Errorf("Stdout = %q, want it to mention a.txt", resp.Stdout)
	}
}

// 4. git.diff with a ref, and git.diff limited to a path.
func TestGitDiff(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	runGit(t, dir, "add", "b.txt")

	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.diff", `{"ref":"HEAD"}`)
	if resp.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", resp.ExitCode)
	}
	if !strings.Contains(resp.Stdout, "a.txt") {
		t.Errorf("Stdout = %q, want the diff of a.txt against HEAD", resp.Stdout)
	}

	resp = callGitOK(t, g, "git.diff", `{"path":"a.txt"}`)
	if !strings.Contains(resp.Stdout, "a.txt") || strings.Contains(resp.Stdout, "b.txt") {
		t.Errorf("Stdout = %q, want only a.txt's diff", resp.Stdout)
	}
}

// 5. git.log honours max_count and its default, and refuses a non-numeric
// or oversized max_count.
func TestGitLogMaxCount(t *testing.T) {
	dir := newRepo(t)
	for _, message := range []string{"second", "third", "fourth"} {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(message+"\n"), 0o644); err != nil {
			t.Fatalf("write a.txt: %v", err)
		}
		runGit(t, dir, "add", "a.txt")
		runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-q", "-m", message)
	}

	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.log", `{"max_count":"2"}`)
	if got := len(strings.Split(strings.TrimRight(resp.Stdout, "\n"), "\n")); got != 2 {
		t.Errorf("commits shown = %d, want 2", got)
	}

	resp = callGitOK(t, g, "git.log", `{}`)
	if got := len(strings.Split(strings.TrimRight(resp.Stdout, "\n"), "\n")); got != 4 {
		t.Errorf("commits shown with the default = %d, want 4", got)
	}

	if _, err := callGit(t, g, "git.log", `{"max_count":"nope"}`); err == nil {
		t.Error("git.log with a non-numeric max_count: error = nil, want it refused")
	}
	if _, err := callGit(t, g, "git.log", `{"max_count":"201"}`); err == nil {
		t.Error("git.log with max_count over 200: error = nil, want it refused")
	}
}

// 6. git.show returns the file content at a revision.
func TestGitShow(t *testing.T) {
	dir := newRepo(t)
	first := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-q", "-m", "second")

	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.show", `{"ref":"`+first+`","path":"a.txt"}`)
	if resp.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", resp.ExitCode)
	}
	if strings.TrimSpace(resp.Stdout) != "one" {
		t.Errorf("Stdout = %q, want the first revision's content", resp.Stdout)
	}
}

// 7. git.commit makes a commit, and is absent from a read-only set.
func TestGitCommit(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitCommit: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.commit", `{"message":"second"}`)
	if resp.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, Stderr = %q, want a successful commit", resp.ExitCode, resp.Stderr)
	}

	log := runGit(t, dir, "log", "--oneline")
	if !strings.Contains(log, "second") {
		t.Errorf("git log --oneline = %q, want the new commit", log)
	}

	readOnly, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit(read) error = %v", err)
	}
	for _, tool := range readOnly.Tools() {
		if tool.Name == "git.commit" {
			t.Error("git.commit is in a read-only set, want it absent")
		}
	}
}

// 8. A revision that begins with - is refused before git runs, and so is a
// path containing .. and a path the deny list covers.
func TestGitRefusesUnsafeArguments(t *testing.T) {
	dir := newRepo(t)
	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true, FSDeny: []string{".git/**"}}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	if _, err := callGit(t, g, "git.diff", `{"ref":"--output=/tmp/x"}`); err == nil {
		t.Error("git.diff with a ref starting with -: error = nil, want it refused")
	}
	if _, err := callGit(t, g, "git.diff", `{"path":"../etc/passwd"}`); err == nil {
		t.Error("git.diff with a .. path: error = nil, want it refused")
	}
	if _, err := callGit(t, g, "git.diff", `{"path":".git/config"}`); err == nil {
		t.Error("git.diff with a denied path: error = nil, want it refused")
	}
}

// 9. A git failure comes back as data: git.show of a revision that does not
// exist has a non-zero exit code and non-empty stderr, and a nil error.
func TestGitFailureIsDataNotAnError(t *testing.T) {
	dir := newRepo(t)
	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.show", `{"ref":"deadbeefdeadbeef"}`)
	if resp.ExitCode == 0 {
		t.Error("ExitCode = 0, want a failing revision to be reported as non-zero")
	}
	if resp.Stderr == "" {
		t.Error("Stderr is empty, want git's own error message")
	}
}

// 10. An unknown argument name is refused.
func TestGitRefusesUnknownArgument(t *testing.T) {
	dir := newRepo(t)
	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	_, err = callGit(t, g, "git.status", `{"bogus":"x"}`)
	if err == nil {
		t.Fatal("git.status with an unknown argument: error = nil, want it refused")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %v, want it to name the argument", err)
	}
}

// 11. deny.go's matcher, over the deny list of spec section 7.3.
func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{".git/**", ".git", true},
		{".git/**", ".git/config", true},
		{".env*", ".env.local", true},
		{"**/*.pem", "keys/a.pem", true},
		{"**/*.pem", "a.pemx", false},
		{"vendor/**", "vendor/x/y.go", true},
		{"**/id_*", "id_rsa", true},
		{"vendor/**", "src/main.go", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// 12. git.blame names the author of every line of a file, and refuses a call
// that leaves the path out: blaming the whole tree is not a question this
// tool answers.
func TestGitBlameNamesTheAuthor(t *testing.T) {
	dir := newRepo(t)
	g, err := NewGit(GitOptions{Workspace: dir, Policy: agent.Policy{GitRead: true}})
	if err != nil {
		t.Fatalf("NewGit() error = %v", err)
	}

	resp := callGitOK(t, g, "git.blame", `{"path":"a.txt"}`)
	if resp.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, Stderr = %q, want a successful blame", resp.ExitCode, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "test") || !strings.Contains(resp.Stdout, "one") {
		t.Errorf("Stdout = %q, want the author and the line's content", resp.Stdout)
	}

	if _, err := callGit(t, g, "git.blame", `{}`); err == nil {
		t.Error("git.blame without a path: error = nil, want it refused")
	}
	if _, err := callGit(t, g, "git.blame", `{"path":"a.txt","ref":"-L1,2"}`); err == nil {
		t.Error("git.blame with a ref starting with -: error = nil, want it refused")
	}
}
