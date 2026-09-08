package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
)

// FSOptions is what the runner hands over for one step's filesystem tools.
type FSOptions struct {
	// Workspace is the directory every path is relative to.
	Workspace string

	// Policy decides read, write, output size and denied paths (spec
	// section 7.3).
	Policy agent.Policy
}

// Defaults and caps for fs.glob and fs.grep. The spec gives no numbers for
// these two, so the values mirror git.log's shape: a small default with a
// hard ceiling well above it.
const (
	defaultFSGlobResults = 200
	maxFSGlobResults     = 1000

	defaultFSGrepMatches = 100
	maxFSGrepMatches     = 500

	// fsGrepLineBytes caps one returned match's line.
	fsGrepLineBytes = 512

	// fsSniffBytes is how much of a file's head is checked for a NUL byte
	// to tell binary content from text.
	fsSniffBytes = 8192

	// maxFSWriteBytes caps fs.write's content: large enough for source
	// files, small enough that one call cannot fill the disk.
	maxFSWriteBytes = 1 << 20
)

// FS is the filesystem tool set of one step.
type FS struct {
	workspace string
	read      bool
	write     bool
	deny      []string
	maxBytes  int64
}

// NewFS prepares the filesystem tools of a step. A step whose policy grants
// neither read nor write gets no tools, not an error: the profiles of
// section 7.2 leave both out on purpose for a step that only fetches.
func NewFS(opts FSOptions) (*FS, error) {
	write := opts.Policy.FSWrite == scenario.FSWriteWorkspace
	if !opts.Policy.FSRead && !write {
		return &FS{}, nil
	}
	if opts.Workspace == "" {
		return nil, errors.New("fs: no workspace")
	}

	maxBytes := opts.Policy.MaxResultBytes
	if maxBytes <= 0 {
		maxBytes = agent.DefaultMaxResultBytes
	}
	return &FS{
		workspace: opts.Workspace,
		// Writing implies reading: an agent that cannot see what it is
		// editing would edit blind.
		read:     opts.Policy.FSRead || write,
		write:    write,
		deny:     opts.Policy.FSDeny,
		maxBytes: maxBytes,
	}, nil
}

// Tools returns fs.read, fs.glob and fs.grep when the step may read the
// workspace, with fs.write appended last when it may also change it.
func (f *FS) Tools() []gateway.Tool {
	if !f.read {
		return nil
	}

	tools := []gateway.Tool{
		{
			Name:        "fs.read",
			Description: fsReadDescription,
			InputSchema: json.RawMessage(fsReadSchema),
			Handler:     f.readCall,
		},
		{
			Name:        "fs.glob",
			Description: fsGlobDescription,
			InputSchema: json.RawMessage(fsGlobSchema),
			Handler:     f.globCall,
		},
		{
			Name:        "fs.grep",
			Description: fsGrepDescription,
			InputSchema: json.RawMessage(fsGrepSchema),
			Handler:     f.grepCall,
		},
	}
	if f.write {
		tools = append(tools, gateway.Tool{
			Name:        "fs.write",
			Description: fsWriteDescription,
			InputSchema: json.RawMessage(fsWriteSchema),
			Handler:     f.writeCall,
		})
	}
	return tools
}

// FSFile is what fs.read returns.
type FSFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// FSGlobResult is what fs.glob returns.
type FSGlobResult struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// FSMatch is one line fs.grep found.
type FSMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// FSGrepResult is what fs.grep returns.
type FSGrepResult struct {
	Matches   []FSMatch `json:"matches"`
	Truncated bool      `json:"truncated"`
}

// FSWritten is what fs.write returns.
type FSWritten struct {
	Path    string `json:"path"`
	Size    int    `json:"size"`
	Created bool   `json:"created"`
}

func (f *FS) readCall(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "path", "max_bytes"); err != nil {
		return nil, err
	}

	rawPath, ok := given["path"]
	if !ok {
		return nil, errors.New(`argument "path" is required`)
	}
	cleaned, err := checkWorkspacePath("path", rawPath, f.deny)
	if err != nil {
		return nil, err
	}

	limit := f.maxBytes
	if value, ok := given["max_bytes"]; ok {
		n, err := checkFSByteLimit(value, f.maxBytes)
		if err != nil {
			return nil, err
		}
		limit = n
	}

	full, err := f.resolvePath("path", cleaned)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open %q: no such file", cleaned)
		}
		return nil, fmt.Errorf("open %q: %w", cleaned, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", cleaned, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("open %q: is a directory", cleaned)
	}

	head := make([]byte, fsSniffBytes)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	if bytes.IndexByte(head, 0) >= 0 {
		return FSFile{Path: cleaned, Size: info.Size(), Binary: true}, nil
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("open %q: %w", cleaned, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", cleaned, err)
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return FSFile{
		Path:      cleaned,
		Size:      info.Size(),
		Content:   string(data),
		Truncated: truncated,
	}, nil
}

func (f *FS) globCall(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "pattern", "max_results"); err != nil {
		return nil, err
	}

	pattern, ok := given["pattern"]
	if !ok {
		return nil, errors.New(`argument "pattern" is required`)
	}
	if err := checkWorkspaceGlob("pattern", pattern); err != nil {
		return nil, err
	}

	limit := defaultFSGlobResults
	if value, ok := given["max_results"]; ok {
		n, err := checkFSCount("max_results", value, maxFSGlobResults)
		if err != nil {
			return nil, err
		}
		limit = n
	}

	var paths []string
	truncated := false
	walkErr := filepath.WalkDir(f.workspace, func(p string, d iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == f.workspace {
				return walkErr
			}
			return nil
		}
		slash, err := f.relSlash(p)
		if err != nil {
			return err
		}
		if slash == "" {
			return nil
		}
		if d.IsDir() {
			if denied(f.deny, slash) {
				return filepath.SkipDir
			}
			return nil
		}
		if denied(f.deny, slash) || !d.Type().IsRegular() {
			// A symlink is skipped rather than followed: where it points
			// is not this walk's business, and it may point outside the
			// workspace entirely.
			return nil
		}
		if !matchGlob(pattern, slash) {
			return nil
		}
		if len(paths) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		paths = append(paths, slash)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("fs.glob: %w", walkErr)
	}

	sort.Strings(paths)
	return FSGlobResult{Paths: paths, Truncated: truncated}, nil
}

