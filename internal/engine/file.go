package engine

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/tools"
)

// defaultFileMaxBytes caps a file read when the step does not set max_bytes.
const defaultFileMaxBytes = 1 << 20

// execFile reads, writes, appends or lists files inside the workspace. It is
// the declarative counterpart of shelling out to cat, tee or find from a run
// step, and it stays inside the workspace: everything else needs run.
func (e *Engine) execFile(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	if stepErr := fileContextError(ctx, step); stepErr != nil {
		return expr.Step{}, stepErr
	}

	op, rawTarget := step.File.Op()
	if op == scenario.FileOpNone {
		return expr.Step{}, errorf(ClassConfig, "step %s: file: exactly one of read, write, append, glob", step.ID)
	}
	target, stepErr := e.render(fmt.Sprintf("%s.file.%s", step.ID, op), rawTarget)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	cleaned, err := checkFilePath(target)
	if err != nil {
		return expr.Step{}, errorf(ClassConfig, "step %s: file.%s: %v", step.ID, op, err)
	}

	if op == scenario.FileOpGlob {
		return e.fileGlob(step, path, cleaned)
	}
	if op == scenario.FileOpRead {
		return e.fileRead(step, path, cleaned)
	}
	return e.fileWrite(step, path, op, cleaned)
}

// fileRead reads a file and turns its content into the step result, with the
// parse modes of a run step (section 3.3).
func (e *Engine) fileRead(step *scenario.Step, path, cleaned string) (expr.Step, *Error) {
	limit := step.File.MaxBytes.Bytes()
	if limit <= 0 {
		limit = defaultFileMaxBytes
	}
	e.writeStepJSON(path, "input.json", map[string]any{
		"op":        string(scenario.FileOpRead),
		"path":      cleaned,
		"parse":     string(step.File.Parse),
		"max_bytes": limit,
	})

	full, err := resolveFilePath(e.opts.Workspace, cleaned)
	if err != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassConfig, "file.read %s: %v", cleaned, err)
	}
	info, err := os.Stat(full)
	if err != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.read %s: %v", cleaned, err)
	}
	if !info.Mode().IsRegular() {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.read %s: not a regular file", cleaned)
	}
	if info.Size() > limit {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.read %s: %d bytes exceed max_bytes %d", cleaned, info.Size(), limit)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.read %s: %v", cleaned, err)
	}

	result, stepErr := parseOutput(step.ID, step.File.Parse, string(data))
	if stepErr != nil {
		e.writeFileOutput(path, expr.StatusFailed, nil, stepErr)
		return expr.Step{}, stepErr
	}
	out := expr.Step{Result: result}
	e.writeFileOutput(path, expr.StatusSuccess, out.Result, nil)
	return out, nil
}

// fileWrite writes or appends rendered content, creating the parent
// directories on the way.
func (e *Engine) fileWrite(step *scenario.Step, path string, op scenario.FileOp, cleaned string) (expr.Step, *Error) {
	content, stepErr := e.render(fmt.Sprintf("%s.file.content", step.ID), step.File.Content)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	// The content itself stays out of input.json: it may be large and it is
	// already on disk under the path the step wrote.
	e.writeStepJSON(path, "input.json", map[string]any{
		"op":    string(op),
		"path":  cleaned,
		"bytes": len(content),
	})

	full, err := resolveFilePath(e.opts.Workspace, cleaned)
	if err != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassConfig, "file.%s %s: %v", op, cleaned, err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.%s %s: %v", op, cleaned, err)
	}
	if err := writeFileContent(full, op, content); err != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.%s %s: %v", op, cleaned, err)
	}

	out := expr.Step{Result: map[string]any{"path": cleaned, "bytes": len(content)}}
	e.writeFileOutput(path, expr.StatusSuccess, out.Result, nil)
	return out, nil
}

