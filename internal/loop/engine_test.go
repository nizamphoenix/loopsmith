package loop

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeAgent struct {
	build           func(*State) error
	review          func(*State) (Review, error)
	builds, reviews int
}

func (a *fakeAgent) Build(_ context.Context, s *State, _, _ string) error {
	a.builds++
	if a.build != nil {
		return a.build(s)
	}
	return nil
}
func (a *fakeAgent) Review(_ context.Context, s *State, _, _ string) (Review, error) {
	a.reviews++
	if a.review != nil {
		return a.review(s)
	}
	return accepted(), nil
}
func accepted() Review {
	return Review{Verdict: "accept", Summary: "Checks and review pass", Findings: []Finding{}}
}
func setup(t *testing.T) (string, *State) {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, e := git(ctx, repo, args...); e != nil {
			t.Fatal(e)
		}
	}
	mustGit("init", "-b", "main")
	mustGit("config", "user.email", "test@example.invalid")
	mustGit("config", "user.name", "Test")
	if e := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("old\n"), 0600); e != nil {
		t.Fatal(e)
	}
	mustGit("add", ".")
	mustGit("commit", "-m", "test: baseline")
	dir := t.TempDir()
	c := DefaultConfig()
	c.Checks = [][]string{{"/bin/sh", "-c", "test -f file.txt"}}
	s, e := Prepare(ctx, repo, dir, "test", c, "Replace old with new")
	if e != nil {
		t.Fatal(e)
	}
	return dir, &s
}
func TestLoopFixesAndAccepts(t *testing.T) {
	dir, s := setup(t)
	builder := &fakeAgent{build: func(s *State) error {
		return os.WriteFile(filepath.Join(s.Worktree, "file.txt"), []byte("new\n"), 0600)
	}}
	reviewer := &fakeAgent{review: func(s *State) (Review, error) {
		if s.Round == 1 {
			return Review{Verdict: "changes_requested", Summary: "Fix correctness", Findings: []Finding{{Severity: "high", Path: "file.txt", Line: 1, Reason: "Needs a fix"}}}, nil
		}
		return accepted(), nil
	}}
	e := Engine{Builder: builder, Reviewer: reviewer}
	if err := e.Execute(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if s.Stage != "accepted" || s.Round != 2 || s.AcceptedTree == "" || builder.builds != 2 {
		t.Fatalf("unexpected state: %+v", s)
	}
	saved, err := Load(dir)
	if err != nil || saved.AcceptedTree != s.AcceptedTree {
		t.Fatalf("state not persisted: %v", err)
	}
	// A duplicate resume of accepted contents must not call either agent again.
	if err = e.Execute(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if builder.builds != 2 {
		t.Fatal("replayed accepted task")
	}
	os.WriteFile(filepath.Join(s.Worktree, "file.txt"), []byte("changed later\n"), 0600)
	if err = e.Execute(context.Background(), dir, s); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale acceptance: %v", err)
	}
}
func TestChecksPreventReviewAndFeedBuilder(t *testing.T) {
	dir, s := setup(t)
	s.Config.Checks = [][]string{{"/bin/sh", "-c", "grep -q fixed file.txt"}}
	b := &fakeAgent{build: func(s *State) error {
		if s.Round == 2 {
			if !strings.Contains(s.Feedback, "Local check") {
				t.Fatal("missing check feedback")
			}
			return os.WriteFile(filepath.Join(s.Worktree, "file.txt"), []byte("fixed\n"), 0600)
		}
		return nil
	}}
	r := &fakeAgent{}
	if err := (Engine{Builder: b, Reviewer: r}).Execute(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if s.Round != 2 || r.reviews != 1 {
		t.Fatalf("rounds=%d reviews=%d", s.Round, r.reviews)
	}
}
func TestRepeatedFindingsStopAtLimit(t *testing.T) {
	dir, s := setup(t)
	s.Config.MaxRounds = 2
	r := &fakeAgent{review: func(*State) (Review, error) {
		return Review{Verdict: "changes_requested", Summary: "Still broken", Findings: []Finding{{Severity: "medium", Path: "file.txt", Line: 1, Reason: "Wrong result"}}}, nil
	}}
	e := Engine{Builder: &fakeAgent{}, Reviewer: r}
	if err := e.Execute(context.Background(), dir, s); err == nil {
		t.Fatal("expected exhausted rounds")
	}
	if s.Stage != "blocked" || s.Round != 2 {
		t.Fatalf("unexpected state: %+v", s)
	}
	if err := e.Execute(context.Background(), dir, s); err == nil || r.reviews != 2 {
		t.Fatal("blocked task replayed")
	}
}
func TestInvalidReviewNeverAccepts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		review Review
		err    error
	}{
		{"missing verdict", Review{}, nil},
		{"contradiction", Review{Verdict: "accept", Summary: "ok", Findings: []Finding{{Severity: "high", Path: "file.txt", Line: 1, Reason: "bad"}}}, nil},
		{"bad location", Review{Verdict: "changes_requested", Summary: "bad", Findings: []Finding{{Severity: "high", Path: "missing.txt", Line: 1, Reason: "bad"}}}, nil},
		{"tool failed", Review{}, errors.New("tool crashed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, s := setup(t)
			r := &fakeAgent{review: func(*State) (Review, error) { return tc.review, tc.err }}
			if err := (Engine{Builder: &fakeAgent{}, Reviewer: r}).Execute(context.Background(), dir, s); err == nil {
				t.Fatal("invalid review accepted")
			}
			if s.AcceptedTree != "" {
				t.Fatal("invalid review got approval")
			}
		})
	}
}
func TestMutationDuringReviewRejected(t *testing.T) {
	dir, s := setup(t)
	r := &fakeAgent{review: func(s *State) (Review, error) {
		os.WriteFile(filepath.Join(s.Worktree, "untracked.txt"), []byte("oops"), 0600)
		return accepted(), nil
	}}
	if err := (Engine{Builder: &fakeAgent{}, Reviewer: r}).Execute(context.Background(), dir, s); err == nil || !strings.Contains(err.Error(), "changed during review") {
		t.Fatalf("mutation accepted: %v", err)
	}
}
func TestCrashRecoveryConsumesRound(t *testing.T) {
	dir, s := setup(t)
	s.Round = 1
	s.Stage = "reviewing"
	Save(dir, s)
	b := &fakeAgent{build: func(s *State) error {
		if !strings.Contains(s.Feedback, "interrupted") {
			t.Fatal("missing recovery instructions")
		}
		return nil
	}}
	if err := (Engine{Builder: b, Reviewer: &fakeAgent{}}).Execute(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if s.Round != 2 || b.builds != 1 {
		t.Fatal("did not restart conservatively")
	}
}
func TestDeadlineAndCancellation(t *testing.T) {
	dir, s := setup(t)
	s.Deadline = time.Now().Add(-time.Second)
	b := &fakeAgent{}
	if err := (Engine{Builder: b, Reviewer: &fakeAgent{}}).Execute(context.Background(), dir, s); err == nil {
		t.Fatal("expired task ran")
	}
	if b.builds != 0 {
		t.Fatal("builder ran after deadline")
	}
}
func TestCheckTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "leak")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := (Process{}).Run(ctx, dir, []string{"/bin/sh", "-c", "(sleep 0.4; touch \"$1\") & wait", "sh", marker}, "", os.Environ(), io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("descendant survived timeout")
	}
}
func TestLockPreventsConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	a, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := Lock(dir); e == nil {
		b.Close()
		t.Fatal("second writer acquired lock")
	}
	a.Close()
	b, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
}
func TestChangedHEADStopsResume(t *testing.T) {
	dir, s := setup(t)
	if _, e := git(context.Background(), s.Worktree, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "unexpected commit"); e != nil {
		t.Fatal(e)
	}
	b := &fakeAgent{}
	if err := (Engine{Builder: b, Reviewer: &fakeAgent{}}).Execute(context.Background(), dir, s); err == nil {
		t.Fatal("changed HEAD accepted")
	}
	if b.builds != 0 {
		t.Fatal("builder ran in changed worktree")
	}
}
func TestSnapshotIncludesUntrackedWithoutStaging(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	first, e := Snapshot(ctx, s.Worktree)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(s.Worktree, "new.txt"), []byte("new"), 0600)
	second, e := Snapshot(ctx, s.Worktree)
	if e != nil {
		t.Fatal(e)
	}
	if first == second {
		t.Fatal("untracked file not hashed")
	}
	staged, e := git(ctx, s.Worktree, "diff", "--cached", "--name-only")
	if e != nil || staged != "" {
		t.Fatalf("private index modified staging: %s %v", staged, e)
	}
}

