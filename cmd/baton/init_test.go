package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/exitcode"
	"gopkg.in/yaml.v3"
)

// 1. `baton init` writes the hello template and its own scenario passes
// `baton validate` without any provider, config or network involved.
func TestInitCmd_HelloTemplateValidates(t *testing.T) {
	dir := t.TempDir()

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir})
	})
	if code != exitcode.OK {
		t.Fatalf("init exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	if !strings.Contains(stdout, "wrote hello template") {
		t.Errorf("stdout missing confirmation: %q", stdout)
	}

	scenarioPath := filepath.Join(dir, "hello.yaml")
	if _, err := os.Stat(scenarioPath); err != nil {
		t.Fatalf("stat %s: %v", scenarioPath, err)
	}

	var vcode int
	vout, verr := captureOutput(t, func() {
		vcode = run([]string{"validate", scenarioPath})
	})
	if vcode != exitcode.OK {
		t.Fatalf("validate exit code = %d, want %d, stderr: %s", vcode, exitcode.OK, verr)
	}
	if !strings.Contains(vout, "valid") {
		t.Errorf("validate stdout = %q, want it to report the scenario valid", vout)
	}
}

// 2. The summarize template, for each supported provider, also validates:
// the provider/model substitution must produce a well-formed <provider>/model
// reference and a scenario `baton validate` accepts on its own, with no API
// key or network needed since validate never resolves secrets.
func TestInitCmd_SummarizeTemplateValidates(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()

			var code int
			_, stderr := captureOutput(t, func() {
				code = run([]string{"init", dir, "--template", "summarize", "--provider", provider})
			})
			if code != exitcode.OK {
				t.Fatalf("init exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
			}

			scenarioPath := filepath.Join(dir, "summarize.yaml")
			for _, f := range []string{"summarize.yaml", "baton.yaml", filepath.Join("prompts", "summarize.md"), filepath.Join("schemas", "summarize.json")} {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Fatalf("stat %s: %v", f, err)
				}
			}

			var vcode int
			_, verr := captureOutput(t, func() {
				vcode = run([]string{"validate", scenarioPath})
			})
			if vcode != exitcode.OK {
				t.Fatalf("validate exit code = %d, want %d, stderr: %s", vcode, exitcode.OK, verr)
			}
		})
	}
}

// 3. `baton init` followed by `baton run` on the hello template actually
// executes the echo command and checks its exit code, from a directory that
// is neither this repository nor its examples/ directory.
func TestInitCmd_HelloRunsForReal(t *testing.T) {
	dir := t.TempDir()
	if strings.Contains(dir, "Platforma/baton") {
		t.Fatalf("temp dir %s is inside the repository, want outside", dir)
	}

	var initCode int
	captureOutput(t, func() {
		initCode = run([]string{"init", dir})
	})
	if initCode != exitcode.OK {
		t.Fatalf("init exit code = %d, want %d", initCode, exitcode.OK)
	}

	scenarioPath := filepath.Join(dir, "hello.yaml")
	runsDir := filepath.Join(dir, "runs")

	var runCode int
	_, stderr := captureOutput(t, func() {
		runCode = run([]string{"run", scenarioPath, "-i", "who=Baton", "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if runCode != exitcode.OK {
		t.Fatalf("run exit code = %d, want %d, stderr: %s", runCode, exitcode.OK, stderr)
	}
	if !strings.Contains(stderr, "greet") {
		t.Errorf("run stderr = %q, want it to mention the greet step", stderr)
	}
	if !strings.Contains(stderr, "success") {
		t.Errorf("run stderr = %q, want it to report success", stderr)
	}

	runJSON := filepath.Join(runsDir, "r1", "run.json")
	if _, err := os.Stat(runJSON); err != nil {
		t.Fatalf("stat %s: %v", runJSON, err)
	}
	logPath := filepath.Join(runsDir, "r1", "steps", "greet", "stdout.log")
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read %s: %v", logPath, err)
	}
	if !strings.Contains(string(logBytes), "hello Baton") {
		t.Errorf("greet log = %q, want it to contain the real echo output", string(logBytes))
	}
}

// 4. An unrecognized --template value is a configuration error naming the
// offending value, and it writes nothing.
func TestInitCmd_UnknownTemplate(t *testing.T) {
	dir := t.TempDir()

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "bogus"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, `unknown template "bogus"`) {
		t.Fatalf("stderr = %q, want it to name the unknown template", stderr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 0 {
		t.Errorf("dir has %d entries, want 0: nothing should be written", len(entries))
	}
}

// 5. When one of several template files already exists, init refuses
// before writing anything: every other file of that template — files that
// sort both before and after the conflicting one — must also be absent, and
// the pre-existing file must be untouched. This is the "no partial writes"
// guarantee: conflicts are all checked up front, not file by file.
func TestInitCmd_ExistingFileNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	// summarize.yaml, baton.yaml, prompts/summarize.md and
	// schemas/summarize.json are the four files this template writes;
	// schemas/summarize.json sorts after baton.yaml and prompts/summarize.md
	// but before summarize.yaml, so pre-creating it alone checks writes are
	// blocked on both sides of the conflict, not just before it.
	schemasDir := filepath.Join(dir, "schemas")
	if err := os.MkdirAll(schemasDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	sentinel := "not a template file"
	conflictPath := filepath.Join(schemasDir, "summarize.json")
	if err := os.WriteFile(conflictPath, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "summarize"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.Config, stderr)
	}
	if !strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("stderr = %q, want it to refuse", stderr)
	}
	if !strings.Contains(stderr, conflictPath) {
		t.Fatalf("stderr = %q, want it to name %s", stderr, conflictPath)
	}

	got, err := os.ReadFile(conflictPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", conflictPath, err)
	}
	if string(got) != sentinel {
		t.Errorf("conflicting file was modified: got %q, want %q", string(got), sentinel)
	}

	for _, f := range []string{"summarize.yaml", "baton.yaml", filepath.Join("prompts", "summarize.md")} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("stat %s: err = %v, want os.IsNotExist (no partial write)", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "prompts")); !os.IsNotExist(err) {
		t.Errorf("prompts dir was created: err = %v, want os.IsNotExist", err)
	}
}

