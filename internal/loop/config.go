package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

type AgentConfig struct {
	Kind         string   `json:"kind"`
	Command      string   `json:"command"`
	Model        string   `json:"model,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}
type Config struct {
	Builder        AgentConfig `json:"builder"`
	Reviewer       AgentConfig `json:"reviewer"`
	Checks         [][]string  `json:"checks"`
	MaxRounds      int         `json:"max_rounds"`
	CommandTimeout string      `json:"command_timeout"`
	RunTimeout     string      `json:"run_timeout"`
}

func DefaultConfig() Config {
	return Config{Builder: AgentConfig{Kind: "claude", Command: "claude"}, Reviewer: AgentConfig{Kind: "codex", Command: "codex"}, Checks: [][]string{{"go", "test", "./..."}}, MaxRounds: 3, CommandTimeout: "20m", RunTimeout: "2h"}
}
func ReadConfig(path string) (Config, error) {
	var c Config
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if e = ensureEOF(d); e != nil {
		return c, e
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	for _, a := range []AgentConfig{c.Builder, c.Reviewer} {
		if a.Kind != "claude" && a.Kind != "codex" {
			return fmt.Errorf("unsupported agent kind %q", a.Kind)
		}
		if a.Command == "" {
			return errors.New("agent command is required")
		}
	}
	if c.MaxRounds < 1 || c.MaxRounds > 20 {
		return errors.New("max_rounds must be between 1 and 20")
	}
	if len(c.Checks) == 0 {
		return errors.New("configure at least one local check")
	}
	for _, v := range c.Checks {
		if len(v) == 0 || v[0] == "" {
			return errors.New("check commands must be nonempty argument arrays")
		}
	}
	for _, v := range []string{c.CommandTimeout, c.RunTimeout} {
		d, e := time.ParseDuration(v)
		if e != nil || d <= 0 {
			return fmt.Errorf("invalid timeout %q", v)
		}
	}
	return nil
}
