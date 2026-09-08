package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
)

// GitOptions is what the runner hands over for one step's git tools.
type GitOptions struct {
	// Workspace is the repository root; every git call runs there.
	Workspace string

	// Policy decides read, commit, output size and denied paths (spec
	// section 7.3).
	Policy agent.Policy
}

// gitTimeout is one call's own time limit. There is no shared budget to
// borrow from, unlike a scenario command: a git call is read-only or a
// single commit, never a build.
const gitTimeout = 30 * time.Second

// Defaults for git.log when the call omits max_count.
const (
	defaultGitLogCount = 20
	maxGitLogCount     = 200
)

// Git is the git tool set of one step.
type Git struct {
	workspace string
	read      bool
	commit    bool
	deny      []string
	maxBytes  int64
}

// NewGit prepares the git tools of a step. A step whose policy grants
// neither read nor commit gets no tools, not an error: the profiles of
// section 7.2 leave git out on purpose for a research step that only
// fetches.
func NewGit(opts GitOptions) (*Git, error) {
	if !opts.Policy.GitRead && !opts.Policy.GitCommit {
		return &Git{}, nil
	}
	if opts.Workspace == "" {
		return nil, errors.New("git: no workspace")
	}

	maxBytes := opts.Policy.MaxResultBytes
	if maxBytes <= 0 {
		maxBytes = agent.DefaultMaxResultBytes
	}
	return &Git{
		workspace: opts.Workspace,
		// Committing implies reading: an agent that cannot see what it
		// changed would commit blind.
		read:     opts.Policy.GitRead || opts.Policy.GitCommit,
		commit:   opts.Policy.GitCommit,
		deny:     opts.Policy.FSDeny,
		maxBytes: maxBytes,
	}, nil
}

// Tools returns status, diff, log and show when the step may read the
// repository, with commit appended last when the step may also change it.
func (g *Git) Tools() []gateway.Tool {
	if !g.read {
		return nil
	}

	tools := []gateway.Tool{
		{
			Name:        "git.status",
			Description: gitStatusDescription,
			InputSchema: json.RawMessage(gitStatusSchema),
			Handler:     g.status,
		},
		{
			Name:        "git.diff",
			Description: gitDiffDescription,
			InputSchema: json.RawMessage(gitDiffSchema),
			Handler:     g.diff,
		},
		{
			Name:        "git.log",
			Description: gitLogDescription,
			InputSchema: json.RawMessage(gitLogSchema),
			Handler:     g.log,
		},
		{
			Name:        "git.show",
			Description: gitShowDescription,
			InputSchema: json.RawMessage(gitShowSchema),
			Handler:     g.show,
		},
		{
			Name:        "git.blame",
			Description: gitBlameDescription,
			InputSchema: json.RawMessage(gitBlameSchema),
			Handler:     g.blame,
		},
	}
	if g.commit {
		tools = append(tools, gateway.Tool{
			Name:        "git.commit",
			Description: gitCommitDescription,
			InputSchema: json.RawMessage(gitCommitSchema),
			Handler:     g.commitCall,
		})
	}
	return tools
}

func (g *Git) status(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given); err != nil {
		return nil, err
	}
	return g.run(ctx, "status", "--porcelain=v1", "--branch")
}

func (g *Git) diff(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path"); err != nil {
		return nil, err
	}

	argv := []string{"diff"}
	if ref, ok := given["ref"]; ok {
		if err := checkRevision("ref", ref); err != nil {
			return nil, err
		}
		argv = append(argv, ref)
	}
	if raw, ok := given["path"]; ok {
		cleaned, err := g.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--", cleaned)
	}
	return g.run(ctx, argv...)
}

func (g *Git) log(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path", "max_count"); err != nil {
		return nil, err
	}

	count := defaultGitLogCount
	if value, ok := given["max_count"]; ok {
		n, err := checkMaxCount(value)
		if err != nil {
			return nil, err
		}
		count = n
	}

	argv := []string{
		"log",
		fmt.Sprintf("--max-count=%d", count),
		"--date=iso",
		"--pretty=format:%H %ad %an %s",
	}
	if ref, ok := given["ref"]; ok {
		if err := checkRevision("ref", ref); err != nil {
			return nil, err
		}
		argv = append(argv, ref)
	}
	if raw, ok := given["path"]; ok {
		cleaned, err := g.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--", cleaned)
	}
	return g.run(ctx, argv...)
}

func (g *Git) show(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path"); err != nil {
		return nil, err
	}

	ref, ok := given["ref"]
	if !ok {
		return nil, errors.New(`argument "ref" is required`)
	}
	if err := checkRevision("ref", ref); err != nil {
		return nil, err
	}

	target := ref
	if raw, ok := given["path"]; ok {
		cleaned, err := g.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		target = ref + ":" + cleaned
	}
	return g.run(ctx, "show", target)
}

