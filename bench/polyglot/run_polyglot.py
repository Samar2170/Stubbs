#!/usr/bin/env python3
"""Benchmark stubbs on Aider's polyglot Exercism exercises.

Quick dev-loop benchmark: a seeded sample of exercises per language, each run
in the stubbs-polyglot docker image; stubbs implements the exercise, then the
runner executes the exercise's test suite and scores it. Exercises come from
https://github.com/Aider-AI/polyglot-benchmark (225 curated Exercism exercises
across C++, Go, Java, JavaScript, Python, Rust; this harness supports the
python/go/javascript subset whose toolchains it ships).

Subcommands:
    image   Build the stubbs-polyglot toolchain docker image
    select  Seed-sample exercises and pre-flight validate them -> exercises.jsonl
    run     Run stubbs on each exercise, then run + score its tests
    report  Print a leaderboard-style summary of a run

Usage:
    uv run python run_polyglot.py image
    uv run python run_polyglot.py select --count 10 --seed 42
    uv run python run_polyglot.py run   --run-id p1
    uv run python run_polyglot.py report --run-id p1
"""

import argparse
import json
import os
import random
import re
import shutil
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

POLYGLOT_DIR = Path(__file__).resolve().parent
REPO_ROOT = POLYGLOT_DIR.parent.parent
RESULTS_DIR = POLYGLOT_DIR / "results"
DEFAULT_EXERCISES_REPO = POLYGLOT_DIR / "polyglot-benchmark"
EXERCISES_FILE = POLYGLOT_DIR / "exercises.jsonl"
IMAGE = "stubbs-polyglot:latest"
WORKDIR = "/work"
NPM_MODULES = "/npm-install/node_modules"
STUBBS_HELP_PROBE = "--auto-quit"
STATUS_BY_EXIT = {0: "complete", 2: "limits"}
# never copied into a workdir: .meta holds exercism's example solution and
# .approaches holds solution walkthroughs — either would let the agent cheat.
EXCLUDED_DIRS = (".meta", ".approaches", "__pycache__", "node_modules")

TEST_TIMEOUT = 120


def log(msg: str) -> None:
    print(msg, flush=True)


