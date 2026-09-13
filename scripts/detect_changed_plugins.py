#!/usr/bin/env python3
"""Detect changed first-level plugin modules and emit a CI matrix."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path, PurePosixPath

from validate_repo import REPO_ROOT, plugin_dirs


ZERO_SHA = "0" * 40


def git(repo_root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=repo_root,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
    )
    return result.stdout


def commit_exists(repo_root: Path, revision: str) -> bool:
    if not revision or revision == ZERO_SHA:
        return False
    result = subprocess.run(
        ["git", "cat-file", "-e", f"{revision}^{{commit}}"],
        cwd=repo_root,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def changed_files(repo_root: Path, base: str, head: str) -> list[str]:
    if not commit_exists(repo_root, head):
        raise ValueError(f"head revision is not available: {head}")

    if commit_exists(repo_root, base):
        output = git(
            repo_root,
            "-c",
            "core.quotepath=false",
            "diff",
            "--name-only",
            f"{base}...{head}",
            "--",
        )
    else:
        print(
            f"Base revision {base or '<empty>'} is unavailable; testing all current plugins.",
            file=sys.stderr,
        )
        output = git(
            repo_root,
            "-c",
            "core.quotepath=false",
            "ls-tree",
            "-r",
            "--name-only",
            head,
        )

    return [line for line in output.splitlines() if line]


def select_changed_plugins(files: list[str], available_plugins: set[str]) -> list[str]:
    changed: set[str] = set()
    for file_name in files:
        parts = PurePosixPath(file_name).parts
        if parts and parts[0] in available_plugins:
            changed.add(parts[0])
    return sorted(changed)


def matrix_for(plugins: list[str]) -> dict[str, list[dict[str, object]]]:
    if not plugins:
        return {
            "include": [
                {
                    "plugin": ".",
                    "label": "no changed plugins",
                    "run_tests": False,
                }
            ]
        }

    return {
        "include": [
            {"plugin": plugin, "label": plugin, "run_tests": True}
            for plugin in plugins
        ]
    }


def write_github_outputs(
    output_path: Path, plugins: list[str], matrix: dict[str, object]
) -> None:
    with output_path.open("a", encoding="utf-8", newline="\n") as output_file:
        output_file.write(f"plugins={json.dumps(plugins, separators=(',', ':'))}\n")
        output_file.write(f"matrix={json.dumps(matrix, separators=(',', ':'))}\n")
        output_file.write(f"has_changes={'true' if plugins else 'false'}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, help="base Git revision")
    parser.add_argument("--head", default="HEAD", help="head Git revision")
    parser.add_argument(
        "--github-output",
        type=Path,
        help="append plugins, matrix, and has_changes to this GitHub output file",
    )
    args = parser.parse_args()

    available_plugins = {plugin_dir.name for plugin_dir in plugin_dirs(REPO_ROOT)}
    try:
        files = changed_files(REPO_ROOT, args.base, args.head)
    except (subprocess.CalledProcessError, ValueError) as exc:
        print(f"Changed plugin detection failed: {exc}", file=sys.stderr)
        return 1

    plugins = select_changed_plugins(files, available_plugins)
    matrix = matrix_for(plugins)

    if args.github_output:
        write_github_outputs(args.github_output, plugins, matrix)
        summary = ", ".join(plugins) if plugins else "none"
        print(f"Changed plugins: {summary}")
    else:
        print(json.dumps(matrix, separators=(",", ":")))

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
