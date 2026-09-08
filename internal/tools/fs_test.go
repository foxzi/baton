package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
)

// callFS finds a tool by name in the set and calls it.
func callFS(t *testing.T, f *FS, name, args string) (any, error) {
	t.Helper()
	var handler gateway.Handler
	for _, tool := range f.Tools() {
		if tool.Name == name {
			handler = tool.Handler
		}
	}
	if handler == nil {
		t.Fatalf("tool %q is not in the set", name)
	}
	return handler(context.Background(), json.RawMessage(args))
}

// callFSOK calls a tool and requires it to succeed, returning its result.
func callFSOK(t *testing.T, f *FS, name, args string) any {
	t.Helper()
	out, err := callFS(t, f, name, args)
	if err != nil {
		t.Fatalf("%s: call error = %v", name, err)
	}
	return out
}

// readOnlyFS builds an FS whose policy grants fs.read but not fs.write.
func readOnlyFS(t *testing.T, workspace string) *FS {
	t.Helper()
	f, err := NewFS(FSOptions{
		Workspace: workspace,
		Policy:    agent.Policy{FSRead: true, FSDeny: agent.DefaultFSDeny()},
	})
	if err != nil {
		t.Fatalf("NewFS(read) error = %v", err)
	}
	return f
}

// writableFS builds an FS whose policy grants both fs.read and fs.write.
func writableFS(t *testing.T, workspace string) *FS {
	t.Helper()
	f, err := NewFS(FSOptions{
		Workspace: workspace,
		Policy:    agent.Policy{FSRead: true, FSWrite: scenario.FSWriteWorkspace, FSDeny: agent.DefaultFSDeny()},
	})
	if err != nil {
		t.Fatalf("NewFS(write) error = %v", err)
	}
	return f
}

// 1. A policy with neither read nor write yields no tools, and no error.
func TestNewFSNoAccessGivesNoTools(t *testing.T) {
	f, err := NewFS(FSOptions{Workspace: t.TempDir(), Policy: agent.Policy{}})
	if err != nil {
		t.Fatalf("NewFS() error = %v", err)
	}
	if tools := f.Tools(); len(tools) != 0 {
		t.Fatalf("Tools() = %v, want none for a step without fs access", tools)
	}
}

// 2. Read-only lists exactly read/glob/grep, in that order; write access
// appends fs.write last, and is absent from the read-only set.
func TestNewFSToolLists(t *testing.T) {
	dir := t.TempDir()

	read := readOnlyFS(t, dir)
	names := toolNames(read.Tools())
	want := []string{"fs.read", "fs.glob", "fs.grep"}
	if !equalNames(names, want) {
		t.Fatalf("read-only Tools() = %v, want %v", names, want)
	}
	for _, tool := range read.Tools() {
		if tool.Name == "fs.write" {
			t.Error("fs.write is in a read-only set, want it absent")
		}
	}

	write := writableFS(t, dir)
	names = toolNames(write.Tools())
	want = append(want, "fs.write")
	if !equalNames(names, want) {
		t.Fatalf("read+write Tools() = %v, want %v", names, want)
	}
}

// 3. NewFS refuses an empty workspace when access is granted.
func TestNewFSRefusesEmptyWorkspace(t *testing.T) {
	if _, err := NewFS(FSOptions{Policy: agent.Policy{FSRead: true}}); err == nil {
		t.Error("NewFS() with no workspace: error = nil, want it refused")
	}
}

// 4. fs.read returns a text file's content and real size.
func TestFSReadTextFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	f := readOnlyFS(t, dir)
	out := callFSOK(t, f, "fs.read", `{"path":"a.txt"}`)
	file, ok := out.(FSFile)
	if !ok {
		t.Fatalf("fs.read returned %T, want FSFile", out)
	}
	if file.Content != "hello\n" || file.Size != 6 || file.Truncated || file.Binary {
		t.Errorf("fs.read = %+v, want the file's content untruncated", file)
	}
}

// 5. fs.read truncates a file larger than max_bytes.
func TestFSReadTruncates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write big.txt: %v", err)
	}

	f := readOnlyFS(t, dir)
	out := callFSOK(t, f, "fs.read", `{"path":"big.txt","max_bytes":"4"}`)
	file, ok := out.(FSFile)
	if !ok {
		t.Fatalf("fs.read returned %T, want FSFile", out)
	}
	if !file.Truncated || file.Content != "0123" || file.Size != 10 {
		t.Errorf("fs.read = %+v, want Truncated with the first 4 bytes and the real size", file)
	}
}

