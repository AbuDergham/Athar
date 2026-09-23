package copilot

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Kinds of a chatSessions .jsonl mutation log entry (VS Code >= 1.109).
const (
	opInitial = 0 // {kind:0, v:<whole session>}: replaces the state
	opSet     = 1 // {kind:1, k:[path], v}: sets the value at path
	opPush    = 2 // {kind:2, k:[path], v:[items], i?}: truncates the array at path to i, then appends v
	opDelete  = 3 // {kind:3, k:[path]}: removes the key (or array element) at path
)

// logEntry is one line of the mutation log.
type logEntry struct {
	Kind *int              `json:"kind"`
	K    []json.RawMessage `json:"k"`
	V    json.RawMessage   `json:"v"`
	I    *int              `json:"i"`
}

// replayLog rebuilds a session from a mutation log. state is nil when the log
// has no initial snapshot. skipped counts lines that could not be parsed or
// applied (never the last line when it is torn). torn is true when the last
// line has no newline and does not parse: the file is probably being written.
func replayLog(data []byte) (state any, skipped int, torn bool) {
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		last := i == len(lines)-1 // no newline after it
		var e logEntry
		if err := json.Unmarshal(line, &e); err != nil || e.Kind == nil {
			if last {
				torn = true
			} else {
				skipped++
			}
			continue
		}
		var ok bool
		state, ok = apply(state, e)
		if !ok {
			skipped++
		}
	}
	return state, skipped, torn
}

// apply applies one entry to state and reports whether it could.
func apply(state any, e logEntry) (any, bool) {
	if *e.Kind == opInitial {
		v, ok := decodeValue(e.V)
		if !ok {
			return state, false
		}
		return v, true
	}
	if state == nil {
		return state, false // a mutation before any snapshot
	}
	path, ok := decodePath(e.K)
	if !ok {
		return state, false
	}
	switch *e.Kind {
	case opSet:
		v, ok := decodeValue(e.V)
		if !ok {
			return state, false
		}
		return update(state, path, func(any) (any, bool) { return v, true })
	case opPush:
		var items []any
		if len(bytes.TrimSpace(e.V)) > 0 {
			v, ok := decodeValue(e.V)
			if !ok {
				return state, false
			}
			if v != nil {
				if items, ok = v.([]any); !ok {
					return state, false
				}
			}
		}
		return update(state, path, func(old any) (any, bool) {
			arr, ok := old.([]any)
			if old != nil && !ok {
				return old, false
			}
			if e.I != nil && *e.I >= 0 && *e.I < len(arr) {
				arr = arr[:*e.I]
			}
			return append(arr, items...), true
		})
	case opDelete:
		if len(path) == 0 {
			return state, false
		}
		leaf := path[len(path)-1]
		return update(state, path[:len(path)-1], func(parent any) (any, bool) {
			switch p := parent.(type) {
			case map[string]any:
				key, ok := leaf.(string)
				if !ok {
					return parent, false
				}
				delete(p, key)
				return p, true
			case []any:
				idx, ok := leaf.(int)
				if !ok || idx < 0 || idx >= len(p) {
					return parent, false
				}
				return append(p[:idx:idx], p[idx+1:]...), true
			}
			return parent, false
		})
	}
	return state, false // unknown kind
}

// update replaces the value at path with fn(old value). Missing object keys
// are created; an array index may point one past the end (append).
func update(cur any, path []any, fn func(old any) (any, bool)) (any, bool) {
	if len(path) == 0 {
		return fn(cur)
	}
	switch key := path[0].(type) {
	case string:
		m, ok := cur.(map[string]any)
		if !ok {
			if cur != nil {
				return cur, false
			}
			m = map[string]any{}
		}
		child, ok := update(m[key], path[1:], fn)
		if !ok {
			return cur, false
		}
		m[key] = child
		return m, true
	case int:
		a, ok := cur.([]any)
		if (!ok && cur != nil) || key < 0 || key > len(a) {
			return cur, false
		}
		if key == len(a) {
			a = append(a, nil)
		}
		child, ok := update(a[key], path[1:], fn)
		if !ok {
			return cur, false
		}
		a[key] = child
		return a, true
	}
	return cur, false
}

// decodePath turns k (strings are object keys, numbers array indexes) into
// []any of string and int.
func decodePath(k []json.RawMessage) ([]any, bool) {
	path := make([]any, 0, len(k))
	for _, raw := range k {
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] == '"' {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, false
			}
			path = append(path, s)
			continue
		}
		n, err := strconv.Atoi(string(raw))
		if err != nil {
			return nil, false
		}
		path = append(path, n)
	}
	return path, true
}

// decodeValue decodes a JSON value keeping numbers exact (json.Number).
func decodeValue(raw json.RawMessage) (any, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}
