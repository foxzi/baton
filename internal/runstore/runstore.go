// Package runstore owns the on-disk run directory: run.json, the event log,
// dedupe effects and per-step files (docs/ru/spec.md, section 10.1).
//
// Every byte this package writes passes through a secrets.Redactor first
// (docs/ru/spec.md, section 13), so a run directory never carries a secret in
// plaintext regardless of which caller forgot to redact it upstream.
package runstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/foxzi/baton/internal/secrets"
)

// SchemaVersion is the run.json schema version (docs/ru/spec.md, section 10.2).
const SchemaVersion = 1

// Run statuses (docs/ru/spec.md, section 10.1).
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// RunError describes a run or step failure.
type RunError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

// StepState is the run.json record for one step.
type StepState struct {
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	Attempts     int        `json:"attempts,omitempty"`
	FallbackUsed bool       `json:"fallback_used,omitempty"`
	CacheHit     bool       `json:"cache_hit,omitempty"`
	Resumed      bool       `json:"resumed,omitempty"`
	Error        *RunError  `json:"error,omitempty"`
}

// RunState is the content of run.json (docs/ru/spec.md, section 10.2).
type RunState struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Status        string         `json:"status"`
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	Inputs        map[string]any `json:"inputs,omitempty"`
	// Scenario is the absolute path of the scenario file, so that a resume
	// can find it again (section 10.4).
	Scenario   string                `json:"scenario,omitempty"`
	Steps      map[string]*StepState `json:"steps,omitempty"`
	CostUSD    *float64              `json:"cost_usd"`
	FailedStep string                `json:"failed_step,omitempty"`
	Error      *RunError             `json:"error,omitempty"`
	ResumeOf   string                `json:"resume_of,omitempty"`
}

// NewID returns a run id of the form YYYYMMDD-HHMMSS-<4 hex>
// (docs/ru/spec.md, section 10.1). The hex suffix is random so that two runs
// started in the same second still get distinct ids.
func NewID(now time.Time) string {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		// crypto/rand failing is effectively unrecoverable; fall back to a
		// fixed suffix rather than panicking, since a run id only needs to
		// be unique, not secret.
		suffix = []byte{0, 0}
	}
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(suffix))
}

// runIDPattern is the set of run ids Create accepts. A run id becomes the
// name of a single directory under runsDir (filepath.Join(runsDir, runID)),
// so it must not be able to smuggle a path separator or a ".." segment; the
// safest way to guarantee that is to only allow characters that can never
// form one. NewID and nextResumeID (cmd/baton/resume.go) both only ever
// produce ids made of digits, lower-case hex and "-", which this accepts.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidateID reports whether id is safe to use as a run directory name.
// --run-id, resume and any other caller that lets a user pick a run id
// should call this before it reaches Create.
func ValidateID(id string) error {
	if id == "" {
		return errors.New("runstore: run id must not be empty")
	}
	if !runIDPattern.MatchString(id) {
		return fmt.Errorf("runstore: run id %q is invalid: must start with a letter or digit and contain only letters, digits, \"-\" or \"_\"", id)
	}
	return nil
}

// Store owns one run directory. All methods are safe for concurrent use.
type Store struct {
	dir      string
	id       string
	redactor *secrets.Redactor

	mu         sync.Mutex
	eventsFile *os.File
	effects    map[string]bool
}

// Create makes <runsDir>/<runID>/ and opens events.jsonl for appending. The
// redactor may be nil, in which case nothing is redacted.
//
// Create fails if the run directory already exists and is not empty, so that
// a run never silently mixes its files with a previous one.
func Create(runsDir, runID string, redactor *secrets.Redactor) (*Store, error) {
	if err := ValidateID(runID); err != nil {
		return nil, err
	}
	dir := filepath.Join(runsDir, runID)

	switch info, err := os.Stat(dir); {
	case err == nil:
		if !info.IsDir() {
			return nil, fmt.Errorf("runstore: %s exists and is not a directory", dir)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("runstore: read %s: %w", dir, err)
		}
		if len(entries) > 0 {
			return nil, fmt.Errorf("runstore: run directory %s already exists and is not empty", dir)
		}
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("runstore: stat %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "steps"), 0o755); err != nil {
		return nil, fmt.Errorf("runstore: create %s: %w", dir, err)
	}

	eventsPath := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("runstore: open %s: %w", eventsPath, err)
	}

	return &Store{
		dir:        dir,
		id:         runID,
		redactor:   redactor,
		eventsFile: f,
		effects:    make(map[string]bool),
	}, nil
}

