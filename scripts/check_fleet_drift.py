#!/usr/bin/env python3
"""Check the shared free-fleet contract across local sibling repositories.

This is intentionally check-only. It never copies or overwrites source files.
career-fleet may extend its first migration, shared models, and operations
documentation with profile metadata, so those career-specific overlays are
normalized or intentionally excluded from byte-for-byte comparison.
Package-specific aliases, bundled skills, and task presets are also
variant-specific and are intentionally outside the shared-file allowlist.
"""
from __future__ import annotations

import argparse
import hashlib
import re
import subprocess
import sys
from pathlib import Path


EXACT_FILES = (
    "scripts/check_fleet_drift.py",
    "tests/test_route_policy_contract.py",
    "free_fleet/providers/base.py",
    "free_fleet/providers/registry.py",
    "free_fleet/providers/demo.py",
    "free_fleet/providers/openai_compatible.py",
    "free_fleet/providers/openrouter.py",
    "free_fleet/providers/opencode.py",
    "free_fleet/catalog.py",
    "free_fleet/export.py",
    "free_fleet/grounding.py",
    "free_fleet/input_data.py",
    "free_fleet/packer.py",
    "free_fleet/sessions.py",
    "free_fleet/slicer.py",
    "free_fleet/ui.py",
    "free_fleet/migrations/002_intelligence_and_policy.sql",
    "free_fleet/data/routes.seed.json",
    "free_fleet/resources/skill/references/task-contracts.md",
    "free_fleet/resources/skill/references/operations.md",
    "skills/free-fleet/references/operations.md",
    ".agents/skills/free-fleet/references/operations.md",
)


def _default_repos(cwd: Path) -> list[Path]:
    candidates = (
        cwd,
        cwd.parent / "bulk-lanes",
        cwd.parent / "account-fleet",
        cwd.parent / "Career" / "career-fleet",
        cwd.parent.parent / "bulk-lanes",
        cwd.parent.parent / "account-fleet",
        cwd.parent.parent / "Career" / "career-fleet",
    )
    repos: list[Path] = []
    for candidate in candidates:
        resolved = candidate.resolve()
        if resolved.is_dir() and (resolved / ".git").exists() and resolved not in repos:
            repos.append(resolved)
    return repos


def _normalized_digest(path: Path, relative: str) -> str:
    text = path.read_text(encoding="utf-8")
    if relative.endswith("references/operations.md"):
        text = re.sub(
            r'Schema version is `?"3"`?\. Account runs can also retain the exact immutable Ideal Company Profile revision used for the campaign\.',
            'Schema version is `"2"`.',
            text,
        )
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _check_exact_files(repos: list[Path]) -> bool:
    ok = True
    baseline = repos[0]
    for relative in EXACT_FILES:
        expected = baseline / relative
        if not expected.is_file():
            print(f"MISSING {baseline}: {relative}")
            ok = False
            continue
        expected_digest = _normalized_digest(expected, relative)
        for repo in repos[1:]:
            candidate = repo / relative
            if not candidate.is_file():
                print(f"MISSING {repo}: {relative}")
                ok = False
            elif _normalized_digest(candidate, relative) != expected_digest:
                print(f"DRIFT  {relative}: {baseline} != {repo}")
                ok = False
    return ok


def _run_contract(repo: Path) -> bool:
    result = subprocess.run(
        [sys.executable, "-m", "pytest", "-q", "tests/test_route_policy_contract.py"],
        cwd=repo,
        check=False,
    )
    if result.returncode:
        print(f"FAIL   route-policy contract: {repo}")
        return False
    print(f"PASS   route-policy contract: {repo}")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--repo",
        dest="repos",
        action="append",
        type=Path,
        help="Repository to check; repeat for a multi-repo comparison",
    )
    parser.add_argument(
        "--self",
        action="store_true",
        help="Check only the current repository (the CI mode)",
    )
    parser.add_argument(
        "--no-tests",
        action="store_true",
        help="Compare exact files without running the contract test",
    )
    args = parser.parse_args()

    repos = [path.expanduser().resolve() for path in (args.repos or [])]
    if not repos:
        repos = [Path.cwd().resolve()] if args.self else _default_repos(Path.cwd())
    if not repos:
        parser.error("no Git repositories found; pass --repo or run inside a checkout")

    ok = True
    if len(repos) > 1:
        ok = _check_exact_files(repos) and ok
    if not args.no_tests:
        for repo in repos:
            ok = _run_contract(repo) and ok
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