func TestSnapshotKeepsTrackedIgnoredFiles(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(s.Worktree, ".gitignore"), []byte("file.txt\n"), 0600)
	if _, e := git(ctx, s.Worktree, "add", ".gitignore"); e != nil {
		t.Fatal(e)
	}
	tree, e := Snapshot(ctx, s.Worktree)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = git(ctx, s.Worktree, "show", tree+":file.txt"); e != nil {
		t.Fatal("tracked file incorrectly omitted:", e)
	}
}
func TestLocationValidationPreservesBlankLines(t *testing.T) {
	_, s := setup(t)
	os.WriteFile(filepath.Join(s.Worktree, "file.txt"), []byte("\n\nlast\n\n"), 0600)
	r := Review{Findings: []Finding{{Path: "file.txt", Line: 4}}}
	if e := r.ValidateLocations(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	r.Findings[0].Line = 5
	if e := r.ValidateLocations(context.Background(), s); e == nil {
		t.Fatal("out-of-range line accepted")
	}
}

func TestChildKeepsLockAfterCoordinatorDescriptorCloses(t *testing.T) {
	dir := t.TempDir()
	lock, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := Process{Lock: lock, OnStart: func(int) error { close(started); return nil }}
	go func() {
		finished <- p.Run(ctx, dir, []string{"/bin/sleep", "10"}, "", os.Environ(), io.Discard, io.Discard)
	}()
	<-started
	lock.Close()
	if other, e := Lock(dir); e == nil {
		other.Close()
		t.Fatal("orphaned command allowed a concurrent writer")
	}
	cancel()
	<-finished
	other, e := Lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	other.Close()
}
