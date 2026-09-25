# stubbs polyglot benchmark (Aider Polyglot, quick dev-loop)

Runs the stubbs agent on a seeded sample of [Aider's polyglot
benchmark](https://aider.chat/2024/12/21/polyglot.html) exercises — curated
hard Exercism exercises (`Aider-AI/polyglot-benchmark`) — and scores it by
executing each exercise's test suite. This is the fast regression signal while
stubbs is under heavy development; use the SWE-bench harness (`bench/`) for
deep evals.

- Languages: python, go, javascript (toolchains shipped in the docker image)
- Isolation: one exercise per docker container (stubbs executes arbitrary LLM code)
- Scoring: exercise pass = test suite exit 0 after the agent finishes; plus
  per-test-case pass counts (pytest / `go test -v` / jest output parsing)
- Integrity: the runner excludes exercism's `.meta/` (example solution) and
  `.approaches/` from workdirs, and **restores pristine test files before
  scoring** so agent edits to tests can't inflate results — same approach as
  aider's own harness
- Fidelity notes: js specs are un-gated the same way aider does it
  (`xtest(` → `test(`), shared jest install under `/npm-install/node_modules`
  with the exact pins from aider's benchmark image; go exercises whose stubs
  don't declare types are kept (agent must make them compile), matching aider

## One-time setup

```
# 1. stubbs binary (must support --auto-quit)
cd stubbs && go build -o stubbs ./cmd/stubbs && cd -

# 2. python venv (reuse bench/.venv via uv; run_polyglot.py is stdlib-only)
cd bench && uv sync && cd -

# 3. exercise repo
cd bench/polyglot
git clone --depth 1 https://github.com/Aider-AI/polyglot-benchmark

# 4. toolchain image (~1.6GB, python3.11+pytest / go1.21.5 / node20+jest29)
uv run python run_polyglot.py image
```

## Pick exercises

Seed-sample `--count` per language, then pre-flight each candidate: the test
suite must run and fail on the untouched stub (drops pre-solved/broken
exercises, logs in `logs/preflight/`). Writes `exercises.jsonl`:

```
cd bench/polyglot
uv run python run_polyglot.py select --languages python,go,javascript --count 10 --seed 42
```

Same seed ⇒ same set.

## Run

```
uv run python run_polyglot.py run --run-id p1
```

Per exercise it:
1. starts a fresh container from `stubbs-polyglot`, copies in the workdir
   (stub + tests + task prompt built from `.docs/instructions.md`), injects
   `STUBBS_API_KEY` from the host config and the stubbs binary,
2. pipes the task to `stubbs -t - -y --auto-quit -s 15 -c 0.5` inside a
   pseudo-TTY (stubbs is a bubbletea TUI; scoring runs without `-t`),
3. restores the original test files (anti-cheat), runs the test command
   (120s timeout) and parses per-test results,
4. saves the trajectory, per-exercise log, and appends to `results.jsonl`.

Results land in `bench/polyglot/results/polyglot-<run-id>/`. Re-runs skip
exercises with an existing trajectory + result; interrupted agent runs get
re-scored without re-running the agent.

Useful flags: `--step-limit` (default 15), `--cost-limit` ($0.5),
`--timeout` (300s agent), `--test-timeout` (120s), `--workers` (3),
`--only <lang/name>...`, `--model`, `--exercises`, `--seed`, `--count`.

## Report

```
uv run python run_polyglot.py report --run-id p1
```

Prints overall + per-language pass rate, test-case pass rate, cost and average
agent time, plus the failed list. Machine-readable copies: `summary.json`
(written by `run`) and `report.json` in `results/polyglot-<run-id>/`.

Reference points (aider leaderboard methodology, pass-rate-1 = first pass):
o1 (high) 23.7%, gpt-4o 16.4%, claude-3.5-sonnet 22.2% — but mind that those
are aider-the-editor on all 225 exercises; stubbs is an agentic shell loop, so
treat numbers as internal regression signal, not leaderboard comparison.

## Notes

- stubbs needs a TTY (bubbletea), so the agent runs under `docker exec -t`;
  the test phase deliberately does not, keeping output parseable.
- The first-run wizard is skipped when `STUBBS_API_KEY`/`OPENROUTER_API_KEY`
  is set (`config.IsConfigured` honors env vars).
- An exercise counts as passed only when the test command exits 0 AND at least
  one test was parsed — protects against empty/silent suites.
- Cost/step values come from the trajectory JSON, same as the SWE-bench runner.
- Full 225-exercise parity: `select --count <N>` with larger N or a bigger
  `--languages` set once more toolchains are added to the image (rust/cpp/java).