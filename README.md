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
go build -o stubbs ./cmd/stubbs        # the coding agent
go build -o stubbs-chat ./cmd/chat     # the chat app
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

## Chat

`stubbs-chat` is a separate binary — a conversational app built on the same
agent loop, LLM client, environment and tool registry as the coding agent,
but exposing a single **web browsing** tool instead of `bash`. It keeps the
same config (`.stubbs/`), so the first run reuses the wizard/API key.

```bash
go build -o stubbs-chat ./cmd/chat
./stubbs-chat                     # start chatting
./stubbs-chat -m openai/gpt-4o     # choose a model for this session
```

Flags: `-m/--model`, `-c/--cost-limit` (default 1), `-s/--step-limit` (model
steps per turn, default 50), `--config`, `--version`.

In the chat UI:

| Input | Action |
|---|---|
| `enter` | send the message |
| `ctrl+j` | newline |
| `ctrl+c` / `esc` | stop the current reply (again to quit) |
| `ctrl+o` | toggle web tool output |
| `pgup` / `pgdn` | scroll the transcript (mouse wheel works too) |
| `shift+up` / `shift+down` | scroll one line |
| `ctrl+home` / `ctrl+end` | jump to the first / latest message |
| `/clear` | clear the transcript |
| `/help` | show help |

The transcript follows new messages until you scroll up; a status hint shows
the scroll position, and returning to the bottom (or `ctrl+end`) resumes
auto-follow.

## Architecture

```
cmd/stubbs/     Coding-agent CLI entrypoint: flags, config, wiring, summary
cmd/chat/       Chat-app CLI entrypoint (same internals, web tool only)
src/agent/      Agent loop, interactive agent (modes, approvals, interrupts),
                chat session, session transcripts, context accounting
src/llm/        OpenRouter client, streaming/retries, model catalog
src/tools/      BashTool (timeouts, output caps), the bwrap sandbox, WebTool
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
