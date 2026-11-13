package loop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	e := (Process{}).Run(ctx, dir, append([]string{"git"}, args...), "", os.Environ(), &out, &errOut)
	if e != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), e, errOut.String())
	}
	return out.String(), nil
}
func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, e := gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), e
}
func Repository(ctx context.Context, path string) (string, error) {
	return git(ctx, path, "rev-parse", "--show-toplevel")
}
func Prepare(ctx context.Context, repo, dir, id string, c Config, task string) (State, error) {
	var s State
	if strings.TrimSpace(task) == "" {
		return s, errors.New("task must not be empty")
	}
	if e := c.Validate(); e != nil {
		return s, e
	}
	root, e := Repository(ctx, repo)
	if e != nil {
		return s, e
	}
	clean, e := git(ctx, root, "status", "--porcelain")
	if e != nil {
		return s, e
	}
	if clean != "" {
		return s, errors.New("source repository must be clean; commit or stash changes first")
	}
	base, e := git(ctx, root, "rev-parse", "HEAD")
	if e != nil {
		return s, e
	}
	duration, _ := time.ParseDuration(c.RunTimeout)
	s = State{Version: 1, ID: id, Repository: root, Worktree: filepath.Join(dir, "worktree"), Branch: "niz/loopsmith-" + id, Base: base, Task: task, Config: c, Stage: "preparing", Started: time.Now().UTC(), Deadline: time.Now().UTC().Add(duration)}
	if e = Save(dir, &s); e != nil {
		return s, e
	}
	if _, e = git(ctx, root, "worktree", "add", "-b", s.Branch, s.Worktree, base); e != nil {
		return s, e
	}
	s.Stage = "build"
	return s, Save(dir, &s)
}

// Snapshot uses a private index: tracked and untracked non-ignored contents and
// executable bits are covered without changing the user's staging area.
func Snapshot(ctx context.Context, dir string) (string, error) {
	f, e := os.CreateTemp("", "loopsmith-index-*")
	if e != nil {
		return "", e
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	defer os.Remove(name)
	defer os.Remove(name + ".lock")
	env := append(os.Environ(), "GIT_INDEX_FILE="+name)
	run := func(args ...string) (string, error) {
		var b, errOut bytes.Buffer
		e := (Process{}).Run(ctx, dir, append([]string{"git"}, args...), "", env, &b, &errOut)
		if e != nil {
			return "", fmt.Errorf("snapshot: %w: %s", e, errOut.String())
		}
		return strings.TrimSpace(b.String()), nil
	}
	if _, e = run("read-tree", "HEAD"); e != nil {
		return "", e
	}
	if _, e = run("add", "-A", "--", "."); e != nil {
		return "", e
	}
	return run("write-tree")
}
func Reconcile(ctx context.Context, s *State) error {
	root, e := Repository(ctx, s.Worktree)
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	expected, e := filepath.EvalSymlinks(s.Worktree)
	if e != nil {
		return e
	}
	if actual != expected {
		return errors.New("worktree path no longer matches")
	}
	head, e := git(ctx, s.Worktree, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	if head != s.Base {
		return errors.New("worktree HEAD changed; inspect it before continuing")
	}
	branch, e := git(ctx, s.Worktree, "branch", "--show-current")
	if e != nil {
		return e
	}
	if branch != s.Branch {
		return errors.New("worktree branch changed")
	}
	return nil
}
