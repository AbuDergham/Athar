package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplayLog(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		want    string // JSON of the final state
		skipped int
		torn    bool
	}{
		{
			name:  "snapshot only",
			lines: []string{`{"kind":0,"v":{"a":1,"b":[1,2]}}`},
			want:  `{"a":1,"b":[1,2]}`,
		},
		{
			name: "set nested, creating keys, and append by index",
			lines: []string{
				`{"kind":0,"v":{"r":[{"x":1}]}}`,
				`{"kind":1,"k":["r",0,"y","z"],"v":"new"}`,
				`{"kind":1,"k":["r",1],"v":{"x":2}}`,
				`{"kind":1,"k":["t"],"v":1.5e3}`,
			},
			want: `{"r":[{"x":1,"y":{"z":"new"}},{"x":2}],"t":1.5e3}`,
		},
		{
			name: "push appends, i truncates first",
			lines: []string{
				`{"kind":0,"v":{"r":[1,2,3]}}`,
				`{"kind":2,"k":["r"],"v":[4,5]}`,
				`{"kind":2,"k":["r"],"v":[9],"i":2}`,
				`{"kind":2,"k":["n"],"v":["first"]}`,
				`{"kind":2,"k":["r"]}`,
			},
			want: `{"n":["first"],"r":[1,2,9]}`,
		},
		{
			name: "delete key and array element",
			lines: []string{
				`{"kind":0,"v":{"a":1,"b":2,"r":["x","y","z"]}}`,
				`{"kind":3,"k":["a"]}`,
				`{"kind":3,"k":["r",1]}`,
			},
			want: `{"b":2,"r":["x","z"]}`,
		},
		{
			name: "a new snapshot resets the state",
			lines: []string{
				`{"kind":0,"v":{"a":1}}`,
				`{"kind":1,"k":["b"],"v":2}`,
				`{"kind":0,"v":{"c":3}}`,
			},
			want: `{"c":3}`,
		},
		{
			name: "bad entries are skipped",
			lines: []string{
				`{"kind":1,"k":["early"],"v":1}`, // before any snapshot
				`{"kind":0,"v":{"a":[1],"s":"str"}}`,
				`garbage`,
				`{"k":["a"],"v":1}`,              // no kind
				`{"kind":1,"k":["a",5],"v":1}`,   // index out of range
				`{"kind":1,"k":["s","x"],"v":1}`, // key into a string
				`{"kind":2,"k":["s"],"v":[1]}`,   // push to a non-array
				`{"kind":2,"k":["a"],"v":7}`,     // push of a non-list
				`{"kind":3,"k":[]}`,              // delete the root
				`{"kind":3,"k":["a",9]}`,         // delete out of range
				`{"kind":9,"k":["a"]}`,           // unknown kind
				`{"kind":1,"k":[true],"v":1}`,    // bad path element
				``,
			},
			want:    `{"a":[1],"s":"str"}`,
			skipped: 11,
		},
		{
			name:  "torn last line",
			lines: []string{`{"kind":0,"v":{"a":1}}`, `{"kind":1,"k":["a"],"v":`},
			want:  `{"a":1}`,
			torn:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := strings.Join(tt.lines, "\n")
			if !tt.torn {
				data += "\n"
			}
			state, skipped, torn := replayLog([]byte(data))
			got, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want || skipped != tt.skipped || torn != tt.torn {
				t.Errorf("got %s skipped=%d torn=%v, want %s skipped=%d torn=%v", got, skipped, torn, tt.want, tt.skipped, tt.torn)
			}
		})
	}
}

func TestReplayNoSnapshot(t *testing.T) {
	state, skipped, torn := replayLog([]byte("\n\n"))
	if state != nil || skipped != 0 || torn {
		t.Errorf("empty log = %v %d %v", state, skipped, torn)
	}
}
