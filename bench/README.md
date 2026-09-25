# stubbs SWE-bench Verified benchmark

Runs the stubbs agent on a fixed set of SWE-bench Verified instances and grades
it with the official swebench harness. Each instance runs inside its official
docker image; stubbs is executed inside the container with `--auto-quit` so the
process exits on its own when the task is done, and the runner extracts the
resulting patch with `git diff`.

> For quick dev-loop regression checks use the polyglot benchmark instead:
> `bench/polyglot/README.md` (~15 min and ~$0.10 per run vs hours + multi-GB
> image builds). Keep this SWE-bench harness for periodic deep evals.

## One-time setup

```
# 1. stubbs binary (must support --auto-quit)
go build -o stubbs ./cmd/stubbs

# 2. python venv with the harness (bench/.venv)
cd bench && uv sync && cd ..
```

## Pick instances

Selects 15 instances (8 django + 7 sympy by default, single version per repo so
docker layers are shared), writes the full dataset rows to `bench/instances.jsonl`:

```
cd bench
uv run python select_instances.py --repos django,sympy --count 15 --seed 42
```

Re-running with the same seed produces the same set.

## Run the agent

```
cd bench
uv run python run_bench.py run --run-id r1
```

Per instance it:
1. builds `sweb.eval.x86_64.<instance_id>:latest` locally (cached; one env-image
   build per repo+version — this is the slow first-run step),
2. starts a container as root in `/testbed`, injects `STUBBS_API_KEY` from the
   host config, copies the stubbs binary in,
3. pipes the problem statement to `stubbs -t - -y --auto-quit -s 40 -c 2.0`,
4. extracts the patch (`git add -A && git diff --cached`, excluding `.stubbs/`),
5. saves the trajectory, per-instance log, and appends to `predictions.jsonl`.

Results land in `bench/results/<run-id>/`. The run is resumable — instances
with an existing trajectory are skipped on re-run.

Useful flags: `--step-limit`, `--cost-limit`, `--timeout`, `--workers`,
`--build-workers`, `--only <instance_id>...`, `--model`, `--no-build`.

## Grade

```
cd bench
uv run python run_bench.py grade --run-id r1
```

Calls `python -m swebench.harness.run_evaluation` (official harness) with the
predictions, reusing the already-built images. Prints resolved/unresolved/error
instance lists. Per-instance eval logs: `bench/results/<run-id>/grading/logs/`.

## Disk

Env image builds are heavy (~1–3GB per repo+version); grading runs with
`--cache_level env` so instance images are removed after grading. Check
`docker system df` and `docker image prune -f` when disk is tight (~46GB free
fits ~15 instances from two repo+version combos).

## Notes

- stubbs writes session logs to `.stubbs/` inside `/testbed` (its cwd); the
  patch extraction excludes it, so it never pollutes the patch.
- The agent's shell sources the image's `testbed` conda env before launching
  stubbs, so `python`/`pytest` point at the right environment.
- The harness reverts test files before applying the test patch, so agent
  edits to test files don't inflate results.
- Agent exit codes: 0 = finished, 2 = limits exceeded (step/cost), other = error.