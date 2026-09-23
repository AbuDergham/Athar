package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// SplitCommand splits a command written as one string ("codex resume {id}") into
// program and arguments. Tokens are separated by white space; double quotes group
// a token that contains spaces and are removed ("C:\Program Files\x.exe" --resume {id}).
// There is no escaping: backslashes are literal, so Windows paths need no doubling.
// An empty string gives an empty, non-nil slice; an unterminated quote is an error.
func SplitCommand(s string) ([]string, error) {
	args := []string{}
	var cur strings.Builder
	inToken, inQuote := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			inToken = true
		case !inQuote && unicode.IsSpace(r):
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if inQuote {
		return nil, errors.New("unterminated double quote")
	}
	if inToken {
		args = append(args, cur.String())
	}
	return args, nil
}

// forbidden are characters that cmd.exe would interpret (§6.1): commands in
// athar.toml are program + arguments, never shell syntax.
const forbidden = `"%!^&|<>`

// checkArgs rejects control characters, shell metacharacters and unknown
// {placeholders} in a command's tokens.
func checkArgs(args []string) error {
	for _, a := range args {
		for _, r := range a {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("argument %q contains a control character", a)
			}
			if strings.ContainsRune(forbidden, r) {
				return fmt.Errorf("argument %q contains %q (shell syntax is not allowed; use program + arguments)", a, r)
			}
		}
		if p := unknownPlaceholder(a); p != "" {
			return fmt.Errorf("argument %q has unknown placeholder %s (use {id} or {cwd})", a, p)
		}
	}
	return nil
}

// unknownPlaceholder returns the first "{...}" in a that is not {id} or {cwd},
// or "" when there is none. A brace without a partner is literal text.
func unknownPlaceholder(a string) string {
	for i := 0; i < len(a); {
		j := strings.IndexByte(a[i:], '{')
		if j < 0 {
			return ""
		}
		i += j
		rest := a[i:]
		switch {
		case strings.HasPrefix(rest, "{id}"):
			i += len("{id}")
		case strings.HasPrefix(rest, "{cwd}"):
			i += len("{cwd}")
		default:
			if k := strings.IndexByte(rest, '}'); k >= 0 {
				return rest[:k+1]
			}
			return ""
		}
	}
	return ""
}

// command is a TOML value that is either one string (split by SplitCommand)
// or an array of strings (used as is).
type command []string

// UnmarshalTOML implements toml.Unmarshaler.
func (c *command) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		args, err := SplitCommand(v)
		if err != nil {
			return err
		}
		*c = args
	case []any:
		args := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("array element %v is %s, want a string", e, tomlType(e))
			}
			args = append(args, s)
		}
		*c = args
	default:
		return fmt.Errorf("value is %s, want a string or an array of strings", tomlType(v))
	}
	return nil
}

// tomlType names the TOML type of a decoded value for error messages.
func tomlType(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any, []map[string]any:
		return "a table"
	default:
		return "a date/time"
	}
}