// 6. `baton help init` and `baton init -h`/`--help` both print init's own
// usage text and never write anything; an unknown command name given to
// help is reported the same way an unknown top-level command is.
func TestInitCmd_HelpRouting(t *testing.T) {
	t.Run("help init prints init usage", func(t *testing.T) {
		var code int
		stdout, _ := captureOutput(t, func() {
			code = run([]string{"help", "init"})
		})
		if code != exitcode.OK {
			t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
		}
		if stdout != initUsage {
			t.Fatalf("stdout = %q, want the exact init usage text", stdout)
		}
	})

	t.Run("bare help prints the top-level usage", func(t *testing.T) {
		var code int
		stdout, _ := captureOutput(t, func() {
			code = run([]string{"help"})
		})
		if code != exitcode.OK {
			t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
		}
		if stdout != usage {
			t.Fatalf("stdout = %q, want the exact top-level usage text", stdout)
		}
		if !strings.Contains(stdout, "init") {
			t.Errorf("top-level usage does not list init: %q", stdout)
		}
	})

	t.Run("help of an unknown command is a configuration error", func(t *testing.T) {
		var code int
		_, stderr := captureOutput(t, func() {
			code = run([]string{"help", "bogus"})
		})
		if code != exitcode.Config {
			t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
		}
		if !strings.Contains(stderr, `unknown command "bogus"`) {
			t.Fatalf("stderr = %q, want it to name the unknown command", stderr)
		}
	})

	t.Run("init -h prints usage, exits OK and writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		var code int
		_, stderr := captureOutput(t, func() {
			code = run([]string{"init", dir, "-h"})
		})
		if code != exitcode.OK {
			t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
		}
		if !strings.Contains(stderr, initUsage) {
			t.Fatalf("stderr = %q, want it to contain the init usage text", stderr)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("dir has %d entries, want 0: -h must not write", len(entries))
		}
	})
}

// 7. A dangling symlink at a target path is still a conflict, even though
// os.Stat (which follows symlinks) would report it as missing. initConflicts
// must use Lstat so init refuses instead of writing through the link.
func TestInitCmd_DanglingSymlinkIsConflict(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "hello.yaml")
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.Config, stderr)
	}
	if !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, linkPath) {
		t.Fatalf("stderr = %q, want it to refuse and name %s", stderr, linkPath)
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != filepath.Join(dir, "does-not-exist") {
		t.Errorf("symlink target changed to %q, want it untouched", target)
	}
}

