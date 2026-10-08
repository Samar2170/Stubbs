# Extensible harness: configuration profiles

Goal: make stubbs loadable in different configurations with different tool
options, so the same binary can be tuned per task instead of being recompiled.

Today the wiring is hardcoded in `cmd/stubbs/main.go`; this document plans how to
turn that into a resolvable, inspectable **profile**.

Status: proposal. Nothing here is implemented yet.

---

## 1. Current state

### What is already extensible

The substrate is better than most projects at this stage:

| Piece | Location | Notes |
|---|---|---|
| `types.Tool` interface | `src/types/tool.go` | Clean 4-method contract: `Name/Description/Parameters/Execute` |
| `types.Registry` | `src/types/tool.go` | Register/Get/List |
| `env.Environment` | `src/env/env.go` | Interface with one implementation (`LocalEnvironment`) |
| `ApprovalConfig` | `src/config/env.go` | Persisted YAML section, per-tool allow/confirm |
| `MemoryConfig` | `src/config/env.go` | Persisted YAML section |
| StubbsConfig load/save | `src/config/env.go` | Atomic writes, env override, legacy migration |

### The problem

Everything is assembled inline in `run()` in `cmd/stubbs/main.go`:

```go
registry.Register(bashTool)
registry.Register(tools.NewFileReadTool(workdir))
registry.Register(tools.NewFileWriteTool(workdir))
registry.Register(tools.NewFileListTool(workdir))
registry.Register(tools.NewFileEditTool(workdir))
registry.Register(tools.NewWebFetchTool())
if memStore != nil { registry.Register(tools.NewMemoryTool(memStore)) }
```

Dropping `web_fetch`, disabling memory, or changing the bash timeout requires
editing Go and rebuilding. There is no seam between *what the agent is* and
*how it is configured*.

### Findings that constrain the design

1. **`Registry.List()` iterates a map**, so the `tools` array in every API
   request is in random order. This breaks prompt-cache hits and makes benchmark
   runs non-reproducible. Should be fixed before anything depends on tool order.

2. **`Environment` is pluggable but singular.** `KnownEnvs = ["local", "docker"]`
   is declared in `src/config/env.go`, but no docker environment exists. The
   config advertises an option that cannot be selected.

3. **Whitelist and approval are the same concept in two homes.** `--whitelist`
   is CLI-only (regex list); `approval.tools` is persisted YAML. A symptom of
   there being no coherent "session setup" object.

4. **`SYSTEM_TEMPLATE` is a hardcoded var** in `src/agent/agent.go`. The
   `prompts` package only embeds `repo-map.md` and `memory.md`. No per-task
   prompt override is possible.

5. **`contextBudget` is a package global** (`MODEL_LIMIT * 1/2`) in
   `src/agent/context.go`. `context.md` already flags "Phase 2 replaces these
   globals with a per-model context window."

6. **Dead parameter.** `NewAgent(..., model string, contextEnabled bool)` never
   reads `contextEnabled`; callers always pass `false`.

7. **`AgentConfig.WallTimeLimit` has no CLI flag.** It exists in the struct but
   `main.go` only ever sets `StepLimit`, `CostLimit`, `WorkingDir`, `Memory`.

8. **The bench harness compensates with flag soup:**
   `stubbs -t - -y --auto-quit -s 24 -c 5`. This is the smell a profile removes.

---

## 2. Design: profiles

A **profile** is a named bundle of everything that varies by task.

```yaml
# .stubbs/profiles/python-bench.yaml
name: python-bench
extends: default                    # inherit + override
description: Exercism solving — no network, terse prompt, tight limits

system_prompt: file:prompts/python.md   # or inline block scalar

model: deepseek/deepseek-v4.1-flash

tools:
  bash:       { enabled: true, timeout: 120s, max_output: 8192 }
  read:  { enabled: true, max_lines: 400, read_secrets: false }
  write: { enabled: true }
  edit:  { enabled: true }
  list:  { enabled: true }
  webfetch:  { enabled: false }        # offline task
  memory:     { enabled: false }

mode: yolo
limits: { steps: 24, cost: 5.0, wall_time: 600 }
memory: { enabled: false }
env:    { kind: local, timeout: 300 }
approval:
  default: allow
  tools: { bash: confirm }
```

The benchmark then reduces to:

```bash
stubbs -p python-bench -t - --auto-quit
```

### Two levels of "tool options"

Deliberately separated, because they have very different implementation cost:

