package adapters

import (
	"path/filepath"
	"testing"
)

func TestEnvFor(t *testing.T) {
	home := filepath.Join(string(filepath.Separator)+"home", "u")
	j := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	vars := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	win := map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`,
		"XDG_CONFIG_HOME": "/x/config"}
	cases := []struct {
		name, goos, home      string
		env                   map[string]string
		appData, localAppData string
		documents             string
	}{
		{"windows", "windows", home, win, `C:\Users\u\AppData\Roaming`, `C:\Users\u\AppData\Local`, j("Documents")},
		{"windows without variables", "windows", home, nil, "", "", j("Documents")},
		{"macOS", "darwin", home, map[string]string{"XDG_CONFIG_HOME": "/x/config"},
			j("Library", "Application Support"), j("Library", "Application Support"), j("Documents")},
		{"linux defaults", "linux", home, nil, j(".config"), j(".local", "share"), j("Documents")},
		{"linux XDG", "linux", home, map[string]string{"XDG_CONFIG_HOME": "/x/config", "XDG_DATA_HOME": "/x/data"},
			"/x/config", "/x/data", j("Documents")},
		{"linux relative XDG is ignored", "linux", home, map[string]string{"XDG_CONFIG_HOME": "cfg", "XDG_DATA_HOME": "./d"},
			j(".config"), j(".local", "share"), j("Documents")},
		{"freebsd is like linux", "freebsd", home, nil, j(".config"), j(".local", "share"), j("Documents")},
		{"no home", "linux", "", nil, "", "", ""},
		{"no home, XDG set", "linux", "", map[string]string{"XDG_CONFIG_HOME": "/x/config"}, "/x/config", "", ""},
		{"macOS without home", "darwin", "", nil, "", "", ""},
	}
	for _, c := range cases {
		e := EnvFor(c.goos, c.home, vars(c.env))
		if e.Home != c.home || e.AppData != c.appData || e.LocalAppData != c.localAppData || e.Documents != c.documents || e.Getenv == nil {
			t.Errorf("%s: got home %q appData %q localAppData %q documents %q, want %q %q %q %q",
				c.name, e.Home, e.AppData, e.LocalAppData, e.Documents, c.home, c.appData, c.localAppData, c.documents)
		}
	}
}

func TestCurrentEnvKeepsTempDir(t *testing.T) {
	if e := CurrentEnv("/tmp/x"); e.TempDir != "/tmp/x" || e.Getenv == nil {
		t.Errorf("CurrentEnv = %+v", e)
	}
}
