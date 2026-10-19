#!/usr/bin/env python3
"""Fail-closed coverage and CI depth checks."""

import argparse
import json
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/conformance.yml"
FLOORS = ROOT / "testing/coverage-floor.json"
RACE_COMMAND = "go test -race -count=1 ./..."
FUZZ_COMMAND = "script/gate.sh fuzz"
FUZZ_PACKAGE = "./internal/exif"
REQUIRED_FUZZ_TARGETS = {"FuzzReadEXIF"}


class GateError(RuntimeError):
    pass


def run(args: list[str]) -> str:
    result = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, check=False)
    if result.returncode:
        raise GateError(f"{' '.join(args)} failed:\n{result.stdout}{result.stderr}")
    return result.stdout


def coverage_errors(
    floors: dict[str, float], packages: set[str], measured: dict[str, float]
) -> list[str]:
    errors = [f"discovered package has no coverage floor: {p}" for p in sorted(packages - floors.keys())]
    errors += [f"coverage floor targets missing package: {p}" for p in sorted(floors.keys() - packages)]
    for package in sorted(packages & floors.keys()):
        if package not in measured:
            errors.append(f"package has no measured coverage: {package}")
        elif measured[package] < floors[package]:
            errors.append(
                f"{package} coverage {measured[package]:.1f}% is below {floors[package]:.1f}%"
            )
    return errors


def job_block(workflow: str, name: str) -> str:
    active = "\n".join(line for line in workflow.splitlines() if not line.lstrip().startswith("#"))
    match = re.search(
        rf"^  {re.escape(name)}:\n(.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)",
        active,
        re.MULTILINE | re.DOTALL,
    )
    return match.group(1) if match else ""


def workflow_errors(workflow: str, discovered_fuzz: set[str]) -> list[str]:
    race = job_block(workflow, "race")
    aggregate = job_block(workflow, "conformance")
    fuzz = job_block(workflow, "fuzz")
    errors: list[str] = []
    if f"run: {RACE_COMMAND}" not in race:
        errors.append("race job does not execute the required race command")
    if "continue-on-error:" in race or re.search(r"^    if:", race, re.MULTILINE):
        errors.append("race job must be unconditional and blocking")
    if "needs: [fast-conformance, race]" not in aggregate:
        errors.append("conformance aggregator does not require race")
    if 'test "$RACE_RESULT" = success' not in aggregate:
        errors.append("conformance aggregator can mask race failure")
    if "schedule:" not in workflow or "if: github.event_name == 'schedule'" not in fuzz:
        errors.append("fuzz job is not schedule-only")
    if f"run: {FUZZ_COMMAND}" not in fuzz or "FUZZ_TIME: 20s" not in fuzz:
        errors.append("fuzz job does not execute a bounded fuzz command")
    scheduled = set(re.findall(r"^          - (Fuzz[A-Za-z0-9_]+)$", fuzz, re.MULTILINE))
    for target in sorted(REQUIRED_FUZZ_TARGETS - discovered_fuzz):
        errors.append(f"required fuzz target was not discovered: {target}")
    for target in sorted(discovered_fuzz ^ scheduled):
        errors.append(f"fuzz target discovery/schedule mismatch: {target}")
    return errors


def load_floors() -> dict[str, float]:
    document = json.loads(FLOORS.read_text(encoding="utf-8"))
    if document.get("schema") != 1 or not isinstance(document.get("packages"), dict):
        raise GateError("coverage-floor.json must have schema 1 and a packages object")
    floors = document["packages"]
    if not floors or any(isinstance(v, bool) or not isinstance(v, (int, float)) or v <= 0 or v > 100 for v in floors.values()):
        raise GateError("coverage floors must be numbers in (0, 100]")
    return {package: float(floor) for package, floor in floors.items()}


def discover_packages() -> set[str]:
    return set(run(["go", "list", "./..."]).splitlines())


def discover_fuzz_targets() -> set[str]:
    output = run(["go", "test", "-list", r"^Fuzz[A-Za-z0-9_]+$", FUZZ_PACKAGE])
    return set(re.findall(r"^Fuzz[A-Za-z0-9_]+$", output, re.MULTILINE))


def check_contract() -> None:
    floors = load_floors()
    packages = discover_packages()
    errors = coverage_errors(floors, packages, {package: floors[package] for package in packages if package in floors})
    errors += workflow_errors(WORKFLOW.read_text(encoding="utf-8"), discover_fuzz_targets())
    if errors:
        raise GateError("\n".join(errors))


def check_coverage() -> None:
    floors = load_floors()
    packages = discover_packages()
    measured: dict[str, float] = {}
    with tempfile.TemporaryDirectory(prefix="photos-coverage-") as temp:
        for index, package in enumerate(sorted(packages)):
            profile = Path(temp) / f"{index}.out"
            sys.stdout.write(run(["go", "test", "-count=1", "-covermode=set", f"-coverprofile={profile}", package]))
            summary = run(["go", "tool", "cover", f"-func={profile}"])
            match = re.search(r"^total:.*?([0-9]+(?:\.[0-9]+)?)%$", summary, re.MULTILINE)
            if not match:
                raise GateError(f"cannot parse coverage for {package}")
            measured[package] = float(match.group(1))
    errors = coverage_errors(floors, packages, measured)
    if errors:
        raise GateError("\n".join(errors))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("contract", "coverage", "self-test", "check"))
    command = parser.parse_args().command
    try:
        if command in {"self-test", "check"}:
            sys.stdout.write(run([sys.executable, ".github/workflows/test_depth_gate_test.py"]))
        if command in {"contract", "check"}:
            check_contract()
        if command in {"coverage", "check"}:
            check_coverage()
    except (GateError, OSError, json.JSONDecodeError) as error:
        print(f"depth gate: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
