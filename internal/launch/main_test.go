package launch

import (
	"encoding/json"
	"os"
	"testing"
)

// helperEnv switches the test binary into a helper that records how it was
// started (arguments, working directory, one environment variable) into the
// file named by helperOutEnv. testdata/echoargs.cmd calls it like an npm shim.
const (
	helperEnv    = "ATHAR_LAUNCH_HELPER"
	helperOutEnv = "ATHAR_LAUNCH_HELPER_OUT"
	helperVarEnv = "ATHAR_LAUNCH_HELPER_VAR"
)

type helperReport struct {
	Args   []string `json:"args"`
	Cwd    string   `json:"cwd"`
	Var    string   `json:"var"`
	VarSet bool     `json:"varSet"`
}

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "echo" {
		cwd, _ := os.Getwd()
		name := os.Getenv(helperVarEnv)
		v, ok := os.LookupEnv(name)
		b, _ := json.Marshal(helperReport{Args: os.Args[1:], Cwd: cwd, Var: v, VarSet: ok})
		if err := os.WriteFile(os.Getenv(helperOutEnv), b, 0o644); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
