package packs

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/itchyny/gojq"
)

// emptyEnv is the environment gojq sees. Transforms must be pure (spec
// section 7.4.2): env and $ENV resolve to nothing and input and inputs fail
// at runtime because no input iterator is provided, so a transform cannot
// reach the process environment, where secrets live, or the runner's stdin.
func emptyEnv() []string { return nil }

// compileJQ parses and compiles a jq expression from a pack. Field names the
// pack field for diagnostics.
func compileJQ(field, source string) (*gojq.Code, error) {
	query, err := gojq.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	if err := checkPure(query); err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	code, err := gojq.Compile(query, gojq.WithEnvironLoader(emptyEnv))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return code, nil
}

// runJQ applies a compiled expression to a value and returns its single
// result. Pack expressions describe one value, so extra outputs are an error
// rather than a silent choice.
func runJQ(code *gojq.Code, input any) (any, error) {
	iter := code.Run(input)
	value, ok := iter.Next()
	if !ok {
		return nil, errors.New("produced no value")
	}
	if err, isErr := value.(error); isErr {
		return nil, err
	}
	if _, more := iter.Next(); more {
		return nil, errors.New("produced more than one value")
	}
	return value, nil
}

// Unwrap applies the envelope to a decoded response body and reports an error
// the pack recognises as a failure carried in a successful response.
func (e *Envelope) Unwrapped(body any) (any, error) {
	if e == nil {
		return body, nil
	}
	if e.errorWhen != nil {
		failed, err := runJQ(e.errorWhen, body)
		if err != nil {
			return nil, fmt.Errorf("envelope.error_when: %w", err)
		}
		if truthy(failed) {
			return nil, errors.New(e.errorText(body))
		}
	}
	if e.unwrap == nil {
		return body, nil
	}
	unwrapped, err := runJQ(e.unwrap, body)
	if err != nil {
		return nil, fmt.Errorf("envelope.unwrap: %w", err)
	}
	return unwrapped, nil
}

// errorText renders the message the pack extracts from a failed response.
func (e *Envelope) errorText(body any) string {
	if e.errorMessage == nil {
		return "the response reports an error"
	}
	message, err := runJQ(e.errorMessage, body)
	if err != nil {
		return "the response reports an error"
	}
	if text, ok := message.(string); ok && text != "" {
		return text
	}
	if message == nil {
		return "the response reports an error"
	}
	return fmt.Sprint(message)
}

// Items extracts the item list of one page. It returns the page itself when
// the pagination strategy does not name a location.
func (p *Pagination) PageItems(page any) (any, error) {
	if p == nil || p.items == nil {
		return page, nil
	}
	items, err := runJQ(p.items, page)
	if err != nil {
		return nil, fmt.Errorf("pagination.items: %w", err)
	}
	return items, nil
}

// NextCursor extracts the cursor of the following page. It returns nil when
// the strategy names no cursor or the page carries none.
func (p *Pagination) NextCursor(page any) (any, error) {
	if p == nil || p.next == nil {
		return nil, nil
	}
	cursor, err := runJQ(p.next, page)
	if err != nil {
		return nil, fmt.Errorf("pagination.next: %w", err)
	}
	return cursor, nil
}

// PageTotal extracts the total item count a page reports, which the offset
// style stops on. The second result is false when the pack names no total or
// the page carries no number.
func (p *Pagination) PageTotal(page any) (int, bool, error) {
	if p == nil || p.total == nil {
		return 0, false, nil
	}
	value, err := runJQ(p.total, page)
	if err != nil {
		return 0, false, fmt.Errorf("pagination.total: %w", err)
	}
	switch typed := value.(type) {
	case nil:
		return 0, false, nil
	case int:
		return typed, true, nil
	case float64:
		return int(typed), true, nil
	case *big.Int:
		return int(typed.Int64()), true, nil
	default:
		return 0, false, fmt.Errorf("pagination.total: %v is not a number", value)
	}
}

// ExchangeToken extracts the token from the response of the exchange
// operation.
func (a *Auth) ExchangeToken(body any) (string, error) {
	if a.extract == nil {
		return "", errors.New("auth.extract: required for the exchange scheme")
	}
	value, err := runJQ(a.extract, body)
	if err != nil {
		return "", fmt.Errorf("auth.extract: %w", err)
	}
	token, ok := value.(string)
	if !ok {
		if value == nil {
			return "", errors.New("auth.extract: the response carries no token")
		}
		return "", fmt.Errorf("auth.extract: %v is not a token", value)
	}
	if token == "" {
		return "", errors.New("auth.extract: the response carries no token")
	}
	return token, nil
}

// Transformed applies the operation transform, or pick, to a result. Results
// without either are returned unchanged.
func (o *Op) Transformed(result any) (any, error) {
	if o.transform == nil {
		return result, nil
	}
	transformed, err := runJQ(o.transform, result)
	if err != nil {
		return nil, fmt.Errorf("transform: %w", err)
	}
	return transformed, nil
}

// truthy follows jq: false and null are false, everything else is true.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	default:
		return true
	}
}
