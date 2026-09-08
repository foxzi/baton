package packs

import (
	"fmt"

	"github.com/itchyny/gojq"
)

// impureNames are the jq names a pack expression must not use (spec section
// 7.4.2): a transform reads the response and nothing else. gojq already
// refuses input, inputs and $__loc__ when it compiles, but a pack is rejected
// on the name alone so the pack author gets a message about the rule instead
// of one about the compiler.
var impureNames = map[string]string{
	"env":      "a transform must not read the process environment",
	"$ENV":     "a transform must not read the process environment",
	"input":    "a transform must not read the runner's input",
	"inputs":   "a transform must not read the runner's input",
	"$__loc__": "a transform must not depend on its own source location",
}

// checkPure reports the first impure name a parsed expression uses.
func checkPure(q *gojq.Query) error { return pureQuery(q) }

func pureQuery(q *gojq.Query) error {
	if q == nil {
		return nil
	}
	for _, def := range q.FuncDefs {
		if err := pureQuery(def.Body); err != nil {
			return err
		}
	}
	if err := pureTerm(q.Term); err != nil {
		return err
	}
	if err := pureQuery(q.Left); err != nil {
		return err
	}
	if err := pureQuery(q.Right); err != nil {
		return err
	}
	for _, pattern := range q.Patterns {
		if err := purePattern(pattern); err != nil {
			return err
		}
	}
	return nil
}

func pureTerm(t *gojq.Term) error {
	if t == nil {
		return nil
	}

	if t.Func != nil {
		if why, impure := impureNames[t.Func.Name]; impure {
			return fmt.Errorf("%s is not allowed: %s", t.Func.Name, why)
		}
		for _, arg := range t.Func.Args {
			if err := pureQuery(arg); err != nil {
				return err
			}
		}
	}

	if err := pureIndex(t.Index); err != nil {
		return err
	}
	if err := pureString(t.Str); err != nil {
		return err
	}
	if t.Unary != nil {
		if err := pureTerm(t.Unary.Term); err != nil {
			return err
		}
	}
	if t.Object != nil {
		for _, kv := range t.Object.KeyVals {
			if err := pureString(kv.KeyString); err != nil {
				return err
			}
			if err := pureQueries(kv.KeyQuery, kv.Val); err != nil {
				return err
			}
		}
	}
	if t.Array != nil {
		if err := pureQuery(t.Array.Query); err != nil {
			return err
		}
	}
	if t.If != nil {
		if err := pureQueries(t.If.Cond, t.If.Then, t.If.Else); err != nil {
			return err
		}
		for _, elif := range t.If.Elif {
			if err := pureQueries(elif.Cond, elif.Then); err != nil {
				return err
			}
		}
	}
	if t.Try != nil {
		if err := pureQueries(t.Try.Body, t.Try.Catch); err != nil {
			return err
		}
	}
	if t.Reduce != nil {
		if err := pureQueries(t.Reduce.Query, t.Reduce.Start, t.Reduce.Update); err != nil {
			return err
		}
		if err := purePattern(t.Reduce.Pattern); err != nil {
			return err
		}
	}
	if t.Foreach != nil {
		if err := pureQueries(t.Foreach.Query, t.Foreach.Start, t.Foreach.Update, t.Foreach.Extract); err != nil {
			return err
		}
		if err := purePattern(t.Foreach.Pattern); err != nil {
			return err
		}
	}
	if t.Label != nil {
		if err := pureQuery(t.Label.Body); err != nil {
			return err
		}
	}
	if err := pureQuery(t.Query); err != nil {
		return err
	}
	for _, suffix := range t.SuffixList {
		if err := pureIndex(suffix.Index); err != nil {
			return err
		}
	}
	return nil
}

func pureQueries(qs ...*gojq.Query) error {
	for _, q := range qs {
		if err := pureQuery(q); err != nil {
			return err
		}
	}
	return nil
}

func pureIndex(i *gojq.Index) error {
	if i == nil {
		return nil
	}
	if err := pureString(i.Str); err != nil {
		return err
	}
	return pureQueries(i.Start, i.End)
}

func pureString(s *gojq.String) error {
	if s == nil {
		return nil
	}
	return pureQueries(s.Queries...)
}

func purePattern(p *gojq.Pattern) error {
	if p == nil {
		return nil
	}
	for _, item := range p.Array {
		if err := purePattern(item); err != nil {
			return err
		}
	}
	for _, kv := range p.Object {
		if err := pureString(kv.KeyString); err != nil {
			return err
		}
		if err := pureQuery(kv.KeyQuery); err != nil {
			return err
		}
		if err := purePattern(kv.Val); err != nil {
			return err
		}
	}
	return nil
}
