#!/usr/bin/env python3
"""Benchmark stubbs on SWE-bench Verified instances.

Subcommands:
    run    Run the stubbs agent inside official SWE-bench instance containers
           and collect a patch per instance (predictions.jsonl + trajectories).
    grade  Grade the collected predictions with the official swebench harness.

Images are built locally via the swebench 2.1.8 harness API
(sweb.eval.x86_64.<instance_id>:latest; one env image per repo+version, cheap
instance layers on top). No registry dependency.

Usage:
    uv run python run_bench.py run   --run-id r1 [--instances-file ...]
    uv run python run_bench.py grade --run-id r1
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

BENCH_DIR = Path(__file__).resolve().parent
REPO_ROOT = BENCH_DIR.parent
RESULTS_DIR = BENCH_DIR / "results"

IMAGE_KEY = "sweb.eval.x86_64.{instance_id}:latest"  # swebench 2.1.8 naming
WORKDIR = "/testbed"
# `docker exec ... bash -c` is non-interactive, so the image's .bashrc conda
# activation does not apply; source it explicitly so stubbs (and its bash-tool
# children) get the testbed python on PATH.
ACTIVATE_TESTBED = "source /opt/miniconda3/etc/profile.d/conda.sh && conda activate testbed"
PATCH_CMD = "git add -A && git diff --cached -- . ':(exclude).stubbs'"
STUBBS_HELP_PROBE = "--auto-quit"

STATUS_BY_EXIT = {0: "complete", 2: "limits"}


def log(msg: str) -> None:
    print(msg, flush=True)


def sh(args: list[str], *, input: bytes | None = None, timeout: float | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        args, input=input, timeout=timeout, capture_output=True
    )


def docker(*args: str, timeout: float | None = None) -> subprocess.CompletedProcess:
    return sh(["docker", *args], timeout=timeout)


def docker_out(*args: str, timeout: float | None = None) -> str:
    r = docker(*args, timeout=timeout)
    if r.returncode != 0:
        raise RuntimeError(f"docker {' '.join(args[:4])} failed:\n{r.stderr.decode(errors='replace')}")
    return r.stdout.decode(errors="replace")


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
        v = v.strip().strip('"').strip("'")
        out[k.strip()] = v
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
    # pflag prints usage to stderr; check both.
    if r.returncode != 0 or STUBBS_HELP_PROBE.encode() not in r.stdout + r.stderr:
        sys.exit(f"{binary} does not support {STUBBS_HELP_PROBE} — rebuild it: go build -o stubbs ./cmd/stubbs")


def build_images(instances: list[dict], max_workers: int) -> tuple[set[str], list[str]]:
    """Build instance images via the swebench API; returns (ok_ids, skipped_ids)."""
    import docker
    from swebench.harness.docker_build import build_instance_images

    client = docker.from_env()
    ok, _failed = build_instance_images(
        client=client, dataset=instances, force_rebuild=False, max_workers=max_workers
    )
    # Instances whose env image build failed never get an instance image at all,
    # so anything missing from `ok` is skipped.
    ok_ids = {spec.instance_id for spec in ok}
    return ok_ids, [i["instance_id"] for i in instances if i["instance_id"] not in ok_ids]


class Predictions:
    """instance_id -> prediction, rewritten atomically after each result."""

    def __init__(self, path: Path):
        self.path = path
        self.lock = threading.Lock()
        self.data: dict[str, dict] = {}
        if path.is_file():
            for line in path.read_text().splitlines():
                if line.strip():
                    p = json.loads(line)
                    self.data[p["instance_id"]] = p

    def put(self, instance_id: str, patch: str, model_name: str) -> None:
        with self.lock:
            self.data[instance_id] = {
                "instance_id": instance_id,
                "model_patch": patch,
                "model_name_or_path": model_name,
            }
            tmp = self.path.with_suffix(".tmp")
            with open(tmp, "w") as f:
                for p in self.data.values():
                    f.write(json.dumps(p) + "\n")
            tmp.replace(self.path)


def run_instance(
    inst: dict,
    *,
    args,
    api_key: str,
    model: str,
    traj_dir: Path,
    log_dir: Path,
) -> dict:
    iid = inst["instance_id"]
    image = IMAGE_KEY.format(instance_id=iid)
    name = f"stubbs-{re.sub(r'[^A-Za-z0-9_.-]', '-', args.run_id)}-{iid}"[:250]
    log_path = log_dir / f"{iid}.log"
    traj_path = traj_dir / f"{iid}.json"
    started = time.time()

    def rec(msg: str) -> None:
        with open(log_path, "a") as f:
            f.write(f"{msg}\n")

    try:
        if traj_path.is_file():
            rec("trajectory exists, skipping agent run")
            return summarize(iid, inst, "resumed", None, traj_path, started)

        docker("rm", "-f", name)
        docker_ok(
            "run", "-d", "--name", name,
            "-w", WORKDIR,
            "-e", f"STUBBS_API_KEY={api_key}",
            "-e", "PAGER=cat", "-e", "MANPAGER=cat", "-e", "GIT_PAGER=cat",
            "-e", "TQDM_DISABLE=1", "-e", "PIP_PROGRESS_BAR=off",
            image, "tail", "-f", "/dev/null",
        )
        rec(f"container {name} started from {image}")

        task_file = traj_dir.parent / "tasks" / f"{iid}.txt"
        task_file.parent.mkdir(parents=True, exist_ok=True)
        task_file.write_text(inst["problem_statement"])
        docker_ok("cp", str(task_file), f"{name}:/tmp/sweb_task.txt")
        docker_ok("cp", str(args.stubbs), f"{name}:/usr/local/bin/stubbs")
        docker_ok("exec", name, "chmod", "+x", "/usr/local/bin/stubbs")

        traj_in_container = "/tmp/traj.json"
        cmd = (
            f"source /opt/miniconda3/etc/profile.d/conda.sh && conda activate testbed && "
            f"exec stubbs -t - -y --auto-quit -s {args.step_limit} -c {args.cost_limit} "
            f"-m {model} -o {traj_in_container} < /tmp/sweb_task.txt"
        )
        status = "error"
        try:
            r = docker("exec", name, "bash", "-c", cmd, timeout=args.timeout)
            rec(f"stubbs exit={r.returncode}")
            status = STATUS_BY_EXIT.get(r.returncode, "error")
            if r.stdout:
                rec("--- stdout ---\n" + r.stdout.decode(errors="replace"))
            if r.stderr:
                rec("--- stderr ---\n" + r.stderr.decode(errors="replace"))
        except subprocess.TimeoutExpired:
            status = "timeout"
            rec(f"timeout after {args.timeout}s")

        # Patch whatever state the agent left behind (even after a timeout).
        patch = ""
        try:
            pr = docker("exec", "-w", WORKDIR, name, "bash", "-c", PATCH_CMD)
            patch = pr.stdout.decode(errors="replace") if pr.returncode == 0 else ""
            rec(f"patch bytes: {len(patch)}")
        except Exception as e:
            rec(f"patch extraction failed: {e}")

        docker("cp", f"{name}:{traj_in_container}", str(traj_path))
        return summarize(iid, inst, status, patch, traj_path, started)
    finally:
        docker("rm", "-f", name)


def summarize(iid: str, inst: dict | None, status: str, patch: str | None, traj_path: Path, started: float) -> dict:
    steps = cost = None
    if traj_path.is_file():
        try:
            data = json.loads(traj_path.read_text())
            steps, cost = data.get("steps"), data.get("cost")
        except Exception:
            pass
    return {
        "instance_id": iid,
        "repo": (inst or {}).get("repo", ""),
        "version": (inst or {}).get("version", ""),
        "status": status,
        "steps": steps,
        "cost": cost,
        "patch_bytes": len(patch) if patch is not None else None,
        "patch": patch,
        "seconds": round(time.time() - started, 1),
    }


def cmd_run(args) -> None:
    require_docker()
    verify_stubbs(args.stubbs)
    api_key, cfg_model = load_credentials()
    if not api_key:
        sys.exit("no API key: set STUBBS_API_KEY or configure .stubbs/stubbs.env")
    model = args.model or cfg_model
    if not model:
        sys.exit("no model: pass --model or set STUBBS_MODEL / .stubbs/stubbs.env")

    instances_path = args.instances or (BENCH_DIR / "instances.jsonl")
    if not instances_path.is_file():
        sys.exit(f"{instances_path} missing — run select_instances.py first")
    instances = [json.loads(l) for l in instances_path.read_text().splitlines() if l.strip()]
    if args.only:
        want = set(args.only)
        instances = [i for i in instances if i["instance_id"] in want]
    if not instances:
        sys.exit("no instances selected")

    run_dir = RESULTS_DIR / args.run_id
    (run_dir / "trajectories").mkdir(parents=True, exist_ok=True)
    (run_dir / "logs").mkdir(parents=True, exist_ok=True)
    (run_dir / "tasks").mkdir(parents=True, exist_ok=True)

    free = shutil.disk_usage("/").free / 2**30
    if free < 25:
        print(f"WARNING: only {free:.0f}GB free on / — image builds may fill the disk")

    preds = Predictions(run_dir / "predictions.jsonl")

    if not args.no_build:
        print(f"Building images for {len(instances)} instances (env images cached per repo+version)...")
        ok_ids, skipped = build_images(instances, args.build_workers)
        if skipped:
            print(f"WARNING: {len(skipped)} instance image builds failed, skipping: {sorted(skipped)}")
        instances = [i for i in instances if i["instance_id"] in ok_ids]
    if not instances:
        sys.exit("no instance images available")

    print(f"Running stubbs ({args.model_name}) on {len(instances)} instances, workers={args.workers}")
    print(f"limits: {args.step_limit} steps / ${args.cost_limit} / {args.timeout}s timeout\n")

    results = []
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {
            ex.submit(
                run_instance, inst,
                args=args, api_key=api_key, model=model,
                traj_dir=run_dir / "trajectories", log_dir=run_dir / "logs",
            ): inst
            for inst in instances
        }
        for fut in as_completed(futs):
            inst = futs[fut]
            try:
                r = fut.result()
            except Exception as e:
                r = {
                    "instance_id": inst["instance_id"],
                    "repo": inst.get("repo", ""), "version": inst.get("version", ""),
                    "status": "error", "steps": None, "cost": None,
                    "patch_bytes": None, "patch": None, "seconds": None,
                }
                (run_dir / "logs").mkdir(parents=True, exist_ok=True)
                with open(run_dir / "logs" / f"{r['instance_id']}.log", "a") as f:
                    f.write(f"ERROR: {e}\n")
            results.append(r)
            if r.get("patch") is not None:
                preds.put(r["instance_id"], r["patch"], args.model_name)
            elif r["status"] == "resumed" and r["instance_id"] not in preds.data:
                print(f"  warning: {r['instance_id']} resumed without a stored patch — delete its trajectory to re-run it")
            cost = f"${r['cost']:.4f}" if r["cost"] is not None else "-"
            secs = f"{r['seconds']}s" if r["seconds"] is not None else "-"
            print(f"  {r['instance_id']:42} {r['status']:8} steps={r['steps']} cost={cost} patch={r['patch_bytes']}B ({secs})")

    results.sort(key=lambda r: r["instance_id"])
    (run_dir / "summary.json").write_text(
        json.dumps(
            {
                "run_id": args.run_id,
                "model": args.model_name,
                "dataset": args.dataset,
                "step_limit": args.step_limit,
                "cost_limit": args.cost_limit,
                "results": [{k: v for k, v in r.items() if k != "patch"} for r in results],
            },
            indent=2,
        )
        + "\n"
    )

    print("\nSummary:")
    print(f"{'instance_id':42} {'status':8} {'steps':>5} {'cost':>8} {'patch':>9}")
    total_cost = 0.0
    for r in sorted(results, key=lambda r: r["instance_id"]):
        cost = r["cost"] or 0.0
        total_cost += cost
        print(f"{r['instance_id']:42} {r['status']:8} {str(r['steps']):>5} ${cost:>7.4f} {str(r['patch_bytes'])}B")
    print(f"\n{len(results)} instances, total cost ${total_cost:.4f}")
    print(f"Next: uv run python run_bench.py grade --run-id {args.run_id}")


def cmd_grade(args) -> None:
    require_docker()
    run_dir = RESULTS_DIR / args.run_id
    preds_path = run_dir / "predictions.jsonl"
    if not preds_path.is_file():
        sys.exit(f"{preds_path} missing — run `run_bench.py run --run-id {args.run_id}` first")

    dataset = args.dataset
    meta = run_dir / "summary.json"
    if meta.is_file() and not dataset:
        dataset = json.loads(meta.read_text()).get("dataset")
    if not dataset:
        dataset = "princeton-nlp/SWE-bench_Verified"

    grading_dir = run_dir / "grading"
    grading_dir.mkdir(parents=True, exist_ok=True)
    cmd = [
        sys.executable, "-m", "swebench.harness.run_evaluation",
        "--dataset_name", dataset,
        "--predictions_path", str(preds_path.resolve()),
        "--run_id", args.run_id,
        "--max_workers", str(args.max_workers),
        "--timeout", str(args.timeout),
        "--cache_level", "env",
    ]
    print(f"Grading with: {' '.join(cmd)}\n  cwd={grading_dir}")
    r = subprocess.run(cmd, cwd=grading_dir)
    if r.returncode != 0:
        sys.exit(r.returncode)

    resolved, unresolved, errors = [], [], []
    reports_dir = grading_dir / "logs" / "run_evaluation" / args.run_id / args.model_name.replace("/", "__")
    for report in sorted(reports_dir.glob("*/report.json")):
        iid = report.parent.name
        data = json.loads(report.read_text())
        entry = data.get(iid, {})
        if entry.get("resolved"):
            resolved.append(iid)
        elif entry.get("error"):
            errors.append(iid)
        else:
            unresolved.append(iid)

    print(f"\n{'=' * 60}")
    print(f"Resolved: {len(resolved)}/{len(resolved) + len(unresolved) + len(errors)}")
    print(f"  resolved:   {resolved}")
    print(f"  unresolved: {unresolved}")
    print(f"  errors:     {errors}")
    print(f"Full logs: {reports_dir}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("run", help="run the stubbs agent on selected instances")
    r.add_argument("--run-id", required=True, help="identifier for this benchmark run")
    r.add_argument("--instances", type=Path, default=None, help="instances.jsonl (default bench/instances.jsonl)")
    r.add_argument("--stubbs", type=Path, default=REPO_ROOT / "stubbs", help="stubbs binary path")
    r.add_argument("--model", default="", help="model override (default: host .stubbs config)")
    r.add_argument("--model-name", default="stubbs", help="model_name_or_path recorded in predictions")
    r.add_argument("--step-limit", type=int, default=40)
    r.add_argument("--cost-limit", type=float, default=2.0)
    r.add_argument("--timeout", type=int, default=1800, help="per-instance timeout in seconds")
    r.add_argument("--workers", type=int, default=2, help="parallel agent runs")
    r.add_argument("--build-workers", type=int, default=4, help="parallel image builds")
    r.add_argument("--no-build", action="store_true", help="skip image build (images must already exist)")
    r.add_argument("--only", nargs="*", help="restrict to these instance IDs")
    r.add_argument("--dataset", default="princeton-nlp/SWE-bench_Verified", help="HF dataset name recorded for grading")
    r.set_defaults(func=cmd_run)

    g = sub.add_parser("grade", help="grade predictions with the official swebench harness")
    g.add_argument("--run-id", required=True)
    g.add_argument("--dataset", default="", help="HF dataset name (default: read from summary.json)")
    g.add_argument("--max-workers", type=int, default=2)
    g.add_argument("--timeout", type=int, default=1800, help="per-instance test timeout in seconds")
    g.add_argument("--model-name", default="stubbs", help="model_name_or_path used in run")
    g.set_defaults(func=cmd_grade)

    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()