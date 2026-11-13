package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nizamphoenix/loopsmith/internal/loop"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "loopsmith:", e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		path := fs.String("config", "loopsmith.json", "config file")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected positional arguments; use --help for usage")
		}
		b, _ := json.MarshalIndent(loop.DefaultConfig(), "", "  ")
		f, e := os.OpenFile(*path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(append(b, '\n'))
		ce := f.Close()
		if e != nil {
			return e
		}
		fmt.Println("Created", *path, "— set checks for your project before running.")
		return ce
	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
		path := fs.String("config", "loopsmith.json", "config file")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		c, e := loop.ReadConfig(*path)
		if e != nil {
			return e
		}
		var failures []error
		for _, a := range []loop.AgentConfig{c.Builder, c.Reviewer} {
			binary, e := exec.LookPath(a.Command)
			if e != nil {
				failures = append(failures, fmt.Errorf("%s: %w", a.Kind, e))
				continue
			}
			fmt.Println(a.Kind+":", binary)
			timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
			method, e := loop.CheckAuthentication(timeout, a)
			if e == nil {
				fmt.Println("  subscription authentication:", method)
			}
			cancel()
			if e != nil {
				failures = append(failures, fmt.Errorf("%s authentication check: %w", a.Kind, e))
			}
		}
		return errors.Join(failures...)
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		repo := fs.String("repo", ".", "source Git repository")
		config := fs.String("config", "loopsmith.json", "config file")
		task := fs.String("task", "", "task description")
		taskFile := fs.String("task-file", "", "read task from file")
		stateRoot := fs.String("state-dir", "", "run storage directory (outside repository)")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected positional arguments; use --help for usage")
		}
		if *task != "" && *taskFile != "" {
			return errors.New("use --task or --task-file, not both")
		}
		if *taskFile != "" {
			b, e := os.ReadFile(*taskFile)
			if e != nil {
				return e
			}
			*task = string(b)
		}
		if strings.TrimSpace(*task) == "" {
			return errors.New("provide --task or --task-file")
		}
		c, e := loop.ReadConfig(*config)
		if e != nil {
			return e
		}
		for _, a := range []loop.AgentConfig{c.Builder, c.Reviewer} {
			if _, e = exec.LookPath(a.Command); e != nil {
				return e
			}
		}
		if e = authenticate(ctx, c); e != nil {
			return e
		}
		root, e := loop.Repository(ctx, *repo)
		if e != nil {
			return e
		}
		if *stateRoot == "" {
			cache, e := os.UserCacheDir()
			if e != nil {
				return e
			}
			*stateRoot = filepath.Join(cache, "loopsmith", "runs")
		}
		absolute, e := filepath.Abs(*stateRoot)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(absolute, 0700); e != nil {
			return e
		}
		absolute, e = filepath.EvalSymlinks(absolute)
		if e != nil {
			return e
		}
		root, e = filepath.EvalSymlinks(root)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, absolute)
		if e != nil {
			return e
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return errors.New("state directory must be outside the source repository")
		}
		token := make([]byte, 6)
		if _, e = rand.Read(token); e != nil {
			return e
		}
		id := time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(token)
		dir := filepath.Join(absolute, id)
		if e = os.Mkdir(dir, 0700); e != nil {
			return e
		}
		lock, e := loop.Lock(dir)
		if e != nil {
			return e
		}
		defer lock.Close()
		fmt.Println("Run:", dir)
		s, e := loop.Prepare(ctx, root, dir, id, c, *task)
		if e != nil {
			return e
		}
		return execute(ctx, dir, &s, lock)
	case "resume", "status":
		if len(args) != 2 {
			return errors.New("provide exactly one run directory")
		}
		dir, e := filepath.Abs(args[1])
		if e != nil {
			return e
		}
		if args[0] == "status" {
			s, e := loop.Load(dir)
			if e != nil {
				return e
			}
			b, e := json.MarshalIndent(s, "", "  ")
			if e != nil {
				return e
			}
			fmt.Println(string(b))
			return nil
		}
		lock, e := loop.Lock(dir)
		if e != nil {
			return e
		}
		defer lock.Close()
		s, e := loop.Load(dir)
		if e != nil {
			return e
		}
		return execute(ctx, dir, &s, lock)
	case "help", "--help", "-h":
		return usage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func execute(ctx context.Context, dir string, s *loop.State, lock *os.File) error {
	if s.Stage != "accepted" {
		if e := authenticate(ctx, s.Config); e != nil {
			return e
		}
	}
	e := loop.Runtime(dir, s, lock, os.Stdout).Execute(ctx, dir, s)
	if e != nil {
		fmt.Fprintln(os.Stderr, "Progress saved:", dir, "\nWorktree:", s.Worktree)
		return e
	}
	fmt.Println("Local review accepted. Worktree:", s.Worktree, "\nBranch:", s.Branch, "\nReviewed tree:", s.AcceptedTree)
	return nil
}
func usage() error {
	fmt.Println(`Loopsmith — local Claude Code / Codex review loops (macOS and Linux)

  loopsmith init [--config PATH]
  loopsmith doctor [--config PATH]
  loopsmith run --repo PATH --config PATH --task "..." [--state-dir PATH]
  loopsmith run --repo PATH --config PATH --task-file PATH
  loopsmith status RUN_DIRECTORY
  loopsmith resume RUN_DIRECTORY

Configure either tool as builder or reviewer. Check logs in the run directory.
Local acceptance preserves edits on an isolated branch; publishing is not yet automated.`)
	return nil
}

func authenticate(ctx context.Context, c loop.Config) error {
	for _, a := range []loop.AgentConfig{c.Builder, c.Reviewer} {
		t, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, e := loop.CheckAuthentication(t, a)
		cancel()
		if e != nil {
			return e
		}
	}
	return nil
}