// blame is git.blame's handler: who last touched every line of one file.
func (g *Git) blame(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path"); err != nil {
		return nil, err
	}

	rawPath, ok := given["path"]
	if !ok {
		return nil, errors.New(`argument "path" is required`)
	}
	cleaned, err := g.checkPath("path", rawPath)
	if err != nil {
		return nil, err
	}

	argv := []string{"blame", "--date=iso"}
	if ref, ok := given["ref"]; ok {
		if err := checkRevision("ref", ref); err != nil {
			return nil, err
		}
		argv = append(argv, ref)
	}
	argv = append(argv, "--", cleaned)
	return g.run(ctx, argv...)
}

// commitCall is git.commit's handler.
func (g *Git) commitCall(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "message", "path"); err != nil {
		return nil, err
	}

	message, ok := given["message"]
	if !ok {
		return nil, errors.New(`argument "message" is required`)
	}
	if err := checkCommitMessage(message); err != nil {
		return nil, err
	}

	var argv []string
	if raw, ok := given["path"]; ok {
		cleaned, err := g.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		argv = []string{"commit", "--message", message, "--", cleaned}
	} else {
		argv = []string{"commit", "--all", "--message", message}
	}
	return g.run(ctx, argv...)
}

// run executes one git call and turns it into the answer the agent sees. A
// non-zero exit code is data, exactly as for the commands of spec section
// 7.5: a git error message ("not a valid revision") is what the agent needs
// to read, not something the runner should swallow. Only a timeout, a
// cancelled context or a git binary that could not be started at all come
// back as an error.
func (g *Git) run(ctx context.Context, argv ...string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	stdout := &cappedBuffer{limit: g.maxBytes}
	stderr := &cappedBuffer{limit: g.maxBytes}

	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = g.workspace
	cmd.Env = gitEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// git never waits for input in any of these calls, but a hung prompt
	// would otherwise sit there until the timeout.
	cmd.Stdin = strings.NewReader("")
	isolate(cmd)
	cmd.Cancel = func() error { return terminate(cmd) }
	// The last resort if the process ignores the signal: the pipes are
	// closed and the wait ends, whatever git is still doing.
	cmd.WaitDelay = terminateGrace

	started := time.Now()
	runErr := cmd.Run()
	spent := time.Since(started)

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("git %s: no answer within %s", argv[0], gitTimeout)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("git %s: %w", argv[0], ctx.Err())
	case runErr != nil && cmd.ProcessState == nil:
		// git never ran: a missing executable is the runner's problem, and
		// the agent cannot fix it by trying again.
		return nil, fmt.Errorf("git cannot be run: %w", runErr)
	}

	return Response{
		ExitCode:   exitCodeOf(cmd),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		Truncated:  stdout.truncated() || stderr.truncated(),
		DurationMS: spent.Milliseconds(),
	}, nil
}

// gitEnv builds git's environment from a minimal allow list: enough to find
// the binary and know the user's identity for a commit, and nothing that
// carries a credential.
func gitEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	for _, name := range []string{"PATH", "HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// checkUnknownArgs refuses a call that names an argument none of these
// tools declare. sortedNames keeps the order of the errors it reports
// stable.
func checkUnknownArgs(given map[string]string, allowed ...string) error {
	allow := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allow[name] = true
	}

	var problems []error
	for _, name := range sortedNames(given) {
		if !allow[name] {
			problems = append(problems, fmt.Errorf("unknown argument %q", name))
		}
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	return nil
}

// revisionPattern is what a git revision is allowed to look like: this is
// defence, not a full grammar for git's own revision syntax.
var revisionPattern = regexp.MustCompile(`\A[A-Za-z0-9._/~^@{}!-]+\z`)

// checkRevision holds a revision to what is safe to pass as one argv entry.
// A revision legitimately contains .. (HEAD~2, main...HEAD), which is why
// this does not reuse checkValue from args.go: that helper rejects .. on
// purpose, for the different job of holding a path to the workspace.
func checkRevision(name, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("argument %q is empty", name)
	case len(value) > 200:
		return fmt.Errorf("argument %q is longer than 200 bytes", name)
	case strings.HasPrefix(value, "-"):
		// A value starting with - would be read as an option, not a
		// revision, by every one of these git subcommands.
		return fmt.Errorf("argument %q looks like an option, not a revision", name)
	case strings.Contains(value, "//"):
		return fmt.Errorf("argument %q must not contain //", name)
	case strings.ContainsAny(value, "\x00\n "):
		return fmt.Errorf("argument %q must not contain a space or a control character", name)
	case !revisionPattern.MatchString(value):
		return fmt.Errorf("argument %q does not look like a revision", name)
	}
	return nil
}

