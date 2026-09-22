# stubbs vs mini-swe-agent: Gap Analysis

Based on a review of stubbs (`src/agent/agent.go`, `src/run/*`, etc.) against mini-swe-agent v2's core.

Beyond configurable environments and support for other models/providers, here's what mini has that stubbs doesn't:

## Interaction & control flow

1. **Human-in-the-loop agent** — mini's `InteractiveAgent` has confirm/human/yolo modes, per-action
   confirmation with regex whitelists, Ctrl-C interrupts (user types a comment instead of aborting),
   slash commands (`/y /c /u /m /h`), multiline prompts, confirm-on-exit, and prompting for new
   step/cost limits when exhausted. Stubbs has no interactive loop at all (task is hardcoded in
   `main.go:31`).
2. **Real CLI** — flags for model/task/cost-limit/output/yolo, task from stdin, and a first-run
   config wizard. Stubbs has no flag parsing; note `config.IsConfigured()`/`SaveConfig` exist but
   are never called.
3. **Submit signal & exit-status taxonomy** — mini ends runs via
   `echo COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` + a `Submitted` exception, and `run()` returns
   `{exit_status, submission}` (Submitted / LimitsExceeded / TimeExceeded / RepeatedFormatError /
   UserNewTask...). Stubbs "finishes" on the first no-tool-call reply and returns a bare string —
   no way to distinguish completion from running out of limits.
4. **Format-error feedback loop** — `max_consecutive_format_errors`, a requery template on malformed
   responses, and `finish_reason=length` handling; plus text-based (non-tool-calling) model variants
   parsing fenced bash blocks, so it works with any model. Stubbs only has the tool-calling path.

## Trajectory & UX

5. **Trajectory files** — full serialized run (`info`: cost, api_calls, config, version,
   exit_status, submission + messages), saved after *every* step, versioned `trajectory_format`,
   `-o` path. Stubbs' JSONL session log drops `tool_calls` and has no final artifact.
6. **Trajectory browser** (`mini -i` TUI inspector).
7. **Prompt templating** — Jinja `system_template`/`instance_template`/`observation_template`/
   `format_error_template` with strict-undefined and template vars (env info, `n_model_calls`,
   cost, elapsed). Stubbs has one hardcoded string (`agent.go:13`).
8. **Model-friendly environment polish** — `PAGER=cat`, `MANPAGER=cat`, `PIP_PROGRESS_BAR=off`,
   `TQDM_DISABLE=1`, and head/tail output elision with guidance instead of raw truncation.
9. **Batch/benchmark harness** — `mini-extra` (SWE-bench batch runs, GitHub-issue mode, progress
   UI). Stubbs is single-run only.
10. **Tests** — mini has pytest + codecov; stubbs has zero `_test.go` files.

## Bugs spotted while comparing

- `a.Steps` is **never incremented** (`agent.go:68` vs `query()` at :89) → the step limit is never
  enforced; with a free/cheap model the loop only stops on cost limit.
- `main.go:16` does `fmt.Println(cfg)`, which **prints the API key** to stdout.
- `truncateOutput`/`maxOutput` (`tools.go:95-125`) are dead code — `renderExecution` never
  truncates, so huge outputs go to the model unbounded (mini's observation template does the
  head/tail elision).
- `KnownEnvs` advertises `"docker"` but only `local` is implemented; cost check `<=` is off-by-one
  vs. mini's "stop after exceeding".

## Suggested priority order

1. Interactive loop + CLI
2. Submit/exit-status
3. Trajectory format
4. Truncation fixes