// 6. fs.read of a file whose head holds a NUL byte reports Binary and no
// content, instead of burning the result budget on bytes no model can use.
func TestFSReadBinaryFile(t *testing.T) {
	dir := t.TempDir()
	data := append([]byte("PNG"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(filepath.Join(dir, "img.bin"), data, 0o644); err != nil {
		t.Fatalf("write img.bin: %v", err)
	}

	f := readOnlyFS(t, dir)
	out := callFSOK(t, f, "fs.read", `{"path":"img.bin"}`)
	file, ok := out.(FSFile)
	if !ok {
		t.Fatalf("fs.read returned %T, want FSFile", out)
	}
	if !file.Binary || file.Content != "" || file.Size != int64(len(data)) {
		t.Errorf("fs.read = %+v, want Binary with empty content and the real size", file)
	}
}

// 7. fs.read of a missing file is refused as an error, with a short message.
func TestFSReadMissingFile(t *testing.T) {
	dir := t.TempDir()
	f := readOnlyFS(t, dir)

	_, err := callFS(t, f, "fs.read", `{"path":"x.txt"}`)
	if err == nil {
		t.Fatal("fs.read of a missing file: error = nil, want it refused")
	}
	if err.Error() != `open "x.txt": no such file` {
		t.Errorf("error = %q, want the short missing-file message", err.Error())
	}
}

// 8. fs.glob matches **/*.go across subdirectories and never descends into
// .git, which the deny list covers.
func TestFSGlobMatchesAndSkipsGit(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "main.go", "package main\n")
	mustWrite(t, dir, "pkg/util.go", "package pkg\n")
	mustWrite(t, dir, "README.md", "hi\n")
	mustWrite(t, dir, ".git/config", "[core]\n")
	mustWrite(t, dir, ".git/objects/deadbeef", "not go code")

	f := readOnlyFS(t, dir)
	out := callFSOK(t, f, "fs.glob", `{"pattern":"**/*.go"}`)
	result, ok := out.(FSGlobResult)
	if !ok {
		t.Fatalf("fs.glob returned %T, want FSGlobResult", out)
	}
	want := []string{"main.go", "pkg/util.go"}
	if !equalNames(result.Paths, want) {
		t.Fatalf("fs.glob Paths = %v, want %v", result.Paths, want)
	}
	if result.Truncated {
		t.Error("Truncated = true, want false")
	}
}

// 9. fs.grep finds a matching line and reports its number.
func TestFSGrepFindsLine(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "one\ntwo TODO fix me\nthree\n")

	f := readOnlyFS(t, dir)
	out := callFSOK(t, f, "fs.grep", `{"pattern":"TODO"}`)
	result, ok := out.(FSGrepResult)
	if !ok {
		t.Fatalf("fs.grep returned %T, want FSGrepResult", out)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("Matches = %v, want exactly one", result.Matches)
	}
	m := result.Matches[0]
	if m.Path != "a.txt" || m.Line != 2 || !strings.Contains(m.Text, "TODO") {
		t.Errorf("match = %+v, want a.txt line 2 with TODO", m)
	}
}

// 10. fs.grep stops at max_matches and reports Truncated.
func TestFSGrepMaxMatches(t *testing.T) {
	dir := t.TempDir()
	var lines strings.Builder
	for i := 0; i < 10; i++ {
		lines.WriteString("hit " + strconv.Itoa(i) + "\n")
	}
	mustWrite(t, dir, "a.txt", lines.String())

	f := readOnlyFS(t, dir)
	out := callFSOK(t, f, "fs.grep", `{"pattern":"hit","max_matches":"3"}`)
	result, ok := out.(FSGrepResult)
	if !ok {
		t.Fatalf("fs.grep returned %T, want FSGrepResult", out)
	}
	if len(result.Matches) != 3 || !result.Truncated {
		t.Errorf("fs.grep = %+v, want exactly 3 matches and Truncated", result)
	}
}

// 11. fs.grep refuses a pattern that does not compile as a regexp.
func TestFSGrepRefusesInvalidRegexp(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "hello\n")

	f := readOnlyFS(t, dir)
	if _, err := callFS(t, f, "fs.grep", `{"pattern":"("}`); err == nil {
		t.Error("fs.grep with an invalid regexp: error = nil, want it refused")
	}
}

// 12. fs.write creates a file, creating missing parent directories.
func TestFSWriteCreatesFileAndDirs(t *testing.T) {
	dir := t.TempDir()
	f := writableFS(t, dir)

	out := callFSOK(t, f, "fs.write", `{"path":"a/b/c.txt","content":"hi"}`)
	written, ok := out.(FSWritten)
	if !ok {
		t.Fatalf("fs.write returned %T, want FSWritten", out)
	}
	if !written.Created || written.Size != 2 || written.Path != "a/b/c.txt" {
		t.Errorf("fs.write = %+v, want a new 2-byte file", written)
	}

	data, err := os.ReadFile(filepath.Join(dir, "a/b/c.txt"))
	if err != nil {
		t.Fatalf("read the written file: %v", err)
	}
	if string(data) != "hi" {
		t.Errorf("file content = %q, want %q", data, "hi")
	}
}

// 13. fs.write overwriting an existing file reports Created false.
func TestFSWriteOverwrites(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "old")

	f := writableFS(t, dir)
	out := callFSOK(t, f, "fs.write", `{"path":"a.txt","content":"new"}`)
	written, ok := out.(FSWritten)
	if !ok {
		t.Fatalf("fs.write returned %T, want FSWritten", out)
	}
	if written.Created {
		t.Error("Created = true, want false for an existing file")
	}

	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("file content = %q, want %q", data, "new")
	}
}

