# Loopsmith

Bring Claude Code and Codex together for local development and independent review. Choose either tool to build, let the other review, and let Loopsmith carry findings back through the fix loop.

Loopsmith uses your installed command-line tools and subscription logins. It does not call model APIs directly or require API keys.

**Early implementation:** the local build → check → review → fix loop works with simulated agents in both directions. A real two-platform acceptance run is still required. Automatic publishing, CI follow-up, a dashboard, and Windows support are not implemented.

## Start a task

Requirements: macOS or Linux, Git, Go 1.25+ to build, and current Claude Code and Codex CLIs on your PATH. Sign in using `claude auth login` and `codex login` with your subscription accounts.

```sh
go build -o bin/loopsmith ./cmd/loopsmith
./bin/loopsmith init
```

Edit `loopsmith.json` to set checks for the repository you want to work on. Checks are argument arrays, executed directly without a shell. For a Node project, for example, use `[["npm", "test"], ["npm", "run", "build"]]`. Ensure dependencies are available in new worktrees, or configure a setup command as the first check. Checks run each round.

```sh
./bin/loopsmith doctor --config loopsmith.json
./bin/loopsmith run --repo /absolute/path/to/repository \
  --config loopsmith.json --task "Fix the failing pagination behavior"
```

The source repository must have a commit and no uncommitted changes. Keep your local config outside that repository or add it to that repository's local Git exclusions. Loopsmith's own `loopsmith.json` is already ignored.

To swap roles, exchange the complete `builder` and `reviewer` objects:

```json
{
  "builder": { "kind": "codex", "command": "codex" },
  "reviewer": { "kind": "claude", "command": "claude" },
  "checks": [["go", "test", "./..."]],
  "max_rounds": 3,
  "command_timeout": "20m",
  "run_timeout": "2h"
}
```

Each agent accepts an optional `model`. `command` may be an absolute executable path. For Claude builders, optional `allowed_tools` can pre-approve specific shell commands, for example `["Bash(go test:*)"]`. File edits are pre-approved; arbitrary shell commands are not. Loopsmith runs the configured checks itself.

## What happens without supervision

1. Loopsmith creates a branch named `niz/loopsmith-<run-id>` in a new worktree.
2. The builder implements your task.
3. The configured checks run; failures go back to the builder.
4. The other tool reviews the recorded diff and current files.
5. Critical, high, and medium findings return to the builder; low findings are advisory.
6. An accepted review records the Git tree hash and leaves your changes in the worktree.

Routine failed checks and review findings need no manual handoff. The default limit is three build/check/review rounds, including rounds that fail checks. Commands have a 20-minute timeout and the run has a two-hour deadline. Loopsmith stops on tool/authentication failures, invalid review output, unexpected Git changes, or exhausted limits. It does not bypass permission controls or automatically switch to API billing. Subscription rate limits currently stop the run; automatic waiting for a reset is not implemented.

Run in a terminal and leave the process running. It is a foreground CLI, not a background service. No desktop notification system is included yet; progress and completion appear in terminal output.

## Inspect and resume

The command prints its run directory. By default it lives under your operating system's user cache directory, in `loopsmith/runs/`. Use `--state-dir /absolute/path` for durable storage you control; it must be outside the source repository. Do not clear that directory while runs or worktrees are needed.

```sh
./bin/loopsmith status /absolute/path/to/run-directory
./bin/loopsmith resume /absolute/path/to/run-directory
```

The directory contains `state.json`, per-round tool output, check logs and outcomes, structured findings, and the worktree. Logs can contain repository contents and model output. Keep run storage private.

Progress uses atomically replaced JSON files, with no database dependency. Storage is separate from agent execution so a future database can replace it without changing tool adapters.

On interruption, Loopsmith retains edits. Resume first checks the worktree branch, baseline commit, active command group, and saved limits. An uncertain build/check/review restarts at the builder with recovery context and consumes another round. It never reuses an interrupted review. An already accepted task is verified against its recorded tree without running either agent again; later edits invalidate that acceptance. Resume does not extend deadlines or reset round limits.

One process owns a run lock. Agent/check processes inherit the lock so another coordinator cannot immediately start writing after a coordinator crash. Commands run in their own process groups; cancellation kills the group. Surviving groups prevent resume. Programs that deliberately detach into new process groups are outside this supervision model.

## Permissions and boundaries

Codex builds use the workspace-write sandbox and reviews use the read-only sandbox. Claude builds use `acceptEdits` with explicit tool approvals; Claude reviews expose only Read, Glob, and Grep. The coordinator supplies the diff to both reviewers, so Claude does not need shell access for review. Claude tool restrictions are not an operating-system read-only mount. Existing CLI settings, hooks, helpers, and integrations still need to be trusted. Use trusted repositories and configurations; Git worktrees are isolation for edits, not a security sandbox.

API-key environment overrides are removed from child processes. Authentication preflight requires ChatGPT sign-in for Codex and a `claude.ai` login for Claude. The tools retain their own credential storage. Usage events remain in raw tool logs where available; there is no accurate monetary total or spending cap.

Review output must match a schema and contain valid severities, file locations, and reasons. A failed tool or malformed response cannot count as acceptance. Reviews cover tracked files and untracked files not excluded by Git, including deletions and executable bits. Ignored generated files are excluded. Diffs over 2 MiB stop with an explicit error. Acceptance proves which contents were reviewed; it does not guarantee correctness.

Local acceptance does not commit, push, open a PR, or merge. Inspect the reported worktree and commit/push there when ready. Keep using your normal CI before merging.

## Development

```sh
go test -race ./...
go vet ./...
go build ./cmd/loopsmith
```

Tests run fake tool executables through the actual adapters and process runner. They exercise role reversal, failed checks, repeated findings, malformed output, interruption recovery, stale acceptance, timeouts, concurrent writers, and changed Git state without spending subscription usage.

The next milestones are a real Claude/Codex task, bounded recovery from transient tool failures, automatic publishing with duplicate-action recovery, CI follow-up, and then a shared activity view.

Licensed under Apache-2.0.
