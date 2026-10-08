# stubbs

A terminal coding agent. You describe a task, the model proposes shell
commands, you approve (or don't), and stubbs runs them against your working
directory — with an optional [bubblewrap](https://github.com/containers/bubblewrap)
sandbox, cost/step limits, session transcripts, and a benchmark harness.

Stubbs speaks to [OpenRouter](https://openrouter.ai), so any model in its
catalog can drive it — including switching models mid-session.

## Features

- **Interactive TUI** (Bubble Tea) with streaming tool output, collapsible tool
  blocks, markdown-ish rendering and a `@` file/directory picker.
- **Three modes** — `confirm` (ask before each command, the default), `yolo`
  (run everything), and `human` (you type the commands, the model watches).
- **Sandboxed bash** — when `bwrap` is available, commands run with the host
  filesystem read-only, writes confined to the working directory, no network,
  and a cleared environment (`--workdir` sets the root).
- **Budgets** — step and dollar limits, enforced per turn, adjustable on the fly.
- **Sessions** — every conversation is appended to a JSONL transcript under
  `.stubbs/sessions/`, and a run can be exported with `--output` as a
  `stubbs-trajectory-v1` artifact.
- **Benchmark-ready** — `--auto-quit` plus stdin task input, used by the
  SWE-bench and polyglot harnesses in `bench/`.

## Requirements

- Go 1.25+ to build
- An OpenRouter API key
- Optional: `bwrap` for sandboxed command execution

## Build

```bash
go build -o stubbs ./cmd/stubbs
```

## Setup

Run stubbs once and the first-run wizard asks for an API key, model and
environment. Re-run it any time with:

```bash
./stubbs --config
```

Settings are stored in `.stubbs/` **relative to the current directory**:

```
.stubbs/
  config.yaml   # provider, models, env, theme (non-secret)
  .env          # API_KEY / STUBBS_API_KEY
  sessions/     # JSONL transcripts
  context/      # context artifacts
  memory/       # memory store (reserved)
```

The API key may also come from the environment, which is how containers and
benchmarks configure it: `STUBBS_API_KEY` or `OPENROUTER_API_KEY`. Other
overrides: `STUBBS_PROVIDER`, `STUBBS_MODEL`, `STUBBS_ENV`, `STUBBS_THEME`.

## Usage

```bash
# Solve a task (prompts for confirmation before running commands)
./stubbs -t "add a healthcheck endpoint and a test for it"

# Read the task from stdin
cat issue.txt | ./stubbs -t -

# Autonomous run with tighter limits
./stubbs -y -s 40 -c 2.0 -C ./repo -t "fix the failing tests"

# Interactive session with no initial task
./stubbs
```

### Flags

| Flag | Description |
|---|---|
| `-m, --model` | Model to use (overrides config) |
| `-t, --task` | Task text; `-` reads stdin |
| `-c, --cost-limit` | Cost limit in dollars, `0` disables (default 5) |
| `-s, --step-limit` | Maximum number of agent steps (default 24) |
| `-y, --yolo` | Execute without confirmation |
| `-H, --human` | Start in human mode |
| `--whitelist` | Regexes for commands that skip confirmation |
| `-C, --workdir` | Directory tools are confined to (default: cwd) |
| `--read-secrets` | Let the `@` picker attach `.env`-style files |
| `-o, --output` | Write the run to a JSON trajectory file |
| `--exit-immediately` | Don't confirm when the agent wants to finish |
| `--auto-quit` | Quit when the run ends, implies `--exit-immediately` |
| `--config` | Run the configuration wizard and exit |
| `--version` | Print version and exit |

### In-session commands and keys

| Input | Action |
|---|---|
| `/y`, `/c`, `/u` | Switch to yolo, confirm or human mode |
| `/models` | Browse and switch the active model |
| `/m` | Expand the composer for multiline input |
| `/h` | Show help |
| `@` | Open the file/directory picker (fuzzy filter, `enter` to attach) |
| `ctrl+o` | Expand/collapse tool output blocks |
| `ctrl+e` | Expand/collapse the input box |
| `ctrl+c` / `esc` | Interrupt the running agent; again to quit |

## Architecture

```
cmd/stubbs/     CLI entrypoint: flags, config resolution, wiring, run summary
src/agent/      Agent loop, interactive agent (modes, approvals, interrupts),
                session transcripts, context accounting
src/llm/        OpenRouter client, streaming/retries, model catalog
src/tools/      BashTool (timeouts, output caps) and the bwrap sandbox
src/env/        Environment abstraction and local execution
src/types/      Tool contract, Registry, messages
src/tui/        Bubble Tea UI, dialogs, mention picker, themes, config wizard
src/config/     config.yaml + .env loading, atomic saves, legacy migration
src/prompts/    Embedded prompt fragments (repo map)
bench/          SWE-bench Verified harness (see bench/README.md)
bench/polyglot/ Aider Polyglot dev-loop harness (see bench/polyglot/README.md)
```

The `Tool` contract is small — `Name`, `Description`, `Parameters`
(JSON Schema) and `Execute` — and tools are registered in a `Registry` in
`cmd/stubbs/main.go`. Today `bash` is the only registered tool; file access
happens through it. Turning that hardcoded wiring into loadable configuration
profiles is designed in [`plan.md`](plan.md).

## Testing

```bash
go test ./...
```

## Benchmarking

- `bench/` — stubbs on SWE-bench Verified, run in the official per-instance
  docker images and graded with the upstream `swebench` harness.
- `bench/polyglot/` — the fast ~15 minute regression signal: seeded Exercism
  exercises in python/go/javascript, scored by their test suites.

See the README in each directory for setup and invocation.

## Status

Work in progress on the `dev` branch. Known rough edges and planned work live
in [`plan.md`](plan.md), [`step2.md`](step2.md) and [`ui_fixes.md`](ui_fixes.md).
