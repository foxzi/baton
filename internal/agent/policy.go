package agent

import "github.com/foxzi/baton/internal/scenario"

// Policy is what an agent step may do, with every question already answered:
// the profile's row plus the step's overrides (spec sections 7.2 and 7.3).
type Policy struct {
	FSRead  bool
	FSWrite scenario.FSWrite

	// FSDeny is the default deny list plus whatever the step added.
	FSDeny []string

	GitRead   bool
	GitCommit bool

	ExecMode scenario.ExecMode

	// ExecCommands names the commands the agent may run; empty means every
	// command the scenario declares.
	ExecCommands []string

	// APIs are readonly pack operations, as <api>.<op>.
	APIs []string

	// Fetch is nil when the step gets no fetch tool.
	Fetch *FetchPolicy

	// MCP names the third-party MCP servers whose tools the gateway proxies.
	MCP []string

	State scenario.StateAccess

	// AllowUnsafe opens the proxied MCP tools the gateway marks unsafe.
	AllowUnsafe bool

	MaxToolCalls   int
	MaxResultBytes int64
}

// FetchPolicy is the fetch tool's policy: it exists only when the step or its
// profile allows fetching at all.
type FetchPolicy struct {
	Allow    []string
	MaxBytes int64
	MaxCalls int
}

// Defaults the runner applies when a step says nothing. The deny list is the
// one from spec section 7.3; the numbers are that section's example values,
// which are the only ones the spec states.
const (
	DefaultMaxToolCalls   = 60
	DefaultMaxResultBytes = 64 * 1024
	DefaultFetchMaxBytes  = 300 * 1024
	DefaultFetchMaxCalls  = 10
)

// DefaultFSDeny is prepended to a step's own deny list: paths no profile ever
// opens, since they hold credentials or the repository's own plumbing.
func DefaultFSDeny() []string {
	return []string{".git/**", ".env*", "**/*.pem", "**/id_*"}
}

// profileRow is the part of a profile the table in spec section 7.2 fixes.
// The columns it leaves as "by list" (apis, mcp) or as a plain yes/no on a
// list-shaped tool (fetch) are not here: those come from the step's tools
// block, which is the only place such a list can be written.
type profileRow struct {
	fsRead    bool
	fsWrite   scenario.FSWrite
	gitRead   bool
	gitCommit bool
	execMode  scenario.ExecMode
	state     scenario.StateAccess

	// fetch says whether the profile allows a fetch tool at all.
	fetch bool
}

var profileRows = map[scenario.Profile]profileRow{
	scenario.ProfileReview: {
		fsRead:   true,
		fsWrite:  scenario.FSWriteNone,
		gitRead:  true,
		execMode: scenario.ExecModeNone,
		state:    scenario.StateRead,
	},
	scenario.ProfileFix: {
		fsRead:    true,
		fsWrite:   scenario.FSWriteWorkspace,
		gitRead:   true,
		gitCommit: true,
		execMode:  scenario.ExecModeCommands,
		state:     scenario.StateRead,
	},
	scenario.ProfileResearch: {
		fsRead:   true,
		fsWrite:  scenario.FSWriteNone,
		gitRead:  true,
		execMode: scenario.ExecModeNone,
		state:    scenario.StateReadWrite,
		fetch:    true,
	},
}

// ResolvePolicy answers what the step may do. A step without a profile gets
// the review row, the most restrictive of the three, so that a missing
// profile can only narrow what an agent can reach, never widen it.
func ResolvePolicy(step *scenario.AgentStep) Policy {
	row, ok := profileRows[step.Profile]
	if !ok {
		row = profileRows[scenario.ProfileReview]
	}

	p := Policy{
		FSRead:         row.fsRead,
		FSWrite:        row.fsWrite,
		FSDeny:         DefaultFSDeny(),
		GitRead:        row.gitRead,
		GitCommit:      row.gitCommit,
		ExecMode:       row.execMode,
		State:          row.state,
		AllowUnsafe:    step.AllowUnsafe,
		MaxToolCalls:   DefaultMaxToolCalls,
		MaxResultBytes: DefaultMaxResultBytes,
	}
	if row.fetch {
		p.Fetch = &FetchPolicy{
			MaxBytes: DefaultFetchMaxBytes,
			MaxCalls: DefaultFetchMaxCalls,
		}
	}

	applyTools(&p, step.Tools)
	applyLimits(&p, step.Limits)
	return p
}

// applyTools overrides single fields of the profile's row, which is what the
// tools block is for: an absent field keeps what the profile said.
func applyTools(p *Policy, tools *scenario.Tools) {
	if tools == nil {
		return
	}

	if fs := tools.FS; fs != nil {
		if fs.Read != nil {
			p.FSRead = *fs.Read
		}
		if fs.Write != scenario.FSWriteUnset {
			p.FSWrite = fs.Write
		}
		p.FSDeny = append(p.FSDeny, fs.Deny...)
	}

	if git := tools.Git; git != nil {
		if git.Read != nil {
			p.GitRead = *git.Read
		}
		if git.Commit != nil {
			p.GitCommit = *git.Commit
		}
	}

	if exec := tools.Exec; exec != nil {
		if exec.Mode != scenario.ExecModeUnset {
			p.ExecMode = exec.Mode
		}
		p.ExecCommands = exec.Commands
	}

	p.APIs = tools.APIs
	p.MCP = tools.MCP

	// A fetch block names the hosts, so writing one is what turns the tool
	// on for a profile that has none; dropping the block cannot turn off a
	// profile that has one, since there would be no hosts to keep.
	if f := tools.Fetch; f != nil {
		if p.Fetch == nil {
			p.Fetch = &FetchPolicy{
				MaxBytes: DefaultFetchMaxBytes,
				MaxCalls: DefaultFetchMaxCalls,
			}
		}
		p.Fetch.Allow = f.Allow
		if f.MaxBytes > 0 {
			p.Fetch.MaxBytes = int64(f.MaxBytes)
		}
		if f.MaxCalls > 0 {
			p.Fetch.MaxCalls = f.MaxCalls
		}
	}

	if tools.State != scenario.StateUnset {
		p.State = tools.State
	}
}

// applyLimits overrides the gateway's caps; a zero field keeps the default,
// since a step that wants no calls at all has no reason to run.
func applyLimits(p *Policy, limits *scenario.Limits) {
	if limits == nil {
		return
	}
	if limits.MaxToolCalls > 0 {
		p.MaxToolCalls = limits.MaxToolCalls
	}
	if limits.MaxResultBytes > 0 {
		p.MaxResultBytes = int64(limits.MaxResultBytes)
	}
}