1. **enable/disable per tool** — cheap, high value, covers the majority of the
   use case.
2. **per-tool parameter overrides** (timeout, max output, allowed roots) —
   requires tools to declare their options so they can be validated.

---

## 3. Structural change: extract the wiring

The real refactor is pulling `main.go`'s inline setup into a builder, giving
profiles something to feed.

```
src/profile/
  profile.go     # Profile struct, defaults, validation
  load.go        # built-in + user file loading, `extends` merge
  resolve.go     # precedence merging
  builtin/*.yaml # go:embed'ed profiles
src/app/
  build.go       # Profile -> (*Registry, Environment, AgentConfig, TUI opts)
```

`main.go` shrinks to: parse flags → resolve profile → `app.Build(...)` → run.
The CLI becomes a **consumer** of profiles rather than a parallel config system.

### Precedence

Must be explicit and documented:

```
built-in defaults < profile < config.yaml < env vars < CLI flags
```

### Per-tool options

Extend the tool contract so options can be declared and validated:

```go
type Configurable interface {
    Configure(opts map[string]any) error
}

// or factory registration:
type ToolFactory struct {
    Name   string
    New    func(ctx ToolContext, opts map[string]any) (types.Tool, error)
    Schema func() json.RawMessage   // validates profile opts; powers --explain
}
```

`Schema()` earns its keep: it validates a profile at load time and can
auto-generate docs, instead of discovering a typo'd key mid-benchmark.

---

## 4. Phases

| Phase | Work | Why this order |
|---|---|---|
| 0 | Fix `Registry.List()` ordering; extract wiring from `main.go` into a builder (zero behavior change) | Prerequisite — profiles need an injection point |
| 1 | `Profile` struct + resolution + built-in `default` profile; `--profile`, `--explain-profile` | Proves the shape with no new features |
| 2 | Tool enable/disable + per-tool options + `Configurable`/`Schema` + validation | The actual ask |
| 3 | User profiles in `.stubbs/profiles/`, `extends` inheritance, `--profile-file` | Iterate without recompiling |
| 4 | Built-in task profiles (polyglot-python/go/js); wire bench harness to `-p` | Immediate payoff |
| 5 | Per-model context budget + profile-driven prompt files (kills the globals) | Follow-on, touches context/memory |

Phases 0 and 1 are worth doing together: phase 0 alone is invisible, phase 1 is
what makes the idea concrete enough to react to.

`--explain-profile` is the single highest-leverage feature: a resolved-config
dump makes every precedence question debuggable in one command.

---

## 5. Open questions

1. **In-process vs. out-of-process tools.** In-process (Go registry) is simple
   and type-safe but requires recompiling to add a tool. An out-of-process
   JSON-RPC-over-stdio protocol would allow tools in Python/shell — and the
   `Tool` interface is already nearly wire-ready (`Parameters() json.RawMessage`,
   `Execute → ExecutionOutput{Output,Error,Code}`). Start in-process and keep the
   seam, unless "extendible" means *third-party* tools, which changes phase 2
   substantially. **Which is intended?**

2. **How much does a profile own?** Should it set model and approval mode, or
   only tools and prompt? Owning model/mode is more useful for benchmarking but
   risks becoming a second config system that drifts from `config.yaml`.

3. **Are profiles versioned artifacts?** If benchmark results must be comparable
   across runs, the resolved profile should be recorded in the trajectory JSON.
   `writeRun` already stamps `trajectory_format` and `model`, so this is cheap
   now and painful to backfill.

4. **Disk profiles are a security surface.** A profile can set
   `approval.default: allow` and `read_secrets: true`. Should anything (a
   prompt, a flag) gate profiles that loosen policy?

---

## 6. Incidental cleanup (independent of profiles)

- Sort `Registry.List()` output by name.
- Remove the unused `contextEnabled` parameter from `NewAgent`.
- Reconcile `KnownEnvs` with the environments that actually exist, or implement
  the docker environment.
- Give `AgentConfig.WallTimeLimit` a CLI flag (currently struct-only).
- Unify `--whitelist` (CLI) with `approval.tools` (YAML) under one policy model.


1. Context mgmt [X]
2. Memory mgmt [X]
3. multi agent orchestration 
4. sandboxing [X]
5. configurable tools [X]
6. finance module
7. Implment plan mode
8. switch models [X]
9. sessiong history [X]
10. file or dir passing with @ [X]

'on pressing @ a small popup should show list of files searchable by user input, user should be able to pass specific file or directory using this