def sh(args: list[str], *, input: bytes | None = None, timeout: float | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(args, input=input, timeout=timeout, capture_output=True)


def docker(*args: str, timeout: float | None = None) -> subprocess.CompletedProcess:
    return sh(["docker", *args], timeout=timeout)


def docker_ok(*args: str, timeout: float | None = None) -> subprocess.CompletedProcess:
    r = docker(*args, timeout=timeout)
    if r.returncode != 0:
        raise RuntimeError(f"docker {' '.join(args[:4])} failed:\n{r.stderr.decode(errors='replace')}")
    return r


def require_docker() -> None:
    r = sh(["docker", "info"])
    if r.returncode != 0:
        sys.exit("docker daemon not reachable")


def parse_env_file(path: Path) -> dict[str, str]:
    out: dict[str, str] = {}
    if not path.is_file():
        return out
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        out[k.strip()] = v.strip().strip('"').strip("'")
    return out


def load_credentials() -> tuple[str, str]:
    """Return (api_key, model) from env or the host .stubbs config."""
    cfg = parse_env_file(REPO_ROOT / ".stubbs" / "stubbs.env")
    api_key = os.environ.get("STUBBS_API_KEY") or os.environ.get("OPENROUTER_API_KEY") or cfg.get("API_KEY") or cfg.get("STUBBS_API_KEY") or ""
    model = os.environ.get("STUBBS_MODEL") or cfg.get("MODEL") or cfg.get("STUBBS_MODEL") or ""
    return api_key, model


def verify_stubbs(binary: Path) -> None:
    if not binary.is_file():
        sys.exit(f"stubbs binary not found at {binary} — build it: go build -o stubbs ./cmd/stubbs")
    r = sh([str(binary), "-h"])
    if r.returncode != 0 or STUBBS_HELP_PROBE.encode() not in r.stdout + r.stderr:
        sys.exit(f"{binary} does not support {STUBBS_HELP_PROBE} — rebuild it: go build -o stubbs ./cmd/stubbs")


@dataclass(frozen=True)
class Lang:
    test_cmd: str
    test_re: str  # per-test result lines; pass group first, fail group second


LANGS: dict[str, Lang] = {
    "python": Lang(
        test_cmd="python3 -m pytest -v --no-header -p no:cacheprovider",
        test_re=r"^(\S+::\S+) +(PASSED|FAILED|ERROR)",
    ),
    "go": Lang(
        test_cmd="go test -v ./...",
        test_re=r"^ *--- (PASS|FAIL): (\S+)",
    ),
    "javascript": Lang(
        test_cmd=f"PATH={NPM_MODULES}/.bin:$PATH NODE_PATH={NPM_MODULES} jest --verbose",
        test_re=r"^ *(✓|√) |^ *(✕|×|✗|✘) ",
    ),
}


def snake(name: str) -> str:
    return name.replace("-", "_")


def exercise_files(ex_dir: Path, lang: str) -> dict:
    """Test/solution file paths, from .meta/config.json when present (aider's
    harness does the same), else from per-language naming conventions."""
    readme = ".docs/instructions.md"
    if not (ex_dir / readme).is_file():
        readme = ".docs/README.md"
    if not (ex_dir / readme).is_file():
        raise FileNotFoundError(f"instructions missing in {ex_dir}")
    cfg_path = ex_dir / ".meta" / "config.json"
    if cfg_path.is_file():
        try:
            files = json.loads(cfg_path.read_text()).get("files", {})
            test_files, solution_files = files.get("test", []), files.get("solution", [])
            if test_files and solution_files:
                return {"stub": solution_files[0], "test_file": test_files[0],
                        "test_files": test_files, "solution_files": solution_files, "readme": readme}
        except (json.JSONDecodeError, KeyError):
            pass
    s = snake(ex_dir.name)
    if lang == "python":
        stub, test = f"{s}.py", f"{s}_test.py"
    elif lang == "go":
        stub, test = f"{s}.go", f"{s}_test.go"
    elif lang == "javascript":
        stub, test = f"{ex_dir.name}.js", f"{ex_dir.name}.spec.js"
    else:
        sys.exit(f"language {lang} has no file conventions")
    if not (ex_dir / stub).is_file():
        raise FileNotFoundError(f"stub {stub} missing in {ex_dir}")
    if not (ex_dir / test).is_file():
        raise FileNotFoundError(f"test file {test} missing in {ex_dir}")
    return {"stub": stub, "test_file": test, "test_files": [test], "solution_files": [stub], "readme": readme}


def copy_workdir(ex_dir: Path, dest: Path, lang: str) -> None:
    shutil.copytree(ex_dir, dest, ignore=shutil.ignore_patterns(*EXCLUDED_DIRS))
    if lang == "javascript":
        # exercism specs gate most tests behind xtest; activate them (same
        # trick as aider's npm-test.sh) and link the shared jest install
        for spec in dest.glob("*.spec.js"):
            spec.write_text(re.sub(r"\bxtest\(", "test(", spec.read_text()))
        os.symlink(NPM_MODULES, dest / "node_modules")


def fresh_workdir(ex: dict) -> Path:
    """Fresh exercise copy (minus .meta/.approaches) under a per-process tmp dir."""
    tmp = POLYGLOT_DIR / "logs" / f"run-{os.getpid()}" / ex["lang"] / ex["name"]
    shutil.rmtree(tmp, ignore_errors=True)
    copy_workdir(POLYGLOT_DIR / ex["dir"], tmp, ex["lang"])
    return tmp


def build_task(ex: dict) -> str:
    d = POLYGLOT_DIR / ex["dir"]
    files = exercise_files(d, ex["lang"])
    readme = (d / files["readme"]).read_text()
    extra_path = d / ".docs" / "instructions.append.md"
    extra = extra_path.read_text().strip() + "\n\n" if extra_path.exists() else ""
    stubs = "".join(
        f"\nHere is the stub file you must implement ({s}):\n\n```\n{(d / s).read_text()}```\n"
        for s in files["solution_files"]
    )
    cmd = LANGS[ex["lang"]].test_cmd
    return (
        f"Solve the following coding exercise. Work inside the current directory.\n\n"
        f"{readme.strip()}\n\n{extra}{stubs}\n"
        f"Implement the stub so that all tests in {files['test_file']} pass. "
        f"You can run the tests with:\n\n  $ {cmd}\n\n"
        f"Do not modify {files['test_file']} or the .docs directory. "
        f"When the tests pass, you are done.\n"
    )


def parse_test_output(out: str, lang: str) -> tuple[int, int]:
    """Return (passed, failed) test counts parsed from test output."""
    passed = failed = 0
    for m in re.finditer(LANGS[lang].test_re, out, re.MULTILINE):
        if lang == "go":
            passed, failed = (passed + 1, failed) if m.group(1) == "PASS" else (passed, failed + 1)
        elif lang == "python":
            passed, failed = (passed + 1, failed) if m.group(2) == "PASSED" else (passed, failed + 1)
        else:
            passed, failed = (passed + 1, failed) if m.group(1) else (passed, failed + 1)
    if passed + failed == 0:
        # fall back to summary lines ("N passed", "N failed")
        p = re.search(r"(\d+) passed", out)
        f = re.search(r"(\d+) failed", out)
        passed = int(p.group(1)) if p else 0
        failed = int(f.group(1)) if f else 0
    return passed, failed


def image_exists() -> bool:
    return docker("image", "inspect", IMAGE).returncode == 0


def cmd_image(args) -> None:
    if image_exists() and not args.force:
        log(f"{IMAGE} already exists (use --force to rebuild)")
        return
    require_docker()
    log(f"Building {IMAGE} (a few minutes the first time); logs: logs/image-build.log")
    (POLYGLOT_DIR / "logs").mkdir(exist_ok=True)
    with open(POLYGLOT_DIR / "logs" / "image-build.log", "a") as f:
        f.write(f"\n===== {time.strftime('%F %T')} =====\n")
        f.flush()
        r = subprocess.run(["docker", "build", "-t", IMAGE, str(POLYGLOT_DIR)], stdout=f, stderr=subprocess.STDOUT)
    if r.returncode != 0:
        sys.exit("image build failed — see logs/image-build.log")
    log(f"built {IMAGE}")


def enumerate_exercises(exercises_dir: Path, lang: str) -> list[Path]:
    base = exercises_dir / lang / "exercises" / "practice"
    if not base.is_dir():
        sys.exit(f"{base} missing — clone: git clone --depth 1 https://github.com/Aider-AI/polyglot-benchmark")
    return sorted(p for p in base.iterdir() if p.is_dir() and not p.name.startswith("."))


def make_exercise(p: Path, lang: str) -> dict:
    try:
        files = exercise_files(p, lang)
    except FileNotFoundError as e:
        return {"id": f"{lang}/{p.name}", "lang": lang, "name": p.name, "error": str(e)}
    return {
        "id": f"{lang}/{p.name}",
        "lang": lang,
        "name": p.name,
        "dir": str(p.relative_to(POLYGLOT_DIR)),
        **files,
    }


def preflight(ex: dict, pf_dir: Path) -> tuple[bool, str]:
    """Run the test suite on the untouched stub. Keep exercises whose tests
    compile/run and fail; drop pre-solved or broken ones."""
    cname = f"stubbs-polyglot-pf-{re.sub(r'[^A-Za-z0-9_.-]', '-', ex['id'])}"[:250]
    log_file = POLYGLOT_DIR / "logs" / "preflight" / f"{ex['id'].replace('/', '__')}.log"
    log_file.parent.mkdir(parents=True, exist_ok=True)
    try:
        docker("rm", "-f", cname)
        docker_ok("run", "-d", "--name", cname, "-w", WORKDIR, IMAGE, "tail", "-f", "/dev/null")
        copy_workdir(POLYGLOT_DIR / ex["dir"], pf_dir / ex["lang"] / ex["name"], ex["lang"])
        docker_ok("cp", str(pf_dir / ex["lang"] / ex["name"]), f"{cname}:{WORKDIR}/{ex['name']}")
        r = docker("exec", "-w", f"{WORKDIR}/{ex['name']}", cname, "bash", "-c",
                   LANGS[ex["lang"]].test_cmd, timeout=TEST_TIMEOUT + 60)
        out = (r.stdout + r.stderr).decode(errors="replace")
        log_file.write_text(out)
        passed, failed = parse_test_output(out, ex["lang"])
        if r.returncode == 0:
            return False, f"passes on stub ({passed}/{passed + failed})"
        if passed + failed == 0:
            if ex["lang"] == "go" and "undefined:" in out:
                # exercism go stubs may omit type declarations; solvable, keep
                return True, "stub does not compile (missing symbols)"
            return False, "tests did not run"
        return True, f"tests fail on stub ({passed}/{passed + failed} pass)"
    except subprocess.TimeoutExpired:
        return False, "test timeout"
    except Exception as e:
        return False, f"error: {e}"
    finally:
        docker("rm", "-f", cname)


def cmd_select(args) -> None:
    langs = [l.strip() for l in args.languages.split(",") if l.strip()]
    unknown = [l for l in langs if l not in LANGS]
    if unknown:
        sys.exit(f"unknown languages {unknown} — supported: {', '.join(LANGS)}")
    require_docker()
    if not image_exists():
        sys.exit(f"{IMAGE} missing — run: uv run python run_polyglot.py image")
    repo = args.exercises_dir
    if not repo.is_dir():
        sys.exit(f"{repo} missing — clone: git clone --depth 1 https://github.com/Aider-AI/polyglot-benchmark")

    picked: list[dict] = []
    for lang in langs:
        names = [p.name for p in enumerate_exercises(repo, lang)]
        rng = random.Random(f"{args.seed}:{lang}")
        chosen = rng.sample(names, min(args.count, len(names)))
        picked += [make_exercise(repo / lang / "exercises" / "practice" / n, lang) for n in chosen]

    for e in [e for e in picked if "dir" not in e]:
        log(f"  skip {e['id']}: {e['error']}")
    picked = [e for e in picked if "dir" in e]

    pf_dir = POLYGLOT_DIR / "logs" / "preflight-workdirs"
    shutil.rmtree(pf_dir, ignore_errors=True)
    keep, dropped = [], []
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        futs = {pool.submit(preflight, e, pf_dir): e for e in picked}
        for fut in as_completed(futs):
            e, (ok, reason) = futs[fut], fut.result()
            (keep if ok else dropped).append(e if ok else (e, reason))
            log(f"  {'kept  ' if ok else 'dropped'} {e['id']:38} {reason}")

    keep.sort(key=lambda e: e["id"])
    with open(EXERCISES_FILE, "w") as f:
        for e in keep:
            f.write(json.dumps(e) + "\n")
    by_lang: dict[str, int] = {}
    for e in keep:
        by_lang[e["lang"]] = by_lang.get(e["lang"], 0) + 1
    log(f"\nwrote {len(keep)} exercises to {EXERCISES_FILE} ({by_lang})")
    if dropped:
        log(f"dropped {len(dropped)}: " + ", ".join(f"{e['id']} ({r})" for e, r in dropped))


class Results:
    """exercise id -> result record, rewritten atomically after each result."""

    def __init__(self, path: Path):
        self.path = path
        self.lock = threading.Lock()
        self.data: dict[str, dict] = {}
        if path.is_file():
            for line in path.read_text().splitlines():
                if line.strip():
                    r = json.loads(line)
                    self.data[r["id"]] = r

    def put(self, rec: dict) -> None:
        with self.lock:
            self.data[rec["id"]] = rec
            tmp = self.path.with_suffix(".tmp")
            with open(tmp, "w") as f:
                for r in self.data.values():
                    f.write(json.dumps(r) + "\n")
            tmp.replace(self.path)


def read_old_record(run_dir: Path, iid: str) -> dict | None:
    p = run_dir / "results.jsonl"
    if not p.is_file():
        return None
    for line in p.read_text().splitlines():
        if line.strip() and (r := json.loads(line)).get("id") == iid:
            return r
    return None


def run_exercise(ex: dict, *, args, api_key: str, model: str, run_dir: Path) -> dict:
    iid = ex["id"]
    cname = f"stubbs-polyglot-{re.sub(r'[^A-Za-z0-9_.-]', '-', args.run_id)}-{re.sub(r'[^A-Za-z0-9_.-]', '-', iid)}"[:250]
    lang_dir = f"{WORKDIR}/{ex['name']}"
    traj_path = run_dir / "trajectories" / f"{ex['lang']}__{ex['name']}.json"
    log_path = run_dir / "logs" / f"{ex['lang']}__{ex['name']}.log"

    def logf(msg: str) -> None:
        with open(log_path, "a") as f:
            f.write(f"{msg}\n")

    def score_tests() -> None:
        t0 = time.time()
        try:
            # restore pristine test files so agent edits can't inflate results
            src_dir = POLYGLOT_DIR / ex["dir"]
            for tf in ex["test_files"]:
                docker_ok("cp", str(src_dir / tf), f"{cname}:{lang_dir}/{tf}")
            if ex["lang"] == "javascript":
                docker_ok("exec", "-w", lang_dir, cname, "bash", "-c", "sed -i 's/\\bxtest(/test(/g' *.spec.js")
            r = docker("exec", "-w", lang_dir, cname, "bash", "-c",
                       LANGS[ex["lang"]].test_cmd, timeout=args.test_timeout + 60)
            out = (r.stdout + r.stderr).decode(errors="replace")
            passed, failed = parse_test_output(out, ex["lang"])
            rec["test_seconds"] = round(time.time() - t0, 1)
            rec["tests_passed"], rec["tests_failed"] = passed, failed
            rec["tests_total"] = passed + failed
            if r.returncode == 0 and rec["tests_total"] > 0:
                rec["test_status"], rec["passed"] = "ok", True
            elif rec["tests_total"] == 0:
                rec["test_status"], rec["passed"] = "no_tests", False
            else:
                rec["test_status"], rec["passed"] = "fail", False
            rec["test_output_tail"] = "\n".join(out.strip().splitlines()[-12:])
        except subprocess.TimeoutExpired:
            rec["test_status"], rec["passed"] = "timeout", False
            rec["test_seconds"] = round(time.time() - t0, 1)
        except Exception as e:
            rec["test_status"] = f"error: {e}"

    rec = {
        "id": ex["id"], "lang": ex["lang"], "name": ex["name"],
        "agent_status": "error", "test_status": "error",
        "passed": None, "tests_total": None, "tests_passed": None, "tests_failed": None,
        "steps": None, "cost": None, "agent_seconds": None, "test_seconds": None,
    }

    try:
        traj_exists = traj_path.is_file()
        old = read_old_record(run_dir, ex["id"])
        if traj_exists and old is not None:
            logf("trajectory + result exist; skipping")
            return old

        docker("rm", "-f", cname)
        docker_ok("run", "-d", "--name", cname, "-w", WORKDIR,
                  "-e", f"STUBBS_API_KEY={api_key}",
                  "-e", "PAGER=cat", "-e", "MANPAGER=cat", "-e", "GIT_PAGER=cat",
                  "-e", "TQDM_DISABLE=1", "-e", "PIP_PROGRESS_BAR=off",
                  IMAGE, "tail", "-f", "/dev/null")
        docker_ok("cp", str(fresh_workdir(ex)), f"{cname}:{lang_dir}")

        if traj_exists:
            # agent finished in an earlier interrupted run; re-score tests only
            logf("trajectory exists; skipping agent, re-scoring tests")
            rec["agent_status"] = "resumed"
            if old:
                rec["steps"] = old.get("steps")
                rec["cost"] = old.get("cost")
                rec["agent_seconds"] = old.get("agent_seconds")
        else:
            docker_ok("cp", str(args.stubbs), f"{cname}:/usr/local/bin/stubbs")
            docker_ok("exec", cname, "chmod", "+x", "/usr/local/bin/stubbs")

            task_file = run_dir / "tasks" / f"{ex['lang']}__{ex['name']}.txt"
            task_file.parent.mkdir(parents=True, exist_ok=True)
            task_file.write_text(build_task(ex))
            docker_ok("cp", str(task_file), f"{cname}:/tmp/task.txt")

            agent_cmd = (
                f"exec stubbs -t - -y --auto-quit -s {args.step_limit} -c {args.cost_limit} "
                f"-m {model} -o /tmp/traj.json < /tmp/task.txt"
            )
            t0 = time.time()
            try:
                # -t: stubbs is a bubbletea TUI and needs a pty; the task is
                # still read from stdin redirect, scoring runs without -t
                r = docker("exec", "-t", "-w", lang_dir, cname, "bash", "-c", agent_cmd, timeout=args.timeout)
                logf(f"stubbs exit={r.returncode}")
                rec["agent_status"] = STATUS_BY_EXIT.get(r.returncode, "error")
                if r.stdout:
                    logf("--- stdout ---\n" + r.stdout.decode(errors="replace"))
                if r.stderr:
                    logf("--- stderr ---\n" + r.stderr.decode(errors="replace"))
            except subprocess.TimeoutExpired:
                rec["agent_status"] = "timeout"
                logf(f"agent timeout after {args.timeout}s")
            rec["agent_seconds"] = round(time.time() - t0, 1)

            try:
                docker("cp", f"{cname}:/tmp/traj.json", str(traj_path))
                data = json.loads(traj_path.read_text())
                rec["steps"], rec["cost"] = data.get("steps"), data.get("cost")
            except Exception as e:
                logf(f"trajectory extraction failed: {e}")

        score_tests()
        return rec
    finally:
        docker("rm", "-f", cname)


def cmd_run(args) -> None:
    require_docker()
    verify_stubbs(args.stubbs)
    if not image_exists():
        sys.exit(f"{IMAGE} missing — run: uv run python run_polyglot.py image")
    api_key, cfg_model = load_credentials()
    if not api_key:
        sys.exit("no API key: set STUBBS_API_KEY or configure .stubbs/stubbs.env")
    model = args.model or cfg_model
    if not model:
        sys.exit("no model: pass --model or set STUBBS_MODEL / .stubbs/stubbs.env")

    if not args.exercises.is_file():
        sys.exit(f"{args.exercises} missing — run: uv run python run_polyglot.py select")
    exercises = [json.loads(l) for l in args.exercises.read_text().splitlines() if l.strip()]
    if args.only:
        want = set(args.only)
        exercises = [e for e in exercises if e["id"] in want]
    if not exercises:
        sys.exit("no exercises selected")

    run_dir = RESULTS_DIR / f"polyglot-{args.run_id}"
    for sub in ("trajectories", "logs", "tasks"):
        (run_dir / sub).mkdir(parents=True, exist_ok=True)

    results = Results(run_dir / "results.jsonl")
    print(f"Running stubbs ({args.model_name}) on {len(exercises)} exercises, workers={args.workers}")
    print(f"limits: {args.step_limit} steps / ${args.cost_limit} / {args.timeout}s agent, {args.test_timeout}s tests\n")

    try:
        with ThreadPoolExecutor(max_workers=args.workers) as pool:
            futs = {
                pool.submit(run_exercise, ex, args=args, api_key=api_key, model=model, run_dir=run_dir): ex
                for ex in exercises
            }
            for fut in as_completed(futs):
                ex = futs[fut]
                try:
                    r = fut.result()
                except Exception as e:
                    r = {"id": ex["id"], "lang": ex["lang"], "name": ex["name"],
                         "agent_status": "error", "test_status": "error", "passed": None,
                         "tests_total": None, "tests_passed": None, "tests_failed": None,
                         "steps": None, "cost": None, "agent_seconds": None, "test_seconds": None}
                    with open(run_dir / "logs" / f"{ex['lang']}__{ex['name']}.log", "a") as f:
                        f.write(f"ERROR: {e}\n")
                results.put(r)
                mark = {True: "PASS", False: "FAIL", None: "????"}[r.get("passed")]
                tests = f"{r['tests_passed']}/{r['tests_total']}" if r["tests_total"] is not None else "-"
                cost = f"${r['cost']:.4f}" if r["cost"] is not None else "-"
                secs = f"{r['agent_seconds']}s" if r["agent_seconds"] is not None else "-"
                print(f"  {r['id']:38} {mark} {r['agent_status']:8} tests={tests:>7} steps={r['steps']} cost={cost} ({secs})")
    finally:
        shutil.rmtree(POLYGLOT_DIR / "logs" / f"run-{os.getpid()}", ignore_errors=True)

    recs = sorted(results.data.values(), key=lambda r: r["id"])
    (run_dir / "summary.json").write_text(json.dumps({
        "run_id": args.run_id,
        "benchmark": "aider-polyglot",
        "model": args.model_name,
        "model_override": model,
        "step_limit": args.step_limit,
        "cost_limit": args.cost_limit,
        "results": recs,
    }, indent=2) + "\n")

    print("\n" + report_text(recs))
    print(f"Next: uv run python run_polyglot.py report --run-id {args.run_id}")


def report_text(recs: list[dict]) -> str:
    done = [r for r in recs if r.get("passed") is not None]
    n = len(done)
    passed = sum(1 for r in done if r["passed"])
    tp = sum(r["tests_passed"] or 0 for r in done)
    tt = sum(r["tests_total"] or 0 for r in done)
    total_cost = sum(r["cost"] or 0 for r in recs)
    secs = sum(r["agent_seconds"] or 0 for r in recs)
    lines = ["=" * 64]
    lines.append(
        f"Passed: {passed}/{n}" + (f" ({100 * passed / n:.1f}%)" if n else "")
        + f"   test cases: {tp}/{tt}" + (f" ({100 * tp / tt:.1f}%)" if tt else "")
        + f"   cost ${total_cost:.4f}"
    )
    lines.append(f"avg agent time {secs / max(1, len(recs)):.0f}s/exercise")
    for lang in sorted({r["lang"] for r in done}):
        ld = [r for r in done if r["lang"] == lang]
        lp = sum(1 for r in ld if r["passed"])
        ltp = sum(r["tests_passed"] or 0 for r in ld)
        ltt = sum(r["tests_total"] or 0 for r in ld)
        lines.append(f"  {lang:10} {lp}/{len(ld)} passed   cases {ltp}/{ltt}")
    fails = [r["id"] for r in done if not r["passed"]]
    if fails:
        lines.append(f"failed: {', '.join(fails)}")
    return "\n".join(lines)


def cmd_report(args) -> None:
    run_dir = RESULTS_DIR / f"polyglot-{args.run_id}"
    path = run_dir / "results.jsonl"
    if not path.is_file():
        sys.exit(f"{path} missing — run: uv run python run_polyglot.py run --run-id {args.run_id}")
    recs = sorted((json.loads(l) for l in path.read_text().splitlines() if l.strip()), key=lambda r: r["id"])
    print(report_text(recs))
    (run_dir / "report.json").write_text(json.dumps(recs, indent=2) + "\n")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    im = sub.add_parser("image", help="build the stubbs-polyglot toolchain image")
    im.add_argument("--force", action="store_true", help="rebuild even if the image exists")
    im.set_defaults(func=cmd_image)

    s = sub.add_parser("select", help="seed-sample + pre-flight validate exercises")
    s.add_argument("--languages", default="python,go,javascript", help="subset of: " + ", ".join(LANGS))
    s.add_argument("--count", type=int, default=10, help="exercises per language")
    s.add_argument("--seed", type=int, default=42)
    s.add_argument("--exercises-dir", type=Path, default=DEFAULT_EXERCISES_REPO)
    s.add_argument("--workers", type=int, default=4, help="parallel pre-flight checks")
    s.set_defaults(func=cmd_select)

    r = sub.add_parser("run", help="run stubbs on the selected exercises")
    r.add_argument("--run-id", required=True)
    r.add_argument("--exercises", type=Path, default=EXERCISES_FILE)
    r.add_argument("--only", nargs="*", help="restrict to these exercise ids (lang/name)")
    r.add_argument("--stubbs", type=Path, default=REPO_ROOT / "stubbs", help="stubbs binary path")
    r.add_argument("--model", default="", help="model override (default: host .stubbs config)")
    r.add_argument("--model-name", default="stubbs", help="model name recorded in results")
    r.add_argument("--step-limit", type=int, default=15)
    r.add_argument("--cost-limit", type=float, default=0.5)
    r.add_argument("--timeout", type=int, default=300, help="per-exercise agent timeout (s)")
    r.add_argument("--test-timeout", type=int, default=120, help="per-exercise test timeout (s)")
    r.add_argument("--workers", type=int, default=3, help="parallel agent runs")
    r.set_defaults(func=cmd_run)

    p = sub.add_parser("report", help="summarize a run")
    p.add_argument("--run-id", required=True)
    p.set_defaults(func=cmd_report)

    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()