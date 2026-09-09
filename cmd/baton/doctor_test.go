package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/exitcode"
)

// hermeticDoctorConfig points BATON_CONFIG at path and, unless path already
// exists, that means "no configuration": config.Load treats it as required
// only through this override, so an empty non-existent path makes doctor's
// config search fail predictably instead of picking up a real
// ~/.config/baton/config.yaml or a ./baton.yaml left by another test.
func hermeticDoctorConfig(t *testing.T, path string) {
	t.Helper()
	t.Setenv(config.EnvConfigOverride, path)
}

// 1. A scenario with no llm/agent step and a valid, present config file
// reports every check OK and exits 0.
func TestDoctorCmd_Success(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	cfgPath := filepath.Join(dir, "baton.yaml")
	if err := os.WriteFile(cfgPath, []byte("defaults:\n  engine: fake\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	hermeticDoctorConfig(t, cfgPath)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.OK {
		t.Fatalf("doctorCmd() = %d, want %d; output:\n%s", code, exitcode.OK, stdout)
	}
	for _, want := range []string{"OK load", "OK validate", "FOUND " + cfgPath, "not checked:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
}

// 2. An invalid scenario is reported as a load/validate failure and exits
// with exitcode.Config, without touching the configuration or environment.
func TestDoctorCmd_ValidationFailure(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", `
version: 1
name: bad-scenario
steps:
  - id: bad step
    run:
      argv: ["echo", "hi"]
`)
	hermeticDoctorConfig(t, filepath.Join(dir, "missing-config.yaml"))

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.Config {
		t.Fatalf("doctorCmd() = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stdout, "FAIL validate") {
		t.Errorf("stdout missing FAIL validate; got:\n%s", stdout)
	}
}

// 3. A scenario file that does not exist is reported as a load failure, not
// a panic or a generic error, and exits with exitcode.Config.
func TestDoctorCmd_MissingScenarioFile(t *testing.T) {
	dir := t.TempDir()
	hermeticDoctorConfig(t, filepath.Join(dir, "missing-config.yaml"))

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{filepath.Join(dir, "nope.yaml")})
	})
	if code != exitcode.Config {
		t.Fatalf("doctorCmd() = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stdout, "FAIL load") {
		t.Errorf("stdout missing FAIL load; got:\n%s", stdout)
	}
}

// llmDoctorScenario has one llm step whose model names the "test" provider
// and whose schema does not exist on disk, so both the provider check and
// the schema check have something to report.
const llmDoctorScenario = `
version: 1
name: llm-scenario
steps:
  - id: ask
    llm:
      model: test/model-1
      prompt: "hello {{.name}}"
      schema: missing-schema.json
`

// 4. A provider declared in the configuration with its api key read from an
// unset environment variable is reported as a failure and the whole command
// exits with exitcode.Config; the missing schema file is reported too.
func TestDoctorCmd_ProviderKeyUnset(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", llmDoctorScenario)
	cfgPath := filepath.Join(dir, "baton.yaml")
	writeScenario(t, dir, "baton.yaml", `
providers:
  test:
    kind: openai_compatible
    base_url: http://127.0.0.1:0
    api_key:
      from: env
      key: BATON_DOCTOR_TEST_KEY
`)
	hermeticDoctorConfig(t, cfgPath)
	t.Setenv("BATON_DOCTOR_TEST_KEY", "")
	os.Unsetenv("BATON_DOCTOR_TEST_KEY")

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.Config {
		t.Fatalf("doctorCmd() = %d, want %d; output:\n%s", code, exitcode.Config, stdout)
	}
	for _, want := range []string{
		"FAIL provider test",
		"BATON_DOCTOR_TEST_KEY is not set",
		"FAIL schema",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
}

// 5. The same scenario with the environment variable set and the schema
// file present passes every step-level check and exits 0.
func TestDoctorCmd_ProviderKeySetAndSchemaPresent(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", llmDoctorScenario)
	if err := os.WriteFile(filepath.Join(dir, "missing-schema.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfgPath := filepath.Join(dir, "baton.yaml")
	writeScenario(t, dir, "baton.yaml", `
providers:
  test:
    kind: openai_compatible
    base_url: http://127.0.0.1:0
    api_key:
      from: env
      key: BATON_DOCTOR_TEST_KEY
`)
	hermeticDoctorConfig(t, cfgPath)
	t.Setenv("BATON_DOCTOR_TEST_KEY", "sk-test")

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.OK {
		t.Fatalf("doctorCmd() = %d, want %d; output:\n%s", code, exitcode.OK, stdout)
	}
	for _, want := range []string{
		"OK provider test",
		"BATON_DOCTOR_TEST_KEY is set",
		"OK schema",
		"OK prompt: inline template",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
}

// 6. A provider the scenario names but the configuration never declares is
// reported clearly, without a nil-pointer panic.
func TestDoctorCmd_ProviderNotDeclared(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", llmDoctorScenario)
	cfgPath := filepath.Join(dir, "baton.yaml")
	writeScenario(t, dir, "baton.yaml", "defaults:\n  engine: fake\n")
	hermeticDoctorConfig(t, cfgPath)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.Config {
		t.Fatalf("doctorCmd() = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stdout, "FAIL provider test: not declared in configuration") {
		t.Errorf("stdout missing undeclared-provider message; got:\n%s", stdout)
	}
}

// defaultsModelDoctorScenario has one llm step with no model of its own, so
// doctor must resolve it from the scenario's defaults.model like the engine
// does (internal/engine/llm.go prepareLLM), not report it as unset.
const defaultsModelDoctorScenario = `
version: 1
name: defaults-model-scenario
defaults:
  model: test/model-1
steps:
  - id: ask
    llm:
      prompt: "hello {{.name}}"
      schema: schema.json
`

// 7. A step with no llm.model of its own resolves through the scenario's
// defaults.model, the same as a real run would, instead of failing on an
// empty model.
func TestDoctorCmd_ModelFromScenarioDefaults(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", defaultsModelDoctorScenario)
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfgPath := filepath.Join(dir, "baton.yaml")
	writeScenario(t, dir, "baton.yaml", `
providers:
  test:
    kind: openai_compatible
    base_url: http://127.0.0.1:0
    api_key:
      from: env
      key: BATON_DOCTOR_TEST_KEY
`)
	hermeticDoctorConfig(t, cfgPath)
	t.Setenv("BATON_DOCTOR_TEST_KEY", "sk-test")

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.OK {
		t.Fatalf("doctorCmd() = %d, want %d; output:\n%s", code, exitcode.OK, stdout)
	}
	for _, want := range []string{
		"OK model: test/model-1 (from defaults.model)",
		"OK provider test",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
}

// 8. A step with no llm.model and no defaults.model anywhere is reported
// as such, not as a malformed "<provider>/<model>" reference.
func TestDoctorCmd_ModelMissingEverywhere(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", `
version: 1
name: no-model-scenario
steps:
  - id: ask
    llm:
      prompt: "hello {{.name}}"
      schema: schema.json
`)
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	hermeticDoctorConfig(t, filepath.Join(dir, "missing-config.yaml"))

	var code int
	stdout, _ := captureOutput(t, func() {
		code = doctorCmd([]string{scenarioPath})
	})
	if code != exitcode.Config {
		t.Fatalf("doctorCmd() = %d, want %d; output:\n%s", code, exitcode.Config, stdout)
	}
	if !strings.Contains(stdout, "FAIL model: llm.model is not set and defaults.model is empty") {
		t.Errorf("stdout missing empty-model failure; got:\n%s", stdout)
	}
}

// 9. `baton doctor` with no argument prints usage and exits with
// exitcode.Config, and -h/--help exits 0, matching every other command.
func TestDoctorCmd_ArgHandling(t *testing.T) {
	var code int
	captureOutput(t, func() { code = doctorCmd(nil) })
	if code != exitcode.Config {
		t.Errorf("doctorCmd(nil) = %d, want %d", code, exitcode.Config)
	}

	captureOutput(t, func() { code = run([]string{"doctor", "-h"}) })
	if code != exitcode.OK {
		t.Errorf(`run(["doctor", "-h"]) = %d, want %d`, code, exitcode.OK)
	}
}

// 10. `baton doctor` is reachable as a top-level command and its usage text
// is available through `baton help doctor`.
func TestDoctorCmd_ReachableFromTopLevel(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	cfgPath := writeScenario(t, dir, "baton.yaml", "defaults:\n  engine: fake\n")
	hermeticDoctorConfig(t, cfgPath)

	var code int
	captureOutput(t, func() { code = run([]string{"doctor", scenarioPath}) })
	if code != exitcode.OK {
		t.Errorf("run([doctor %s]) = %d, want %d", scenarioPath, code, exitcode.OK)
	}

	helpOut, _ := captureOutput(t, func() { code = run([]string{"help", "doctor"}) })
	if code != exitcode.OK {
		t.Errorf(`run(["help", "doctor"]) = %d, want %d`, code, exitcode.OK)
	}
	if !strings.Contains(helpOut, "baton doctor") {
		t.Errorf("help doctor output missing usage; got:\n%s", helpOut)
	}
}
