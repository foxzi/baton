package engine

// redact masks every secret of the run in text.
//
// Section 13 requires that no secret reaches runs/, the logs or a
// notification. The run store redacts everything it writes, so this covers
// the other two ways text leaves the runner: an event handed to the observer
// (the human log and --json) and a rendered notification. Failure messages go
// through both, and a message can quote a URL with a path token or the tail of
// a response that echoed a header back.
func (e *Engine) redact(text string) string {
	return e.redactor().String(text)
}

// redactFields redacts the string values of an event's fields, leaving other
// values as they are: only a string can carry a secret verbatim.
func (e *Engine) redactFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return fields
	}
	redacted := make(map[string]any, len(fields))
	for key, value := range fields {
		if text, ok := value.(string); ok {
			redacted[key] = e.redact(text)
			continue
		}
		redacted[key] = value
	}
	return redacted
}
