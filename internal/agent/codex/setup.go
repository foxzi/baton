package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/foxzi/baton/internal/agent"
)

// serverName is how the gateway appears to the CLI: the key under
// mcp_servers in config.toml.
const serverName = "baton"

// bearerTokenVar is the environment variable config.toml points at for the
// gateway's bearer token. The token itself never goes into the file.
const bearerTokenVar = "BATON_GATEWAY_TOKEN"

// writeConfig writes the only configuration the CLI is given: a fresh
// config.toml in the run's own CODEX_HOME, naming nothing this step's
// policy does not already allow.
func writeConfig(home string, req agent.Request) error {
	var b strings.Builder

	// A step must never wait for a human.
	b.WriteString("approval_policy = \"never\"\n")

	writeMode := sandboxMode(req.Tools.FSWrite)
	// Unlike the claude-code adapter, the built-in shell tool cannot be
	// removed, so this sandbox mode is what keeps the step inside its
	// policy instead.
	fmt.Fprintf(&b, "sandbox_mode = %s\n", strconv.Quote(writeMode))
	if writeMode == "workspace-write" {
		b.WriteString("\n[sandbox_workspace_write]\n")
		// The network is the gateway's business, not the sandbox's.
		b.WriteString("network_access = false\n")
		fmt.Fprintf(&b, "writable_roots = [%s]\n", strconv.Quote(req.Workspace))
	}

	b.WriteString("\n[tools]\n")
	b.WriteString("web_search = false\n")

	b.WriteString("\n[shell_environment_policy]\n")
	// The step's own secrets go to the CLI process; "core" keeps them out
	// of the commands the agent spawns from inside the sandbox.
	b.WriteString("inherit = \"core\"\n")

	fmt.Fprintf(&b, "\n[mcp_servers.%s]\n", serverName)
	fmt.Fprintf(&b, "url = %s\n", strconv.Quote(req.Gateway.URL))
	if req.Gateway.Token != "" {
		fmt.Fprintf(&b, "bearer_token_env_var = %s\n", strconv.Quote(bearerTokenVar))
	}

	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("codex: write config.toml: %w", err)
	}
	return nil
}

// keptVars are the variables the CLI needs from the runner's own
// environment: its interpreter and tools have to be findable, and a proxy
// has to be reachable. Everything else the step needs is named in the
// scenario.
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

// environ builds the CLI's environment: a short allow list, a home
// directory of its own doubling as CODEX_HOME so that no user configuration
// or cached credential leaks into the run, and the step's own variables.
// OpenAI credentials therefore have to come from the step's env, the same
// way the claude-code adapter needs ANTHROPIC_API_KEY from it.
func environ(home string, gateway agent.GatewayInfo, extra map[string]string) []string {
	env := []string{
		"HOME=" + home,
		"CODEX_HOME=" + home,
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
	if gateway.Token != "" {
		env = append(env, bearerTokenVar+"="+gateway.Token)
	}
	return env
}
