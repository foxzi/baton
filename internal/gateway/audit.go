package gateway

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/foxzi/baton/internal/secrets"
)

// Statuses of an audited call.
const (
	statusOK     = "ok"
	statusError  = "error"
	statusDenied = "denied"
)

// auditLine is one line of tool-calls.jsonl (spec section 7.7).
type auditLine struct {
	Time string `json:"ts"`
	Tool string `json:"tool"`

	// Args are the call's arguments after the secrets redactor.
	Args json.RawMessage `json:"args,omitempty"`

	Status      string `json:"status"`
	ResultBytes int    `json:"result_bytes,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
}

// auditor writes the audit trail of one step. Every line passes the redactor
// first: arguments come from a model, and a model that has been told a secret
// will eventually repeat it.
type auditor struct {
	mu       sync.Mutex
	out      io.Writer
	redactor *secrets.Redactor
	now      func() time.Time
}

func newAuditor(out io.Writer, redactor *secrets.Redactor) *auditor {
	return &auditor{out: out, redactor: redactor, now: time.Now}
}

// write appends one line. A failing audit writer does not fail the call: the
// run's own log is where that shows up, and losing the step to a full disk
// mid-review helps nobody.
func (a *auditor) write(line auditLine) {
	if a == nil || a.out == nil {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	line.Time = a.now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	if a.redactor != nil {
		data = a.redactor.Bytes(data)
	}
	_, _ = a.out.Write(append(data, '\n'))
}
