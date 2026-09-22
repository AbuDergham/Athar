// Package config reads the optional athar.toml next to the exe (spec §8).
package config

// Resume overrides one agent's commands. Each is program + arguments (no shell syntax),
// with {id} and {cwd} placeholders.
type Resume struct {
	Resume          []string `toml:"resume"`
	Fork            []string `toml:"fork"`
	SkipPermissions []string `toml:"skip_permissions"`
}

// Config is athar.toml.
type Config struct {
	Resume  map[string]Resume `toml:"resume"`
	Display struct {
		Arabic string `toml:"arabic"` // "shaped" | "raw" | "" (default chosen by the spike)
	} `toml:"display"`
	Update struct {
		Check *bool `toml:"check"`
	} `toml:"update"`
}

// Load reads dir/athar.toml. A missing file returns the zero Config and no error.
func Load(dir string) (Config, error) { return Config{}, nil }

// UpdateCheckEnabled reports whether update checks are on (default true).
func (c Config) UpdateCheckEnabled() bool { return c.Update.Check == nil || *c.Update.Check }
