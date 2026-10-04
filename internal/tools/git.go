package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	repos     map[string]bool
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
	repos := make(map[string]bool, len(opts.Policy.GitRepos))
	for _, repo := range opts.Policy.GitRepos {
		repos[repo] = true
	}
	return &Git{
		workspace: opts.Workspace,
		repos:     repos,
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
	if err := checkUnknownArgs(given, "repo"); err != nil {
		return nil, err
	}
	repo, err := g.repo(given)
	if err != nil {
		return nil, err
	}
	return repo.run(ctx, "status", "--porcelain=v1", "--branch")
}

func (g *Git) diff(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path", "repo"); err != nil {
		return nil, err
	}
	repo, err := g.repo(given)
	if err != nil {
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
		cleaned, err := repo.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--", cleaned)
	}
	return repo.run(ctx, argv...)
}

func (g *Git) log(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path", "max_count", "repo"); err != nil {
		return nil, err
	}
	repo, err := g.repo(given)
	if err != nil {
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
		cleaned, err := repo.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--", cleaned)
	}
	return repo.run(ctx, argv...)
}

func (g *Git) show(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path", "repo"); err != nil {
		return nil, err
	}
	repo, err := g.repo(given)
	if err != nil {
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
		cleaned, err := repo.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		target = ref + ":" + cleaned
	}
	return repo.run(ctx, "show", target)
}

// blame is git.blame's handler: who last touched every line of one file.
func (g *Git) blame(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "ref", "path", "repo"); err != nil {
		return nil, err
	}
	repo, err := g.repo(given)
	if err != nil {
		return nil, err
	}

	rawPath, ok := given["path"]
	if !ok {
		return nil, errors.New(`argument "path" is required`)
	}
	cleaned, err := repo.checkPath("path", rawPath)
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
	return repo.run(ctx, argv...)
}

// commitCall is git.commit's handler.
func (g *Git) commitCall(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "message", "path", "repo"); err != nil {
		return nil, err
	}
	repo, err := g.repo(given)
	if err != nil {
		return nil, err
	}

	message, ok := given["message"]
	if !ok {
		return nil, errors.New(`argument "message" is required`)
	}
	if err := checkCommitMessage(message); err != nil {
		return nil, err
	}

	pathspec := "."
	if raw, ok := given["path"]; ok {
		cleaned, err := repo.checkPath("path", raw)
		if err != nil {
			return nil, err
		}
		pathspec = cleaned
	}

	// commit --all and commit -- <path> leave untracked files out, and a new
	// test is what most fixes add; so the new files are added first. The
	// list comes from git with .gitignore applied, and the deny list is
	// applied here, with the same matcher the fs tools use.
	listed, err := repo.run(ctx, "ls-files", "--others", "--exclude-standard", "-z", "--", pathspec)
	if err != nil {
		return nil, err
	}
	resp, ok := listed.(Response)
	if !ok || resp.ExitCode != 0 {
		return listed, nil
	}
	if resp.Truncated {
		return nil, errors.New("too many new files to commit at once; commit them by path")
	}
	add := []string{"add", "--"}
	for _, name := range strings.Split(resp.Stdout, "\x00") {
		if name != "" && !repo.denied(name) {
			add = append(add, name)
		}
	}
	if len(add) > 2 {
		staged, err := repo.run(ctx, add...)
		if err != nil {
			return nil, err
		}
		if resp, ok := staged.(Response); ok && resp.ExitCode != 0 {
			return resp, nil
		}
	}

	argv := []string{"commit", "--all", "--message", message}
	if pathspec != "." {
		argv = []string{"commit", "--message", message, "--", pathspec}
	}
	return repo.run(ctx, argv...)
}

