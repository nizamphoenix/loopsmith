package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The helper acts like each installed CLI, including authentication and result
// envelopes. This exercises real subprocesses without spending model usage.
func TestAgentHelper(t *testing.T) {
	if os.Getenv("LOOPSMITH_HELPER") != "1" {
		return
	}
	args := os.Args
	kind := os.Getenv("LOOPSMITH_KIND")
	has := func(v string) bool {
		for _, a := range args {
			if a == v {
				return true
			}
		}
		return false
	}
	value := func(v string) string {
		for i, a := range args {
			if a == v && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if has("status") {
		if kind == "codex" {
			fmt.Println("Logged in using ChatGPT")
		} else {
			fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai"}`)
		}
		os.Exit(0)
	}
	prompt, _ := io.ReadAll(os.Stdin)
	review := strings.HasPrefix(string(prompt), "Independently review")
	if !strings.Contains(string(prompt), "TASK:") {
		os.Exit(7)
	}
	if review && !strings.Contains(string(prompt), "RECORDED DIFF:") {
		os.Exit(8)
	}
	if review {
		if kind == "codex" && value("--sandbox") != "read-only" {
			os.Exit(9)
		}
		if kind == "claude" && (strings.Contains(value("--tools"), "Edit") || strings.Contains(value("--tools"), "Bash")) {
			os.Exit(10)
		}
	} else {
		if kind == "codex" && value("--sandbox") != "workspace-write" {
			os.Exit(11)
		}
		if e := os.WriteFile("file.txt", []byte("new\n"), 0600); e != nil {
			os.Exit(12)
		}
	}
	if os.Getenv("LOOPSMITH_FAIL") == "1" {
		fmt.Println(`{"is_error":true,"subtype":"error_during_execution"}`)
		os.Exit(0)
	}
	result, _ := json.Marshal(accepted())
	if kind == "codex" {
		if e := os.WriteFile(value("--output-last-message"), result, 0600); e != nil {
			os.Exit(13)
		}
		fmt.Println(`{"type":"turn.completed"}`)
	} else {
		if review {
			fmt.Printf("{\"subtype\":\"success\",\"is_error\":false,\"structured_output\":%s}\n", result)
		} else {
			fmt.Println(`{"subtype":"success","is_error":false,"result":"Done"}`)
		}
	}
	os.Exit(0)
}
func helper(t *testing.T, kind string) string {
	t.Helper()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	script := filepath.Join(t.TempDir(), kind)
	content := "#!/bin/sh\nexport LOOPSMITH_HELPER=1\nexport LOOPSMITH_KIND=" + kind + "\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestAgentHelper$ -- \"$@\"\n"
	if e = os.WriteFile(script, []byte(content), 0700); e != nil {
		t.Fatal(e)
	}
	return script
}
func TestRealAdapterLoopBothDirections(t *testing.T) {
	for _, builder := range []string{"claude", "codex"} {
		t.Run(builder+" builds", func(t *testing.T) {
			dir, s := setup(t)
			reviewer := "codex"
			if builder == "codex" {
				reviewer = "claude"
			}
			s.Config.Builder = AgentConfig{Kind: builder, Command: helper(t, builder)}
			s.Config.Reviewer = AgentConfig{Kind: reviewer, Command: helper(t, reviewer)}
			for _, a := range []AgentConfig{s.Config.Builder, s.Config.Reviewer} {
				if _, e := CheckAuthentication(context.Background(), a); e != nil {
					t.Fatal(e)
				}
			}
			lock, e := Lock(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer lock.Close()
			if e = Runtime(dir, s, lock, io.Discard).Execute(context.Background(), dir, s); e != nil {
				t.Fatal(e)
			}
			if s.Stage != "accepted" || s.Round != 1 || s.ActivePGID != 0 {
				t.Fatalf("bad state: %+v", s)
			}
		})
	}
}
func TestClaudeErrorEnvelopeFailsClosed(t *testing.T) {
	t.Setenv("LOOPSMITH_FAIL", "1")
	dir, s := setup(t)
	a := CLIAgent{Config: AgentConfig{Kind: "claude", Command: helper(t, "claude")}}
	if _, e := a.Review(context.Background(), s, dir, "Independently review TASK: inspect RECORDED DIFF:"); e == nil {
		t.Fatal("error envelope accepted")
	}
}
func TestParseReviewRejectsMalformedOutput(t *testing.T) {
	for _, v := range []string{`approved`, `{}`, `{"verdict":"accept","summary":"ok","findings":null}`, `{"verdict":"accept","summary":"ok","findings":[],"extra":true}`, `{"verdict":"accept","summary":"ok","findings":[]} {}`, `{"verdict":"changes_requested","summary":"bad","findings":[{"severity":"high","path":"../secret","line":1,"reason":"bad"}]}`} {
		if _, e := ParseReview([]byte(v)); e == nil {
			t.Errorf("accepted %s", v)
		}
	}
}
func TestSubscriptionEnvStripsAPIKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-secret")
	t.Setenv("ANTHROPIC_API_KEY", "test-secret")
	t.Setenv("LOOPSMITH_OTHER", "retained")
	env := strings.Join(SubscriptionEnv(), "\n")
	if strings.Contains(env, "test-secret") || !strings.Contains(env, "LOOPSMITH_OTHER=retained") {
		t.Fatal("incorrect authentication environment")
	}
}
func TestAPIAuthenticationRejected(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent")
			text := "Logged in using an API key"
			if kind == "claude" {
				text = `{"loggedIn":true,"authMethod":"api_key"}`
			}
			os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+text+"'\n"), 0700)
			if _, e := CheckAuthentication(context.Background(), AgentConfig{Kind: kind, Command: path}); e == nil {
				t.Fatal("API login accepted")
			}
		})
	}
}
