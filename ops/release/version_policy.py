#!/usr/bin/env python3
"""Resolve target-only release versions from trusted final tags."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

FINAL_TAG = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
VERSION = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
SYMBOLIC = {"patch", "minor", "major"}


class PolicyError(ValueError):
    pass


def parse_final_tag(tag: str) -> tuple[int, int, int] | None:
    match = FINAL_TAG.fullmatch(tag)
    if not match:
        return None
    return tuple(int(part) for part in match.groups())


def parse_version(target: str) -> tuple[int, int, int]:
    match = VERSION.fullmatch(target)
    if not match:
        raise PolicyError(f"invalid exact version: {target}")
    return tuple(int(part) for part in match.groups())


def format_version(version: tuple[int, int, int]) -> str:
    return ".".join(str(part) for part in version)


def resolve_target(target: str, baseline: tuple[int, int, int]) -> tuple[int, int, int]:
    if target == "patch":
        return (baseline[0], baseline[1], baseline[2] + 1)
    if target == "minor":
        return (baseline[0], baseline[1] + 1, 0)
    if target == "major":
        return (baseline[0] + 1, 0, 0)
    return parse_version(target)


def canonical_version(
    target: str,
    reachable_tags: list[str],
    source_tags: list[str],
    all_tags: list[str],
) -> dict[str, object]:
    source_versions = sorted(
        version for tag in source_tags if (version := parse_final_tag(tag)) is not None
    )
    if source_versions:
        if len(source_versions) != 1:
            raise PolicyError("source has multiple final release tags")
        version = source_versions[0]
        if target not in SYMBOLIC and parse_version(target) != version:
            raise PolicyError("exact target does not match the existing source tag")
        return {
            "version": format_version(version),
            "tag": f"v{format_version(version)}",
            "baseline": format_version(version),
            "recovery": True,
        }

    reachable_versions = [
        version for tag in reachable_tags if (version := parse_final_tag(tag)) is not None
    ]
    baseline = max(reachable_versions, default=(0, 0, 0))
    version = resolve_target(target, baseline)
    if version <= baseline:
        raise PolicyError("resolved version must be greater than the trusted baseline")
    tag = f"v{format_version(version)}"
    if tag in all_tags:
        raise PolicyError(f"tag {tag} already exists on another source")
    return {
        "version": format_version(version),
        "tag": tag,
        "baseline": format_version(baseline),
        "recovery": False,
    }


def git(repo: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo), *args],
        check=False,
        text=True,
        capture_output=True,
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()
        raise PolicyError(f"git {' '.join(args)} failed: {detail}")
    return result.stdout


def cli(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    resolve = sub.add_parser("resolve")
    resolve.add_argument("--target", required=True)
    resolve.add_argument("--source", default="HEAD")
    resolve.add_argument("--repo", default=".")
    args = parser.parse_args(argv)
    if args.command != "resolve":
        return 2
    try:
        repo = Path(args.repo).resolve()
        source = git(repo, "rev-parse", "--verify", f"{args.source}^{{commit}}").strip()
        reachable = git(repo, "tag", "--merged", source, "--list", "v*").splitlines()
        at_source = git(repo, "tag", "--points-at", source, "--list", "v*").splitlines()
        all_tags = git(repo, "tag", "--list", "v*").splitlines()
        result = canonical_version(args.target, reachable, at_source, all_tags)
        result["source"] = source
        print(json.dumps(result, sort_keys=True))
        return 0
    except PolicyError as error:
        print(f"version policy: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(cli(sys.argv[1:]))