// writeFileContent truncates for write and adds to the end for append.
func writeFileContent(full string, op scenario.FileOp, content string) error {
	if op == scenario.FileOpWrite {
		return os.WriteFile(full, []byte(content), 0o644)
	}
	file, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// fileGlob lists the workspace files matching a pattern, where * stays inside
// one segment and ** crosses segments, as fs.glob does (section 7.3).
func (e *Engine) fileGlob(step *scenario.Step, path, pattern string) (expr.Step, *Error) {
	e.writeStepJSON(path, "input.json", map[string]any{
		"op":   string(scenario.FileOpGlob),
		"path": pattern,
	})

	workspace := e.opts.Workspace
	var paths []string
	walkErr := filepath.WalkDir(workspace, func(p string, d iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Only the workspace itself is fatal; an unreadable directory
			// deeper in the tree is skipped.
			if p == workspace {
				return walkErr
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// A symlink is skipped rather than followed: where it points is not
		// this walk's business, and it may point outside the workspace.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(workspace, p)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if tools.MatchGlob(pattern, slash) {
			paths = append(paths, slash)
		}
		return nil
	})
	if walkErr != nil {
		return expr.Step{}, e.fileFailure(step, path, ClassCommand, "file.glob %s: %v", pattern, walkErr)
	}

	sort.Strings(paths)
	items := make([]any, 0, len(paths))
	for _, p := range paths {
		items = append(items, p)
	}
	out := expr.Step{Result: items}
	e.writeFileOutput(path, expr.StatusSuccess, out.Result, nil)
	return out, nil
}

// fileFailure records a failed file step and returns the classified error.
func (e *Engine) fileFailure(step *scenario.Step, path, class, format string, args ...any) *Error {
	stepErr := errorf(class, "step %s: "+format, append([]any{step.ID}, args...)...)
	e.writeFileOutput(path, expr.StatusFailed, nil, stepErr)
	return stepErr
}

func (e *Engine) writeFileOutput(path, status string, result any, stepErr *Error) {
	output := map[string]any{"status": status}
	if stepErr != nil {
		output["error"] = map[string]any{"class": stepErr.Class, "message": stepErr.Msg}
	} else {
		output["result"] = result
	}
	e.writeStepJSON(path, "output.json", output)
}

// fileContextError reports a deadline or a cancellation that already happened
// before the step touched the filesystem, with the wording of a run step.
func fileContextError(ctx context.Context, step *scenario.Step) *Error {
	err := ctx.Err()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Class: ClassTimeout, Msg: fmt.Sprintf("step %s: timed out", step.ID), Err: err}
	default:
		return &Error{Class: ClassConfig, Msg: fmt.Sprintf("step %s: cancelled", step.ID), Err: err}
	}
}

// checkFilePath holds a file step's path to the workspace: relative, no ..
// segment, a single line, at most 1024 bytes. The same rule as the fs tools
// of section 7.3, without their deny list: a scenario is written by hand and
// may name any file of the workspace, a model may not.
func checkFilePath(value string) (string, error) {
	switch {
	case strings.TrimSpace(value) == "":
		return "", errors.New("must not be empty")
	case len(value) > 1024:
		return "", errors.New("must not be longer than 1024 bytes")
	case strings.HasPrefix(value, "/"):
		return "", errors.New("must be relative to the workspace")
	case strings.ContainsAny(value, "\x00\n"):
		return "", errors.New("must be a single line")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("must not contain ..")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "", errors.New("must name a file, not the workspace itself")
	}
	return cleaned, nil
}

// resolveFilePath turns a path checkFilePath already cleared into the
// absolute path on disk, refusing one that would leave the workspace through
// a symlink: the lexical check above cannot see where a link points. This
// repeats what resolvePath does for the fs tools, which are built around a
// tool policy this step does not have.
func resolveFilePath(workspace, cleaned string) (string, error) {
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", workspace, err)
	}
	candidate := filepath.Join(workspace, filepath.FromSlash(cleaned))
	resolved, err := evalExistingPath(candidate)
	if err != nil {
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", errors.New("leaves the workspace")
	}
	return candidate, nil
}

// evalExistingPath resolves the symlinks of a path that may not exist yet: it
// walks up to the nearest ancestor that does, resolves that, and rejoins the
// rest unresolved, so that a file about to be created is checked like one
// that is already there.
func evalExistingPath(target string) (string, error) {
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
	resolvedParent, parentErr := evalExistingPath(parent)
	if parentErr != nil {
		return "", parentErr
	}
	return filepath.Join(resolvedParent, filepath.Base(target)), nil
}