func (f *FS) grepCall(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "pattern", "glob", "max_matches"); err != nil {
		return nil, err
	}

	rawPattern, ok := given["pattern"]
	if !ok {
		return nil, errors.New(`argument "pattern" is required`)
	}
	if rawPattern == "" {
		return nil, errors.New(`argument "pattern" is empty`)
	}
	expr, err := regexp.Compile(rawPattern)
	if err != nil {
		return nil, fmt.Errorf("argument %q is not a valid regexp: %w", "pattern", err)
	}

	glob := given["glob"]
	if glob != "" {
		if err := checkWorkspaceGlob("glob", glob); err != nil {
			return nil, err
		}
	}

	limit := defaultFSGrepMatches
	if value, ok := given["max_matches"]; ok {
		n, err := checkFSCount("max_matches", value, maxFSGrepMatches)
		if err != nil {
			return nil, err
		}
		limit = n
	}

	var matches []FSMatch
	truncated := false
	walkErr := filepath.WalkDir(f.workspace, func(p string, d iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == f.workspace {
				return walkErr
			}
			return nil
		}
		slash, err := f.relSlash(p)
		if err != nil {
			return err
		}
		if slash == "" {
			return nil
		}
		if d.IsDir() {
			if denied(f.deny, slash) {
				return filepath.SkipDir
			}
			return nil
		}
		if denied(f.deny, slash) || !d.Type().IsRegular() {
			// Only a regular file is searched: following a symlink here
			// would read a file the workspace only points at.
			return nil
		}
		if glob != "" && !matchGlob(glob, slash) {
			return nil
		}
		if len(matches) >= limit {
			truncated = true
			return filepath.SkipAll
		}

		found, binary, ferr := grepFile(p, expr, limit-len(matches))
		if ferr != nil || binary {
			return nil
		}
		for _, m := range found {
			m.Path = slash
			matches = append(matches, m)
		}
		if len(matches) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("fs.grep: %w", walkErr)
	}

	return FSGrepResult{Matches: matches, Truncated: truncated}, nil
}

// grepFile searches one file for lines matching expr, stopping after want
// matches. binary is true when the file's head looks like it holds no text.
func grepFile(path string, expr *regexp.Regexp, want int) (matches []FSMatch, binary bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	head := make([]byte, fsSniffBytes)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, true, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false, err
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if !expr.MatchString(text) {
			continue
		}
		if len(text) > fsGrepLineBytes {
			text = text[:fsGrepLineBytes]
		}
		matches = append(matches, FSMatch{Line: line, Text: text})
		if len(matches) >= want {
			break
		}
	}
	return matches, false, scanner.Err()
}

func (f *FS) writeCall(ctx context.Context, raw json.RawMessage) (any, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownArgs(given, "path", "content"); err != nil {
		return nil, err
	}

	rawPath, ok := given["path"]
	if !ok {
		return nil, errors.New(`argument "path" is required`)
	}
	content, ok := given["content"]
	if !ok {
		return nil, errors.New(`argument "content" is required`)
	}
	if len(content) > maxFSWriteBytes {
		return nil, fmt.Errorf("argument %q is longer than %d bytes", "content", maxFSWriteBytes)
	}

	cleaned, err := checkWorkspacePath("path", rawPath, f.deny)
	if err != nil {
		return nil, err
	}
	full, err := f.resolvePath("path", cleaned)
	if err != nil {
		return nil, err
	}

	created := true
	if info, statErr := os.Lstat(full); statErr == nil {
		created = false
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return nil, fmt.Errorf("path %q is a symlink", cleaned)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("path %q is not a regular file", cleaned)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("fs.write: %s: %w", cleaned, statErr)
	}

	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("fs.write: create a directory for %s: %w", cleaned, err)
	}

	temp, err := os.CreateTemp(dir, filepath.Base(full)+".*")
	if err != nil {
		return nil, fmt.Errorf("fs.write: create a temporary file in %s: %w", dir, err)
	}
	name := temp.Name()
	defer os.Remove(name)

	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return nil, fmt.Errorf("fs.write: write %s: %w", cleaned, err)
	}
	if err := temp.Close(); err != nil {
		return nil, fmt.Errorf("fs.write: write %s: %w", cleaned, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return nil, fmt.Errorf("fs.write: %s: %w", cleaned, err)
	}
	if err := os.Rename(name, full); err != nil {
		return nil, fmt.Errorf("fs.write: replace %s: %w", cleaned, err)
	}

	return FSWritten{Path: cleaned, Size: len(content), Created: created}, nil
}

