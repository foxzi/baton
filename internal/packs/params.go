package packs

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// BoundArgs is the request shape of an operation call after the arguments
// have been checked against params: values sorted into the path, the query
// and the body or form.
type BoundArgs struct {
	Path  map[string]string
	Query map[string]string
	Body  map[string]any
	Form  map[string]string
}

// BindArgs checks args against the params of the operation and sorts them
// into their places. Values are the rendered arguments of the http step:
// strings, numbers, booleans or, for body arguments, any JSON value.
func (o *Op) BindArgs(args map[string]any) (*BoundArgs, error) {
	bound := &BoundArgs{
		Path:  map[string]string{},
		Query: map[string]string{},
		Body:  map[string]any{},
		Form:  map[string]string{},
	}
	var problems []error

	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, ok := o.Params[name]; !ok {
			problems = append(problems, fmt.Errorf("args.%s: operation %s has no such argument", name, o.name))
		}
	}

	paramNames := make([]string, 0, len(o.Params))
	for name := range o.Params {
		paramNames = append(paramNames, name)
	}
	slices.Sort(paramNames)

	for _, name := range paramNames {
		param := o.Params[name]
		value, given := args[name]
		if !given || value == nil {
			if param.Default != nil {
				value = param.Default
			} else if param.IsRequired() {
				problems = append(problems, fmt.Errorf("args.%s: required", name))
				continue
			} else {
				continue
			}
		}
		if err := param.check(name, value); err != nil {
			problems = append(problems, err)
			continue
		}
		switch param.In {
		case InPath:
			text, err := scalar(name, value)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			if param.Encode == EncodePath {
				text = escapeSegment(text)
			}
			bound.Path[name] = text
		case InQuery, InForm:
			text, err := scalar(name, value)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			if param.In == InQuery {
				bound.Query[name] = text
			} else {
				bound.Form[name] = text
			}
		default:
			bound.Body[name] = value
		}
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return bound, nil
}

// check applies the pattern, length and enum constraints of an argument.
// They describe text, so they only apply to values that are text.
func (p *Param) check(name string, value any) error {
	text, ok := value.(string)
	if !ok {
		if scalarText, err := scalar(name, value); err == nil {
			text, ok = scalarText, true
		}
	}
	if !ok {
		if p.pattern != nil || p.MaxLen > 0 || len(p.Enum) > 0 {
			return fmt.Errorf("args.%s: params constrain text, but the value is %T", name, value)
		}
		return nil
	}
	if p.pattern != nil && !p.pattern.MatchString(text) {
		return fmt.Errorf("args.%s: does not match %s", name, p.Pattern)
	}
	if p.MaxLen > 0 && len(text) > p.MaxLen {
		return fmt.Errorf("args.%s: %d bytes exceeds max_len %d", name, len(text), p.MaxLen)
	}
	if len(p.Enum) > 0 && !slices.Contains(p.Enum, text) {
		return fmt.Errorf("args.%s: must be one of %s", name, strings.Join(p.Enum, ", "))
	}
	return nil
}

// ExpandPath substitutes the path arguments into the operation path.
func (o *Op) ExpandPath(path map[string]string) (string, error) {
	var missing []string
	expanded := placeholderRe.ReplaceAllStringFunc(o.path, func(match string) string {
		name := match[1 : len(match)-1]
		value, ok := path[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("path: no value for {%s}", strings.Join(missing, "}, {"))
	}
	return expanded, nil
}

// scalar renders an argument as request text.
func scalar(name string, value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10), nil
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("args.%s: %T cannot go into a path, query or form", name, value)
	}
}

// escapeSegment percent-encodes a value that stands for a whole path
// segment, slashes included, the way GitLab wants project paths.
func escapeSegment(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), "/", "%2F")
}
