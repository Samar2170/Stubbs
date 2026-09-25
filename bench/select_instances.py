#!/usr/bin/env python3
"""Select SWE-bench Verified instances for local benchmarking.

Pulls the dataset from Hugging Face, filters by repo, and picks a seeded
sample. Instances are grouped by (repo, version) so the docker image builds
share environment layers: the version with the most instances is filled
first, spilling over into the next most common version only if needed.

Usage:
    uv run python select_instances.py --repos django,sympy --count 15 --seed 42
"""

import argparse
import json
import random
import sys
from collections import Counter, defaultdict
from pathlib import Path

DATASET_DEFAULT = "princeton-nlp/SWE-bench_Verified"

REPO_ALIASES = {
    "django": "django/django",
    "sympy": "sympy/sympy",
    "astropy": "astropy/astropy",
    "matplotlib": "matplotlib/matplotlib",
    "scikit-learn": "scikit-learn/scikit-learn",
    "sklearn": "scikit-learn/scikit-learn",
    "sphinx": "sphinx-doc/sphinx",
    "pytest": "pytest-dev/pytest",
    "pylint": "pylint-dev/pylint",
    "requests": "psf/requests",
    "flask": "pallets/flask",
    "seaborn": "mwaskom/seaborn",
}


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dataset", default=DATASET_DEFAULT, help="HF dataset name")
    ap.add_argument("--repos", default="django,sympy", help="comma-separated repo names (or owner/name)")
    ap.add_argument("--count", type=int, default=15, help="number of instances to select")
    ap.add_argument("--seed", type=int, default=42, help="random seed for sampling")
    ap.add_argument(
        "--out",
        type=Path,
        default=Path(__file__).resolve().parent / "instances.jsonl",
        help="output jsonl (full dataset rows; feeds run_bench.py)",
    )
    args = ap.parse_args()

    from datasets import load_dataset

    wanted = set()
    for r in args.repos.split(","):
        r = r.strip()
        if r:
            wanted.add(REPO_ALIASES.get(r.lower(), r))

    print(f"Loading {args.dataset} ...")
    ds = load_dataset(args.dataset, split="test")
    rows = [dict(r) for r in ds if r["repo"] in wanted]
    if not rows:
        sys.exit(f"no instances for repos {sorted(wanted)}")

    by_repo = defaultdict(list)
    for r in rows:
        by_repo[r["repo"]].append(r)

    # Quota per repo: as even as possible; first repos in the list get the remainder.
    repos = sorted(by_repo)
    quota = {repo: 0 for repo in repos}
    for i in range(args.count):
        quota[repos[i % len(repos)]] += 1

    rng = random.Random(args.seed)
    selected = []
    print(f"\n{'repo':24} {'version':8} {'available':>9} {'picked':>7}")
    for repo in repos:
        remaining = quota[repo]
        by_version = defaultdict(list)
        for r in by_repo[repo]:
            by_version[r["version"]].append(r)
        # Fill from the most common version first: fewer unique env image
        # builds (one per repo+version) and shared docker layers.
        ranked = sorted(by_version.items(), key=lambda kv: (-len(kv[1]), kv[0]))
        for version, candidates in ranked:
            if remaining <= 0:
                break
            take = min(remaining, len(candidates))
            picked = rng.sample(candidates, take)
            selected.extend(picked)
            remaining -= take
            print(f"{repo:24} {version:8} {len(candidates):>9} {take:>7}")
        if remaining:
            print(f"{repo:24} WARNING: short by {remaining} instances")

    # Deterministic order (dataset id sort), so runs are reproducible.
    selected.sort(key=lambda r: r["instance_id"])

    out = args.out.resolve()
    with open(out, "w") as f:
        for r in selected:
            f.write(json.dumps(r) + "\n")

    ids = [r["instance_id"] for r in selected]
    print(f"\nSelected {len(selected)} instances -> {out}")
    print("Instance IDs:")
    for iid in ids_with_repo(selected):
        print(f"  {iid}")


def ids_with_repo(rows):
    for r in rows:
        yield f"{r['instance_id']}  ({r['repo']} {r['version']})"


if __name__ == "__main__":
    main()