// relSlash turns an absolute path the walk visited into a workspace-relative
// slash path, or "" for the workspace root itself, which the walk visits
// but no tool ever reports as a match.
func (f *FS) relSlash(p string) (string, error) {
	rel, err := filepath.Rel(f.workspace, p)
	if err != nil {
		return "", fmt.Errorf("fs: %s: %w", p, err)
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// resolvePath turns a path checkWorkspacePath already cleared into the
// absolute path on disk, refusing one that would leave the workspace
// through a symlink. checkWorkspacePath's own check is purely lexical: a
// clean, non-absolute, ..-free path can still point outside the workspace
// once a symlink along it is followed.
func (f *FS) resolvePath(name, cleaned string) (string, error) {
	root, err := filepath.EvalSymlinks(f.workspace)
	if err != nil {
		return "", fmt.Errorf("fs: workspace %s: %w", f.workspace, err)
	}
	candidate := filepath.Join(f.workspace, filepath.FromSlash(cleaned))

	resolved, err := evalExisting(candidate)
	if err != nil {
		return "", fmt.Errorf("fs: %s: %w", cleaned, err)
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("argument %q leaves the workspace", name)
	}
	return candidate, nil
}

// evalExisting resolves the symlinks of a path that may not exist yet: it
// walks up to the nearest ancestor that does, resolves that, and rejoins
// the remaining components unresolved. This lets fs.write's target, which
// may still need to be created, be checked the same way as fs.read's,
// which must already exist.
func evalExisting(target string) (string, error) {
	resolved, err := filepath.EvalSymlinks(target)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(target)
	if parent == target {
		return "", err
	}
	resolvedParent, perr := evalExisting(parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(resolvedParent, filepath.Base(target)), nil
}

// checkFSCount holds max_results/max_matches to a positive number no larger
// than max.
func checkFSCount(name, value string, max int) (int, error) {
	if !digitsPattern.MatchString(value) {
		return 0, fmt.Errorf("argument %q must be decimal digits", name)
	}
	n, err := strconv.Atoi(value)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("argument %q must be a positive whole number", name)
	}
	if n > max {
		return 0, fmt.Errorf("argument %q must not be over %d", name, max)
	}
	return n, nil
}

// checkFSByteLimit holds fs.read's max_bytes to a positive number no larger
// than the ceiling, which is the step's own result size limit.
func checkFSByteLimit(value string, ceiling int64) (int64, error) {
	if !digitsPattern.MatchString(value) {
		return 0, errors.New(`argument "max_bytes" must be decimal digits`)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New(`argument "max_bytes" must be a positive whole number`)
	}
	if n > ceiling {
		return 0, fmt.Errorf(`argument "max_bytes" must not be over %d`, ceiling)
	}
	return n, nil
}

// Descriptions and schemas of the filesystem tools, hand-written in the same
// shape git.go uses, since these tools have no scenario-declared shape to
// build them from.
const (
	fsReadDescription = "Read a file's content from the workspace."
	fsReadSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"path":{"type":"string","description":"The workspace-relative path to read."},` +
		`"max_bytes":{"type":"string","description":"Read at most this many bytes, as decimal digits. Defaults to the step's result size limit."}` +
		`},"required":["path"]}`

	fsGlobDescription = "List workspace files whose path matches a glob pattern."
	fsGlobSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"pattern":{"type":"string","description":"A glob to match against workspace-relative paths, such as **/*.go."},` +
		`"max_results":{"type":"string","description":"Return at most this many paths, as decimal digits. Default 200, at most 1000."}` +
		`},"required":["pattern"]}`

	fsGrepDescription = "Search workspace files for lines matching a regular expression."
	fsGrepSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"pattern":{"type":"string","description":"A Go regular expression to search for."},` +
		`"glob":{"type":"string","description":"Limit the search to paths matching this glob, such as **/*.go."},` +
		`"max_matches":{"type":"string","description":"Stop after this many matches, as decimal digits. Default 100, at most 500."}` +
		`},"required":["pattern"]}`

	fsWriteDescription = "Write a file's content in the workspace, creating it or replacing it entirely."
	fsWriteSchema      = `{"type":"object","additionalProperties":false,"properties":{` +
		`"path":{"type":"string","description":"The workspace-relative path to write."},` +
		`"content":{"type":"string","description":"The file's new content, replacing whatever was there."}` +
		`},"required":["path","content"]}`
)