// 8. A regular file where a template expects a directory (e.g. "schemas"
// instead of the "schemas/" it writes summarize.json under) must be caught
// as a conflict before anything is written, not surface later as a MkdirAll
// error after other files of the same template already landed on disk.
func TestInitCmd_FileBlocksTemplateDirectory(t *testing.T) {
	dir := t.TempDir()
	sentinel := "not a directory"
	blocker := filepath.Join(dir, "schemas")
	if err := os.WriteFile(blocker, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "summarize"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.Config, stderr)
	}
	if !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, blocker) {
		t.Fatalf("stderr = %q, want it to refuse and name %s", stderr, blocker)
	}

	got, err := os.ReadFile(blocker)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", blocker, err)
	}
	if string(got) != sentinel {
		t.Errorf("blocking file was modified: got %q, want %q", string(got), sentinel)
	}
	for _, f := range []string{"summarize.yaml", "baton.yaml", filepath.Join("prompts", "summarize.md")} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("stat %s: err = %v, want os.IsNotExist (no partial write)", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "prompts")); !os.IsNotExist(err) {
		t.Errorf("prompts dir was created: err = %v, want os.IsNotExist", err)
	}
}

// 9. A symlinked directory in place of a template directory ("prompts"
// pointing elsewhere) must not be followed: init refuses instead of writing
// through it into whatever the link targets.
func TestInitCmd_SymlinkDirectoryNotFollowed(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	link := filepath.Join(dir, "prompts")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "summarize"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.Config, stderr)
	}
	if !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, link) {
		t.Fatalf("stderr = %q, want it to refuse and name %s", stderr, link)
	}

	entries, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", elsewhere, err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote through the symlink into %s: entries = %v, want none", elsewhere, entries)
	}
}

// 10. A --model value containing YAML-significant characters (colon,
// quotes, "#", spaces) must round-trip through the generated summarize.yaml
// exactly, not corrupt the document or get reinterpreted (e.g. truncated at
// "#" as a comment, or split at ": " into a second mapping key).
func TestInitCmd_ModelWithYAMLSpecialCharsRoundTrips(t *testing.T) {
	dir := t.TempDir()
	const model = `weird: value # not a comment, and "quoted" too`

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "summarize", "--model", model})
	})
	if code != exitcode.OK {
		t.Fatalf("init exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}

	scenarioPath := filepath.Join(dir, "summarize.yaml")
	var doc struct {
		Defaults struct {
			Model string `yaml:"model"`
		} `yaml:"defaults"`
	}
	raw, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", scenarioPath, err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal(%s): %v", scenarioPath, err)
	}
	if doc.Defaults.Model != model {
		t.Fatalf("defaults.model = %q, want %q", doc.Defaults.Model, model)
	}

	var vcode int
	_, verr := captureOutput(t, func() {
		vcode = run([]string{"validate", scenarioPath})
	})
	if vcode != exitcode.OK {
		t.Fatalf("validate exit code = %d, want %d, stderr: %s", vcode, exitcode.OK, verr)
	}
}

// 11. The command `baton init` prints for the summarize template must
// actually find the generated baton.yaml: config.Load resolves "baton.yaml"
// against the current directory (spec section 12), never against the
// scenario's own directory, so init must tell the user to cd into dir
// first rather than pointing at "<dir>/summarize.yaml" from outside it.
func TestInitCmd_SummarizeNextCommandFindsConfig(t *testing.T) {
	dir := t.TempDir()

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "summarize", "--provider", "openrouter"})
	})
	if code != exitcode.OK {
		t.Fatalf("init exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	if !strings.Contains(stdout, "cd "+dir) {
		t.Fatalf("stdout = %q, want it to tell the user to cd into %s before baton run", stdout, dir)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("Chdir(%s): %v", orig, err)
		}
	})

	// The old, broken form: run from outside dir, naming the scenario by
	// its path. config.Load() must not see dir/baton.yaml from here.
	if err := os.Chdir(orig); err != nil {
		t.Fatalf("Chdir(%s): %v", orig, err)
	}
	cfgOutside, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() from %s: %v", orig, err)
	}
	if _, ok := cfgOutside.Providers["openrouter"]; ok {
		t.Fatalf("config.Load() from outside dir unexpectedly saw dir's baton.yaml")
	}

	// The suggested form: cd into dir first. config.Load() must then pick
	// up the openrouter provider init just wrote.
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	cfgInside, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() from %s: %v", dir, err)
	}
	if _, ok := cfgInside.Providers["openrouter"]; !ok {
		t.Fatalf("config.Load() from inside dir did not see the openrouter provider")
	}
}