// Dir returns the run directory path.
func (s *Store) Dir() string { return s.dir }

// ID returns the run id.
func (s *Store) ID() string { return s.id }

// Close closes the event log. It is safe to call once.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventsFile == nil {
		return nil
	}
	err := s.eventsFile.Close()
	s.eventsFile = nil
	return err
}

// redact is the single choke point every write in this package goes through.
// A nil redactor (or a Store built without one) passes data through
// unchanged, per secrets.Redactor's own nil-safety.
func (s *Store) redact(data []byte) []byte {
	return s.redactor.Bytes(data)
}

// Event appends one JSON line with an RFC3339Nano "ts" field, a "type" field
// and the given fields merged in. Concurrency-safe.
func (s *Store) Event(eventType string, fields map[string]any) error {
	event := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		event[k] = v
	}
	event["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	event["type"] = eventType

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("runstore: marshal event: %w", err)
	}
	data = s.redact(data)
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventsFile == nil {
		return errors.New("runstore: store is closed")
	}
	if _, err := s.eventsFile.Write(data); err != nil {
		return fmt.Errorf("runstore: write event: %w", err)
	}
	return nil
}

// WriteRun writes run.json atomically (temp file in the same directory plus
// rename), per docs/ru/spec.md, section 10.2.
func (s *Store) WriteRun(state *RunState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal run.json: %w", err)
	}
	data = s.redact(data)
	if err := atomicWrite(filepath.Join(s.dir, "run.json"), data); err != nil {
		return fmt.Errorf("runstore: write run.json: %w", err)
	}
	return nil
}

// atomicWrite writes data to path via a temp file in the same directory
// followed by a rename, so a reader never observes a partial write.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil {
		os.Remove(tmpName)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmpName)
		return closeErr
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// StepDir creates and returns <dir>/steps/<stepID>. For a foreach element,
// callers pass a path like "review/3"; nested segments are created as
// needed.
func (s *Store) StepDir(stepID string) (string, error) {
	dir := filepath.Join(s.dir, "steps", filepath.FromSlash(stepID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("runstore: create step dir %s: %w", dir, err)
	}
	return dir, nil
}

// WriteStepFile writes a file inside the step directory, creating it if
// needed. Used for input.json, output.json, stdout.log, stderr.log.
func (s *Store) WriteStepFile(stepID, name string, data []byte) error {
	dir, err := s.StepDir(stepID)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, s.redact(data), 0o644); err != nil {
		return fmt.Errorf("runstore: write %s: %w", path, err)
	}
	return nil
}

// WriteStepJSON marshals v with indentation and writes it as name.
func (s *Store) WriteStepJSON(stepID, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal %s: %w", name, err)
	}
	return s.WriteStepFile(stepID, name, data)
}

// effectsFile is the on-disk record of executed dedupe keys
// (docs/ru/spec.md, section 9.4).
type effectsFile struct {
	Keys []string `json:"keys"`
}

// EffectDone reports whether a dedupe key was already executed
// (docs/ru/spec.md, section 9.4).
func (s *Store) EffectDone(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effects[key]
}

// MarkEffect records a dedupe key as executed and persists effects.json.
func (s *Store) MarkEffect(key string) error {
	s.mu.Lock()
	s.effects[key] = true
	keys := make([]string, 0, len(s.effects))
	for k := range s.effects {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	sort.Strings(keys)

	data, err := json.MarshalIndent(effectsFile{Keys: keys}, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal effects.json: %w", err)
	}
	data = s.redact(data)
	if err := atomicWrite(filepath.Join(s.dir, "effects.json"), data); err != nil {
		return fmt.Errorf("runstore: write effects.json: %w", err)
	}
	return nil
}
