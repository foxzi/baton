package agent

import (
	"slices"
	"testing"

	"github.com/foxzi/baton/internal/scenario"
)

// parseAgentStep parses a one-step scenario and returns its agent body, so
// that the tests exercise the same path a real scenario takes.
func parseAgentStep(t *testing.T, body string) *scenario.AgentStep {
	t.Helper()
	const header = `
version: 1
name: n
steps:
  - id: s
    agent:
      engine: fake
      script: s.yaml
      prompt: p
      result: r.json
`
	scn, err := scenario.Parse([]byte(header+body), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	step := scn.Steps[0].Agent
	if step == nil {
		t.Fatalf("Steps[0].Agent = nil, want the agent body")
	}
	return step
}

// TestResolvePolicyProfiles checks the three rows of the table in spec
// section 7.2.
func TestResolvePolicyProfiles(t *testing.T) {
	cases := []struct {
		profile string
		want    Policy
	}{
		{
			profile: "review",
			want: Policy{
				FSRead:   true,
				FSWrite:  scenario.FSWriteNone,
				GitRead:  true,
				ExecMode: scenario.ExecModeNone,
				State:    scenario.StateRead,
			},
		},
		{
			profile: "fix",
			want: Policy{
				FSRead:    true,
				FSWrite:   scenario.FSWriteWorkspace,
				GitRead:   true,
				GitCommit: true,
				ExecMode:  scenario.ExecModeCommands,
				State:     scenario.StateRead,
			},
		},
		{
			profile: "research",
			want: Policy{
				FSRead:   true,
				FSWrite:  scenario.FSWriteNone,
				GitRead:  true,
				ExecMode: scenario.ExecModeNone,
				State:    scenario.StateReadWrite,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			got := ResolvePolicy(parseAgentStep(t, "      profile: "+tc.profile+"\n"))

			if got.FSRead != tc.want.FSRead || got.FSWrite != tc.want.FSWrite {
				t.Errorf("fs = %v/%q, want %v/%q", got.FSRead, got.FSWrite, tc.want.FSRead, tc.want.FSWrite)
			}
			if got.GitRead != tc.want.GitRead || got.GitCommit != tc.want.GitCommit {
				t.Errorf("git = %v/%v, want %v/%v", got.GitRead, got.GitCommit, tc.want.GitRead, tc.want.GitCommit)
			}
			if got.ExecMode != tc.want.ExecMode {
				t.Errorf("exec.mode = %q, want %q", got.ExecMode, tc.want.ExecMode)
			}
			if got.State != tc.want.State {
				t.Errorf("state = %q, want %q", got.State, tc.want.State)
			}
			if len(got.APIs) != 0 || len(got.MCP) != 0 || len(got.ExecCommands) != 0 {
				t.Errorf("apis/mcp/commands = %v/%v/%v, want nothing without a tools block",
					got.APIs, got.MCP, got.ExecCommands)
			}

			// Only research fetches, and only from a host list the step has
			// to write down.
			wantFetch := tc.profile == "research"
			if (got.Fetch != nil) != wantFetch {
				t.Errorf("Fetch = %+v, want present = %v", got.Fetch, wantFetch)
			}
			if got.Fetch != nil && len(got.Fetch.Allow) != 0 {
				t.Errorf("Fetch.Allow = %v, want empty until the step lists hosts", got.Fetch.Allow)
			}
		})
	}
}

// TestResolvePolicyDefaults checks what a step gets when it says nothing:
// the review row, the default deny list and the default caps.
func TestResolvePolicyDefaults(t *testing.T) {
	got := ResolvePolicy(parseAgentStep(t, ""))

	if got.FSWrite != scenario.FSWriteNone || got.ExecMode != scenario.ExecModeNone {
		t.Errorf("policy = %+v, want the review row for a step without a profile", got)
	}
	if got.MaxToolCalls != DefaultMaxToolCalls || got.MaxResultBytes != DefaultMaxResultBytes {
		t.Errorf("limits = %d/%d, want the defaults %d/%d",
			got.MaxToolCalls, got.MaxResultBytes, DefaultMaxToolCalls, DefaultMaxResultBytes)
	}
	if !slices.Equal(got.FSDeny, DefaultFSDeny()) {
		t.Errorf("FSDeny = %v, want the default list %v", got.FSDeny, DefaultFSDeny())
	}
	if got.AllowUnsafe {
		t.Errorf("AllowUnsafe = true, want false unless the step asks for it")
	}
}

// TestResolvePolicyToolsOverride checks that the tools block overrides single
// fields of the profile and leaves the rest alone.
func TestResolvePolicyToolsOverride(t *testing.T) {
	step := parseAgentStep(t, `      profile: review
      allow_unsafe: true
      tools:
        fs: { write: workspace, deny: [".env*", "vendor/**"] }
        git: { commit: true }
        exec: { mode: commands, commands: [test] }
        apis: [forge.get_change]
        fetch: { allow: ["pkg.go.dev"], max_calls: 3 }
        mcp: [context7]
        state: read-write
      limits: { max_tool_calls: 5 }
`)
	got := ResolvePolicy(step)

	if !got.FSRead {
		t.Errorf("FSRead = false, want the profile's true to survive an fs block that does not mention it")
	}
	if got.FSWrite != scenario.FSWriteWorkspace {
		t.Errorf("FSWrite = %q, want the override", got.FSWrite)
	}
	wantDeny := append(DefaultFSDeny(), ".env*", "vendor/**")
	if !slices.Equal(got.FSDeny, wantDeny) {
		t.Errorf("FSDeny = %v, want the default list extended: %v", got.FSDeny, wantDeny)
	}
	if !got.GitRead || !got.GitCommit {
		t.Errorf("git = %v/%v, want read from the profile and commit from the override", got.GitRead, got.GitCommit)
	}
	if got.ExecMode != scenario.ExecModeCommands || !slices.Equal(got.ExecCommands, []string{"test"}) {
		t.Errorf("exec = %q/%v, want commands mode limited to test", got.ExecMode, got.ExecCommands)
	}
	if !slices.Equal(got.APIs, []string{"forge.get_change"}) || !slices.Equal(got.MCP, []string{"context7"}) {
		t.Errorf("apis/mcp = %v/%v, want what the step listed", got.APIs, got.MCP)
	}
	if got.State != scenario.StateReadWrite {
		t.Errorf("State = %q, want the override", got.State)
	}
	if got.MaxToolCalls != 5 || got.MaxResultBytes != DefaultMaxResultBytes {
		t.Errorf("limits = %d/%d, want 5 and the default result cap", got.MaxToolCalls, got.MaxResultBytes)
	}
	if !got.AllowUnsafe {
		t.Errorf("AllowUnsafe = false, want the step's true")
	}

	// A fetch block turns the tool on for a profile that has none, and keeps
	// the defaults for the fields it does not set.
	if got.Fetch == nil {
		t.Fatalf("Fetch = nil, want the block to open the tool for the review profile")
	}
	if !slices.Equal(got.Fetch.Allow, []string{"pkg.go.dev"}) {
		t.Errorf("Fetch.Allow = %v, want the listed host", got.Fetch.Allow)
	}
	if got.Fetch.MaxCalls != 3 || got.Fetch.MaxBytes != DefaultFetchMaxBytes {
		t.Errorf("Fetch caps = %d/%d, want 3 and the default byte cap", got.Fetch.MaxCalls, got.Fetch.MaxBytes)
	}
}

// TestResolvePolicyDenyListIsPrivate checks that resolving twice does not let
// one step's deny list leak into the next, which append on a shared slice
// would do.
func TestResolvePolicyDenyListIsPrivate(t *testing.T) {
	first := ResolvePolicy(parseAgentStep(t, `      tools:
        fs: { deny: ["secrets/**"] }
`))
	second := ResolvePolicy(parseAgentStep(t, ""))

	if slices.Contains(second.FSDeny, "secrets/**") {
		t.Errorf("second FSDeny = %v, want it free of the first step's entries", second.FSDeny)
	}
	if !slices.Contains(first.FSDeny, "secrets/**") {
		t.Errorf("first FSDeny = %v, want its own entry", first.FSDeny)
	}
}
