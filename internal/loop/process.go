//go:build darwin || linux

package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func Lock(dir string) (*os.File, error) {
	f, e := os.OpenFile(dir+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("run is already active (or an agent is still running)")
	}
	return f, nil
}

// Closing the descriptor releases the lock; children inherit it so a coordinator
// crash cannot immediately admit another writer while its agent is alive.
type Process struct {
	Lock    *os.File
	OnStart func(int) error
	OnStop  func() error
}

func (p Process) Run(ctx context.Context, dir string, argv []string, input string, env []string, out, errOut io.Writer) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if p.Lock != nil {
		cmd.ExtraFiles = []*os.File{p.Lock}
	}
	cmd.Cancel = func() error {
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	cmd.WaitDelay = 2 * time.Second
	if e := cmd.Start(); e != nil {
		return e
	}
	if p.OnStart != nil {
		if e := p.OnStart(cmd.Process.Pid); e != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return e
		}
	}
	e := cmd.Wait()
	// Do not leave background workers holding the run or editing after handoff.
	killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if killErr != nil && killErr != syscall.ESRCH {
		return fmt.Errorf("stop command descendants: %w", killErr)
	}
	if p.OnStop != nil {
		if se := p.OnStop(); se != nil {
			return se
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return e
}
func GroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	e := syscall.Kill(-pgid, 0)
	return e == nil || e == syscall.EPERM
}
func SubscriptionEnv() []string {
	var env []string
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		switch k {
		case "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY":
			continue
		}
		env = append(env, v)
	}
	return env
}
