package launch

import (
	"reflect"
	"runtime"
	"sort"
	"testing"
)

func TestWTArgs(t *testing.T) {
	c := Command{
		Title:   "a;b",
		Dir:     `D:\x;y`,
		CmdLine: `set "V=C:\r;s" && claude --resume abc`,
	}
	want := []string{
		"-w", "0", "new-tab", "--title", `a\;b`, "--suppressApplicationTitle", "--inheritEnvironment",
		"-d", `D:\x\;y`, "cmd.exe", "/d", "/s", "/k", `set "V=C:\r\;s" && claude --resume abc`,
	}
	if got := WTArgs(c); !reflect.DeepEqual(got, want) {
		t.Errorf("WTArgs =\n%q\nwant\n%q", got, want)
	}

	// A one-token command line with quotes but no space would not be wrapped by
	// wt, and cmd /s would then strip its quotes: a trailing space prevents that.
	c = Command{Title: "t", Dir: `D:\w`, CmdLine: `"D:\a&b\x.exe"`}
	got := WTArgs(c)
	if last := got[len(got)-1]; last != `"D:\a&b\x.exe" ` {
		t.Errorf("last token = %q", last)
	}
}

func TestConsoleCmdLine(t *testing.T) {
	c := Command{Title: "My title", CmdLine: `set "V=" && claude --resume abc`}
	want := `cmd.exe /d /s /k "title My title && set "V=" && claude --resume abc"`
	if got := consoleCmdLine(c, "/k"); got != want {
		t.Errorf("consoleCmdLine = %q, want %q", got, want)
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"=C:=C:\\", "Path=C:\\bin", "CLAUDE_CONFIG_DIR=old", "KEEP=1"}
	got := mergeEnv(base, map[string]string{"CLAUDE_CONFIG_DIR": "", "NEW": "v", "KEEP": "2"})
	sort.Strings(got)
	want := []string{"=C:=C:\\", "KEEP=2", "NEW=v", "Path=C:\\bin"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeEnv = %q, want %q", got, want)
	}
	if runtime.GOOS == "windows" {
		got := mergeEnv(base, map[string]string{"claude_config_dir": "", "keep": "2"})
		sort.Strings(got)
		want := []string{"=C:=C:\\", "Path=C:\\bin", "keep=2"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("mergeEnv (case-insensitive) = %q, want %q", got, want)
		}
	}
}