// 14. fs.write refuses content over the size limit.
func TestFSWriteRefusesOversizedContent(t *testing.T) {
	dir := t.TempDir()
	f := writableFS(t, dir)

	big := strings.Repeat("x", maxFSWriteBytes+1)
	if _, err := callFS(t, f, "fs.write", `{"path":"a.txt","content":"`+big+`"}`); err == nil {
		t.Error("fs.write with oversized content: error = nil, want it refused")
	}
}

// 15. fs.write refuses a path the deny list covers, and one with ...
func TestFSWriteRefusesDeniedAndDotDot(t *testing.T) {
	dir := t.TempDir()
	f := writableFS(t, dir)

	if _, err := callFS(t, f, "fs.write", `{"path":".env","content":"SECRET=1"}`); err == nil {
		t.Error("fs.write to .env: error = nil, want it refused")
	}
	if _, err := callFS(t, f, "fs.write", `{"path":"../escape.txt","content":"x"}`); err == nil {
		t.Error("fs.write with a .. path: error = nil, want it refused")
	}
}

// 15b. Every fs tool takes a workspace-relative path: an absolute path and a
// parent-directory path are refused whichever tool they are given to (spec
// sections 7.4 and 13).
func TestFSRefusesPathsOutsideTheWorkspace(t *testing.T) {
	f := writableFS(t, t.TempDir())

	cases := []struct {
		tool string
		args string
	}{
		{"fs.read", `{"path":"/etc/passwd"}`},
		{"fs.read", `{"path":"../../etc/passwd"}`},
		{"fs.glob", `{"pattern":"/etc/*"}`},
		{"fs.glob", `{"pattern":"../*"}`},
		{"fs.grep", `{"pattern":"root","glob":"/etc/*"}`},
		{"fs.write", `{"path":"/etc/passwd","content":"x"}`},
	}
	for _, c := range cases {
		if _, err := callFS(t, f, c.tool, c.args); err == nil {
			t.Errorf("%s with %s: error = nil, want it refused", c.tool, c.args)
		}
	}
}

// 16. A symlink pointing outside the workspace is refused by both fs.read
// and fs.write.
func TestFSRefusesSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	read := readOnlyFS(t, dir)
	if _, err := callFS(t, read, "fs.read", `{"path":"link.txt"}`); !isPolicyRefusal(err) {
		t.Errorf("fs.read through a symlink leaving the workspace: error = %v, want a policy refusal", err)
	}

	write := writableFS(t, dir)
	if _, err := callFS(t, write, "fs.write", `{"path":"link.txt","content":"x"}`); !isPolicyRefusal(err) {
		t.Errorf("fs.write through a symlink leaving the workspace: error = %v, want a policy refusal", err)
	}
	if data, rerr := os.ReadFile(filepath.Join(outside, "secret.txt")); rerr != nil || string(data) != "nope" {
		t.Errorf("outside file changed: data=%q err=%v, want it untouched", data, rerr)
	}
}

// mustWrite writes a file relative to dir, creating parent directories.
func mustWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// 17. A symlink inside the workspace is skipped by the walking tools rather
// than followed: fs.glob does not list it and fs.grep does not read what it
// points at, which may be a file outside the workspace entirely.
func TestFSWalkSkipsSymlinks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("token=hunter2\n"), 0o600); err != nil {
		t.Fatalf("write the outside file: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "own.txt"), []byte("token=nothing\n"), 0o644); err != nil {
		t.Fatalf("write own.txt: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}

	f, err := NewFS(FSOptions{Workspace: dir, Policy: agent.Policy{FSRead: true}})
	if err != nil {
		t.Fatalf("NewFS() error = %v", err)
	}

	globOut, ok := callFSOK(t, f, "fs.glob", `{"pattern":"*.txt"}`).(FSGlobResult)
	if !ok {
		t.Fatalf("fs.glob returned %T, want FSGlobResult", globOut)
	}
	globbed := globOut
	for _, p := range globbed.Paths {
		if p == "link.txt" {
			t.Errorf("Paths = %v, want the symlink left out", globbed.Paths)
		}
	}

	grepOut, ok := callFSOK(t, f, "fs.grep", `{"pattern":"token="}`).(FSGrepResult)
	if !ok {
		t.Fatalf("fs.grep returned %T, want FSGrepResult", grepOut)
	}
	grepped := grepOut
	for _, m := range grepped.Matches {
		if m.Path == "link.txt" {
			t.Errorf("fs.grep read through the symlink: %#v", m)
		}
		if strings.Contains(m.Text, "hunter2") {
			t.Errorf("fs.grep returned the outside file's content: %q", m.Text)
		}
	}
	if len(grepped.Matches) != 1 {
		t.Errorf("Matches = %#v, want only the workspace's own file", grepped.Matches)
	}
}
