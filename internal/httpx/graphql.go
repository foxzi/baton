package httpx

import (
	"fmt"
	"strings"

	"github.com/foxzi/baton/internal/packs"
)

// Fields of the GraphQL request and response documents.
const (
	graphQLQueryField     = "query"
	graphQLVariablesField = "variables"
	graphQLErrorsField    = "errors"
)

// graphQLError reports the errors a GraphQL service returns beside a 200
// status. A pack that describes them itself in envelope.error_when keeps
// control; otherwise the protocol-level errors list is enough to fail on.
func graphQLError(pack *packs.Pack, op *packs.Op, body any, what string) error {
	if !op.IsGraphQL() {
		return nil
	}
	if pack.Envelope != nil && pack.Envelope.ErrorWhen != "" {
		return nil
	}
	object, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := object[graphQLErrorsField].([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	return errorf(ClassCommand, "%s: %s", what, graphQLMessages(list))
}

// graphQLMessages joins the messages of the errors list, at most three of
// them, for one error line.
func graphQLMessages(list []any) string {
	const limit = 3
	messages := make([]string, 0, limit)
	for _, entry := range list {
		if len(messages) == limit {
			messages = append(messages, fmt.Sprintf("and %d more", len(list)-limit))
			break
		}
		text := ""
		if object, ok := entry.(map[string]any); ok {
			text, _ = object["message"].(string)
		}
		if text == "" {
			text = fmt.Sprint(entry)
		}
		messages = append(messages, text)
	}
	return strings.Join(messages, "; ")
}

// graphQLVariables returns the variables of a GraphQL request body, creating
// the object when the request sends none.
func graphQLVariables(body map[string]any) map[string]any {
	variables, ok := body[graphQLVariablesField].(map[string]any)
	if !ok {
		variables = map[string]any{}
		body[graphQLVariablesField] = variables
	}
	return variables
}
