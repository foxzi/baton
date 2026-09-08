package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/scenario"
)

// serverName is how the gateway appears to the CLI, and so the prefix of
// every tool name the step's own tools get: mcp__baton__<tool>.
const serverName = "baton"

// writeMCPConfig writes the only MCP configuration the CLI is given: the
// gateway's endpoint for this step, with the run's bearer token.
func writeMCPConfig(dir string, info agent.GatewayInfo) (string, error) {
	type server struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers,omitempty"`
	}

	entry := server{Type: "http", URL: info.URL}
	if info.Token != "" {
		entry.Headers = map[string]string{"Authorization": "Bearer " + info.Token}
	}
	config := map[string]any{"mcpServers": map[string]server{serverName: entry}}

	path := filepath.Join(dir, "mcp.json")
	if err := writeJSON(path, config); err != nil {
		return "", fmt.Errorf("claude-code: write the MCP configuration: %w", err)
	}
	return path, nil
}

// writeSettings writes the settings file that carries the step's deny rules.
// Deny is what the CLI's permission system always honours, whatever an allow
// rule elsewhere says, so the policy is expressed as denials.
func writeSettings(dir string, policy agent.Policy) (string, error) {
	config := map[string]any{
		"permissions": map[string]any{"deny": denyRules(policy)},
	}

	path := filepath.Join(dir, "settings.json")
	if err := writeJSON(path, config); err != nil {
		return "", fmt.Errorf("claude-code: write the settings: %w", err)
	}
	return path, nil
}

// denyRules turns the policy into permission rules for the settings file.
// The path rules guard files inside the workspace, which the file tools can
// otherwise reach.
func denyRules(policy agent.Policy) []string {
	denials := toolDenials(policy)
	rules := make([]string, 0, len(policy.FSDeny)*2+len(denials))
	for _, pattern := range policy.FSDeny {
		rules = append(rules, "Read("+pattern+")", "Edit("+pattern+")")
	}
	return append(rules, denials...)
}

// toolDenials names the tools the step must not have at all. These go on the
// command line as well as into the settings, because a tool name carries no
// pattern and so survives the flag's comma-separated list intact.
func toolDenials(policy agent.Policy) []string {
	denials := deniedTools
	if policy.FSWrite != scenario.FSWriteWorkspace {
		denials = append(append([]string{}, denials...), writeTools...)
	}
	return denials
}

// deniedTools are the CLI's built-in tools no profile ever opens: running
// commands and reaching the network are the gateway's business, and a
// sub-agent would run outside the step's policy.
var deniedTools = []string{"Bash", "BashOutput", "KillShell", "WebFetch", "WebSearch", "Task"}

// writeTools are the built-ins that change files.
var writeTools = []string{"Edit", "Write", "NotebookEdit"}

// readTools are the built-ins that only look at files.
var readTools = []string{"Read", "Glob", "Grep"}

// builtinTools is the built-in set the step gets, for --tools.
func builtinTools(policy agent.Policy) []string {
	var tools []string
	if policy.FSRead {
		tools = append(tools, readTools...)
	}
	if policy.FSWrite == scenario.FSWriteWorkspace {
		// Writing needs reading: an edit the agent cannot read back is a
		// blind edit.
		if !policy.FSRead {
			tools = append(tools, readTools...)
		}
		tools = append(tools, writeTools...)
	}
	return tools
}

// allowedTools are the tools the agent may use without asking: its built-ins
// plus every tool the gateway serves.
func allowedTools(policy agent.Policy) []string {
	// A whole server can be named at once, and the gateway serves nothing
	// the step's policy does not allow.
	return append(builtinTools(policy), "mcp__"+serverName)
}

// linkedSkills are the skill links made for one run, so they can be taken
// back out of the workspace afterwards.
type linkedSkills []string

// linkSkills makes the step's skills visible to the CLI, which looks for them
// under .claude/skills in the working directory. A name the workspace already
// uses is left alone: the workspace's own copy wins, and nothing of the
// user's is overwritten or removed.
func linkSkills(workspace string, skills []string) (linkedSkills, error) {
	if len(skills) == 0 {
		return nil, nil
	}

	root := filepath.Join(workspace, ".claude", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("claude-code: create the skills directory: %w", err)
	}

	var linked linkedSkills
	for _, skill := range skills {
		source, err := filepath.Abs(skill)
		if err != nil {
			linked.remove()
			return nil, fmt.Errorf("claude-code: resolve the skill %q: %w", skill, err)
		}

		link := filepath.Join(root, filepath.Base(source))
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(source, link); err != nil {
			linked.remove()
			return nil, fmt.Errorf("claude-code: link the skill %q: %w", skill, err)
		}
		linked = append(linked, link)
	}
	return linked, nil
}

// remove takes the links back out, leaving the workspace as it was found.
func (l linkedSkills) remove() {
	for _, link := range l {
		_ = os.Remove(link)
	}
}

// keptVars are the variables the CLI needs from the runner's own environment:
// its interpreter and tools have to be findable, and a proxy has to be
// reachable. Everything else the step needs is named in the scenario.
var keptVars = []string{
	"PATH",
	"LANG",
	"TZ",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

// environ builds the CLI's environment: a short allow list, a home directory
// of its own so that no user configuration or cached credential leaks into
// the run, and the step's own variables. Anthropic credentials therefore have
// to come from the step's env (spec section 8.2).
func environ(home string, extra map[string]string) []string {
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		// Telemetry would be one more thing talking to the network from
		// inside a step.
		"CLAUDE_CODE_ENABLE_TELEMETRY=0",
		"DISABLE_TELEMETRY=1",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_ERROR_REPORTING=1",
		"CI=1",
	}
	for _, name := range keptVars {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for name, value := range extra {
		env = append(env, name+"="+value)
	}
	return env
}

// writeJSON writes a configuration file the CLI reads. The files hold a
// per-run token, so they are readable by their owner only.
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// commaList joins tool names the way the CLI's tool flags accept them.
func commaList(tools []string) string {
	return strings.Join(tools, ",")
}