// checkPath holds a workspace-relative path to what git may be asked to
// touch: no leading slash, no .. segment, no control character, and not on
// the deny list of spec section 7.3.
func (g *Git) checkPath(name, value string) (string, error) {
	switch {
	case value == "":
		return "", fmt.Errorf("argument %q is empty", name)
	case len(value) > 1024:
		return "", fmt.Errorf("argument %q is longer than 1024 bytes", name)
	case strings.HasPrefix(value, "/"):
		return "", fmt.Errorf("argument %q must not be an absolute path", name)
	case strings.ContainsAny(value, "\x00\n"):
		return "", fmt.Errorf("argument %q must be a single line", name)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("argument %q must not contain ..", name)
		}
	}

	cleaned := path.Clean(value)
	if denied(g.deny, cleaned) {
		return "", fmt.Errorf("path %q is denied", cleaned)
	}
	return cleaned, nil
}

// maxCountPattern is what git.log's max_count argument must look like:
// decimal digits, nothing else, so that a value such as "-1" or "1e9" is
// refused before it reaches strconv.Atoi.
var maxCountPattern = regexp.MustCompile(`\A[0-9]+\z`)

// checkMaxCount holds git.log's max_count to a positive number no larger
// than maxGitLogCount.
func checkMaxCount(value string) (int, error) {
	if !maxCountPattern.MatchString(value) {
		return 0, errors.New(`argument "max_count" must be decimal digits`)
	}
	n, err := strconv.Atoi(value)
	if err != nil || n == 0 {
		return 0, errors.New(`argument "max_count" must be a positive whole number`)
	}
	if n > maxGitLogCount {
		return 0, fmt.Errorf(`argument "max_count" must not be over %d`, maxGitLogCount)
	}
	return n, nil
}

// checkCommitMessage holds git.commit's message to what is safe as a single
// argv entry: spaces and newlines are fine there, only a NUL and an
// excessive length are not.
func checkCommitMessage(value string) error {
	switch {
	case value == "":
		return errors.New(`argument "message" is empty`)
	case len(value) > 4096:
		return errors.New(`argument "message" is longer than 4096 bytes`)
	case strings.Contains(value, "\x00"):
		return errors.New(`argument "message" must not contain a NUL byte`)
	}
	return nil
}

// Descriptions and schemas of the git tools. The description is what the
// agent picks a tool by (spec section 7.4.8); the schema is hand-written in
// the same shape the rest of this package uses, rather than built from a Go
// value, since these tools have no scenario-declared shape to read it from.
const (
	gitStatusDescription = "Show the working tree's status: modified, staged and untracked files, with the current branch."
	gitStatusSchema      = `{"type":"object","additionalProperties":false,"properties":{}}`

	gitDiffDescription = "Show the working tree's changes, optionally against a revision and limited to one path."
	gitDiffSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"ref":{"type":"string","description":"A revision or range to diff against, such as HEAD~1 or main...HEAD."},` +
		`"path":{"type":"string","description":"Limit the diff to this workspace-relative path."}` +
		`}}`

	gitLogDescription = "Show the commit history, optionally starting from a revision and limited to a path."
	gitLogSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"ref":{"type":"string","description":"A revision or range to start from, such as HEAD or main..feature."},` +
		`"path":{"type":"string","description":"Limit the history to this workspace-relative path."},` +
		`"max_count":{"type":"string","description":"How many commits to show, as decimal digits. Default 20, at most 200."}` +
		`}}`

	gitShowDescription = "Show a commit, or a file's content as of a revision."
	gitShowSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"ref":{"type":"string","description":"The revision to show."},` +
		`"path":{"type":"string","description":"A workspace-relative path to show as of that revision, instead of the whole commit."}` +
		`},"required":["ref"]}`

	gitBlameDescription = "Show who last changed every line of a file, with the commit and date."
	gitBlameSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"path":{"type":"string","description":"The workspace-relative path to blame."},` +
		`"ref":{"type":"string","description":"Blame the file as of this revision, instead of the working tree."}` +
		`},"required":["path"]}`

	gitCommitDescription = "Commit the working tree's changes, optionally limited to one path."
	gitCommitSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"message":{"type":"string","description":"The commit message."},` +
		`"path":{"type":"string","description":"Limit the commit to this workspace-relative path, instead of every change."}` +
		`},"required":["message"]}`
)
