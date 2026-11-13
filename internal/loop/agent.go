package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ReviewSchema = `{"type":"object","additionalProperties":false,"properties":{"verdict":{"type":"string","enum":["accept","changes_requested"]},"summary":{"type":"string"},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"severity":{"type":"string","enum":["critical","high","medium","low"]},"path":{"type":"string"},"line":{"type":"integer","minimum":1},"reason":{"type":"string"}},"required":["severity","path","line","reason"]}}},"required":["verdict","summary","findings"]}`

type Finding struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Reason   string `json:"reason"`
}
type Review struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

func ParseReview(b []byte) (Review, error) {
	var r Review
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&r); e != nil {
		return r, e
	}
	if e := ensureEOF(d); e != nil {
		return r, e
	}
	if r.Verdict != "accept" && r.Verdict != "changes_requested" {
		return r, errors.New("missing or invalid review verdict")
	}
	if strings.TrimSpace(r.Summary) == "" || r.Findings == nil || len(r.Findings) > 100 {
		return r, errors.New("review requires summary and a findings array of at most 100 items")
	}
	blocking := false
	for _, f := range r.Findings {
		switch f.Severity {
		case "critical", "high", "medium":
			blocking = true
		case "low":
		default:
			return r, errors.New("invalid finding severity")
		}
		if f.Path == "" || filepath.IsAbs(f.Path) || filepath.Clean(f.Path) != f.Path || f.Path == ".." || strings.HasPrefix(f.Path, "../") || strings.Contains(f.Path, "\\") || f.Line < 1 || strings.TrimSpace(f.Reason) == "" {
			return r, errors.New("invalid finding location or reason")
		}
	}
	if (r.Verdict == "changes_requested") != blocking {
		return r, errors.New("review verdict contradicts finding severities")
	}
	return r, nil
}
func (r Review) ValidateLocations(ctx context.Context, s *State) error {
	tree, e := Snapshot(ctx, s.Worktree)
	if e != nil {
		return e
	}
	for _, f := range r.Findings {
		// Read blobs from the recorded trees, not filesystem symlinks.
		content, e := gitRaw(ctx, s.Worktree, "show", tree+":"+f.Path)
		if e != nil {
			content, e = gitRaw(ctx, s.Worktree, "show", s.Base+":"+f.Path)
		}
		if e != nil {
			return fmt.Errorf("finding refers to missing file %q", f.Path)
		}
		lines := strings.Count(content, "\n")
		if !strings.HasSuffix(content, "\n") {
			lines++
		}
		if lines == 0 {
			lines = 1
		}
		if f.Line > lines {
			return fmt.Errorf("finding line outside %q", f.Path)
		}
	}
	return nil
}

type Agent interface {
	Build(context.Context, *State, string, string) error
	Review(context.Context, *State, string, string) (Review, error)
}
type CLIAgent struct {
	Config  AgentConfig
	Process Process
}

func (a CLIAgent) invoke(ctx context.Context, s *State, dir, prefix, prompt string, review bool) ([]byte, error) {
	outPath := filepath.Join(dir, prefix+".stdout.log")
	errPath := filepath.Join(dir, prefix+".stderr.log")
	out, e := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	defer out.Close()
	errOut, e := os.OpenFile(errPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	defer errOut.Close()
	var args []string
	if a.Config.Kind == "codex" {
		mode := "workspace-write"
		if review {
			mode = "read-only"
		}
		result := filepath.Join(dir, prefix+".result.json")
		_ = os.Remove(result)
		args = []string{a.Config.Command, "exec", "--sandbox", mode, "-c", `approval_policy="never"`, "-c", `model_provider="openai"`, "--json", "--output-last-message", result}
		if review {
			schema := filepath.Join(dir, "review.schema.json")
			if e = os.WriteFile(schema, []byte(ReviewSchema), 0600); e != nil {
				return nil, e
			}
			args = append(args, "--output-schema", schema)
		}
		if a.Config.Model != "" {
			args = append(args, "--model", a.Config.Model)
		}
		args = append(args, "-")
		if e = a.Process.Run(ctx, s.Worktree, args, prompt, SubscriptionEnv(), out, errOut); e != nil {
			return nil, e
		}
		return os.ReadFile(result)
	}
	// Restrict the available tool surface as well as pre-approving selected tools.
	tools := "Read,Glob,Grep"
	mode := "default"
	if !review {
		tools += ",Edit,Write,Bash"
		mode = "acceptEdits"
	}
	args = []string{a.Config.Command, "-p", "--output-format", "json", "--permission-mode", mode, "--tools", tools}
	if review {
		args = append(args, "--allowedTools", "Read,Glob,Grep")
	} else {
		args = append(args, "--allowedTools", strings.Join(append([]string{"Read", "Glob", "Grep", "Edit", "Write"}, a.Config.AllowedTools...), ","))
	}
	if a.Config.Model != "" {
		args = append(args, "--model", a.Config.Model)
	}
	if review {
		args = append(args, "--json-schema", ReviewSchema)
	}
	if e = a.Process.Run(ctx, s.Worktree, args, prompt, SubscriptionEnv(), out, errOut); e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(outPath)
	if e != nil {
		return nil, e
	}
	var envelope struct {
		IsError    bool            `json:"is_error"`
		Result     string          `json:"result"`
		Structured json.RawMessage `json:"structured_output"`
		Subtype    string          `json:"subtype"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return nil, fmt.Errorf("invalid Claude result: %w", e)
	}
	if envelope.IsError || envelope.Subtype != "success" {
		return nil, fmt.Errorf("Claude did not complete successfully (subtype %q); see %s", envelope.Subtype, outPath)
	}
	if review {
		if len(envelope.Structured) == 0 {
			return nil, errors.New("Claude omitted structured review output")
		}
		return envelope.Structured, nil
	}
	return []byte(envelope.Result), nil
}
func (a CLIAgent) Build(ctx context.Context, s *State, dir, prompt string) error {
	_, e := a.invoke(ctx, s, dir, fmt.Sprintf("round-%02d-build", s.Round), prompt, false)
	return e
}
func (a CLIAgent) Review(ctx context.Context, s *State, dir, prompt string) (Review, error) {
	b, e := a.invoke(ctx, s, dir, fmt.Sprintf("round-%02d-review", s.Round), prompt, true)
	if e != nil {
		return Review{}, e
	}
	return ParseReview(b)
}

// CheckAuthentication fails closed instead of silently falling back to API billing.
func CheckAuthentication(ctx context.Context, a AgentConfig) (string, error) {
	args := []string{a.Command, "login", "status"}
	if a.Kind == "claude" {
		args = []string{a.Command, "auth", "status"}
	}
	var b bytes.Buffer
	if e := (Process{}).Run(ctx, "", args, "", SubscriptionEnv(), &b, &b); e != nil {
		return "", fmt.Errorf("%s authentication unavailable: %w", a.Kind, e)
	}
	if a.Kind == "codex" {
		if !strings.Contains(b.String(), "Logged in using ChatGPT") {
			return "", errors.New("Codex must be signed in with ChatGPT; API authentication is not supported")
		}
		return "ChatGPT", nil
	}
	var auth struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if e := json.Unmarshal(b.Bytes(), &auth); e != nil {
		return "", fmt.Errorf("read Claude authentication: %w", e)
	}
	if !auth.LoggedIn || auth.AuthMethod != "claude.ai" {
		return "", fmt.Errorf("Claude must use a claude.ai subscription login (found %q)", auth.AuthMethod)
	}
	return "claude.ai", nil
}