// run executes one git call and turns it into the answer the agent sees. A
// non-zero exit code is data, exactly as for the commands of spec section
// 7.5: a git error message ("not a valid revision") is what the agent needs
// to read, not something the runner should swallow. Only a timeout, a
// cancelled context or a git binary that could not be started at all come
// back as an error.
func (r gitRepo) run(ctx context.Context, argv ...string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	stdout := &cappedBuffer{limit: r.maxBytes}
	stderr := &cappedBuffer{limit: r.maxBytes}

	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = r.dir
	cmd.Env = gitEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// git never waits for input in any of these calls, but a hung prompt
	// would otherwise sit there until the timeout.
	cmd.Stdin = strings.NewReader("")
	Isolate(cmd)

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
		return gateway.Refusef("argument %q looks like an option, not a revision", name)
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
// the deny list of spec section 7.3. checkWorkspacePath in paths.go is the
// shared implementation; fs.go calls it too.
func (r gitRepo) checkPath(name, value string) (string, error) {
	cleaned, err := checkWorkspacePath(name, value, r.deny)
	if err != nil {
		return "", err
	}
	if r.denied(cleaned) {
		return "", gateway.Refusef("path %q is denied", cleaned)
	}
	return cleaned, nil
}

// gitRepo is the repository one call runs in: the workspace itself, or one
// of the nested repositories the step's policy lists in git.repos.
type gitRepo struct {
	dir      string
	prefix   string
	deny     []string
	maxBytes int64
}

// repo picks the repository named by the call's repo argument. A repository
// outside the policy's list is refused: the list is what keeps the tools
// away from any other checkout that happens to sit in the workspace.
func (g *Git) repo(given map[string]string) (gitRepo, error) {
	r := gitRepo{dir: g.workspace, deny: g.deny, maxBytes: g.maxBytes}
	name, ok := given["repo"]
	if !ok || name == "." {
		return r, nil
	}
	if !g.repos[name] {
		return gitRepo{}, gateway.Refusef("repository %q is not one of the step's git.repos", name)
	}
	r.dir = filepath.Join(g.workspace, name)
	r.prefix = name + "/"
	return r, nil
}

// denied applies the deny list to a path relative to the repository twice:
// as it is, so that .git/** and .env* hold in a nested repository too, and
// as a workspace path, which is what a step writes its own patterns against.
func (r gitRepo) denied(name string) bool {
	return denied(r.deny, name) || denied(r.deny, r.prefix+name)
}

// checkMaxCount holds git.log's max_count to a positive number no larger
// than maxGitLogCount.
func checkMaxCount(value string) (int, error) {
	if !digitsPattern.MatchString(value) {
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
	gitStatusSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		gitRepoProperty +
		`}}`

	gitDiffDescription = "Show the working tree's changes, optionally against a revision and limited to one path."
	gitDiffSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"ref":{"type":"string","description":"A revision or range to diff against, such as HEAD~1 or main...HEAD."},` +
		`"path":{"type":"string","description":"Limit the diff to this workspace-relative path."},` +
		gitRepoProperty +
		`}}`

	gitLogDescription = "Show the commit history, optionally starting from a revision and limited to a path."
	gitLogSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"ref":{"type":"string","description":"A revision or range to start from, such as HEAD or main..feature."},` +
		`"path":{"type":"string","description":"Limit the history to this workspace-relative path."},` +
		`"max_count":{"type":"string","description":"How many commits to show, as decimal digits. Default 20, at most 200."},` +
		gitRepoProperty +
		`}}`

	gitShowDescription = "Show a commit, or a file's content as of a revision."
	gitShowSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"ref":{"type":"string","description":"The revision to show."},` +
		`"path":{"type":"string","description":"A workspace-relative path to show as of that revision, instead of the whole commit."},` +
		gitRepoProperty +
		`},"required":["ref"]}`

	gitBlameDescription = "Show who last changed every line of a file, with the commit and date."
	gitBlameSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"path":{"type":"string","description":"The workspace-relative path to blame."},` +
		`"ref":{"type":"string","description":"Blame the file as of this revision, instead of the working tree."},` +
		gitRepoProperty +
		`},"required":["path"]}`

	gitCommitDescription = "Commit the working tree's changes, new files included, optionally limited to one path."
	gitCommitSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"message":{"type":"string","description":"The commit message."},` +
		`"path":{"type":"string","description":"Limit the commit to this workspace-relative path, instead of every change."},` +
		gitRepoProperty +
		`},"required":["message"]}`

	// gitRepoProperty is the repo argument every git tool takes. Paths in
	// a call that names a repository are relative to that repository.
	gitRepoProperty = `"repo":{"type":"string","description":"Run in this nested repository, one of the step's git.repos, instead of the workspace; paths are then relative to it."}`
)
