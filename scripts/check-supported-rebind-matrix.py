#!/usr/bin/env python3
"""Validate and run the bounded hosted-Docker rebind crash-matrix partitions."""

from __future__ import annotations

import argparse
import copy
import json
import os
import re
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any


TEST_NAME = "TestLiveControllerGatewayRebindCrashMatrix"
PACKAGE = "github.com/hostd/hostd/cmd/hostd"
MANIFEST_PATH = Path("cmd/hostd/gateway_rebind_live_manifest_test.go")
WORKFLOW_PATH = Path(".github/workflows/hosting-gateway-v2-ci.yml")
CONTRACT_PATH = Path("scripts/check-supported-rebind-matrix-contract.json")
CASE_PATTERN = re.compile(r"^(boundary|scenario)/([a-z0-9][a-z0-9-]*)$")
CASE_COMPLETION_ALLOWANCE_SECONDS = 120


class ContractError(RuntimeError):
    """The matrix is not closed, or execution did not prove every owned case."""


def fail(message: str) -> None:
    raise ContractError(message)


def read_json(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot read matrix contract {path}: {error}")
    if not isinstance(data, dict):
        fail("matrix contract must be a JSON object")
    return data


def extract_required_ids(source: str, constant: str) -> list[str]:
    match = re.search(rf"const\s+{re.escape(constant)}\s*=\s*`([^`]*)`", source, re.DOTALL)
    if match is None:
        fail(f"closed source manifest {constant} is absent")
    identifiers = match.group(1).split()
    if not identifiers:
        fail(f"closed source manifest {constant} is empty")
    if any(re.fullmatch(r"[a-z0-9][a-z0-9-]*", identifier) is None for identifier in identifiers):
        fail(f"closed source manifest {constant} has an invalid identifier")
    if len(set(identifiers)) != len(identifiers):
        fail(f"closed source manifest {constant} contains duplicate identifiers")
    return identifiers


def extract_implemented_ids(source: str, variable: str) -> list[str]:
    marker = f"var {variable} ="
    start = source.find(marker)
    if start < 0:
        fail(f"implemented source manifest {variable} is absent")
    opening = source.find("{", start)
    closing = source.find("\n}\n\n", opening)
    if opening < 0 or closing < 0:
        fail(f"implemented source manifest {variable} has an unrecognized declaration")
    identifiers = re.findall(r'^\s*\{"([a-z0-9][a-z0-9-]*)"', source[opening + 1 : closing], re.MULTILINE)
    if not identifiers:
        fail(f"implemented source manifest {variable} is empty")
    if len(set(identifiers)) != len(identifiers):
        fail(f"implemented source manifest {variable} contains duplicate identifiers")
    return identifiers


def source_cases(root: Path) -> list[str]:
    try:
        source = (root / MANIFEST_PATH).read_text(encoding="utf-8")
    except OSError as error:
        fail(f"cannot read closed source manifest: {error}")

    manifests = (
        ("boundary", "gatewayRebindRequiredBoundaryIDs", "gatewayRebindLiveBoundaries"),
        ("scenario", "gatewayRebindRequiredScenarioIDs", "gatewayRebindLiveScenarios"),
    )
    cases: list[str] = []
    for kind, constant, variable in manifests:
        required = extract_required_ids(source, constant)
        implemented = extract_implemented_ids(source, variable)
        if set(required) != set(implemented) or len(required) != len(implemented):
            missing = sorted(set(required) - set(implemented))
            unknown = sorted(set(implemented) - set(required))
            fail(f"closed {kind} source manifest mismatch: missing={missing} unknown={unknown}")
        cases.extend(f"{kind}/{identifier}" for identifier in required)
    if len(cases) != 63:
        fail(f"closed source manifest count={len(cases)} want=63")
    return cases


def group_ids(contract: dict[str, Any]) -> list[str]:
    groups = contract.get("groups")
    if not isinstance(groups, list) or not groups:
        fail("matrix contract groups must be a non-empty array")
    result: list[str] = []
    for group in groups:
        if not isinstance(group, dict) or not isinstance(group.get("id"), str):
            fail("matrix contract has a group without a string id")
        result.append(group["id"])
    if len(set(result)) != len(result):
        fail("matrix contract has duplicate group ids")
    if len(result) != 8:
        fail(f"matrix contract group count={len(result)} want=8")
    return result


def validate_workflow(root: Path, contract: dict[str, Any]) -> None:
    try:
        workflow = (root / WORKFLOW_PATH).read_text(encoding="utf-8")
    except OSError as error:
        fail(f"cannot read hosted CI workflow: {error}")
    job_match = re.search(
        r"^  supported-hostd-rebind-crash-matrix:\n(?P<body>.*?)(?=^  [a-z0-9][a-z0-9-]+:\n|\Z)",
        workflow,
        re.DOTALL | re.MULTILINE,
    )
    if job_match is None:
        fail("hosted CI partition job is absent")
    job = job_match.group("body")
    matrix_match = re.search(r"^        group: \[(?P<groups>[^\]]+)]$", job, re.MULTILINE)
    if matrix_match is None:
        fail("hosted CI partition matrix is absent or not an inline closed list")
    workflow_groups = [item.strip() for item in matrix_match.group("groups").split(",")]
    expected_groups = group_ids(contract)
    if workflow_groups != expected_groups:
        fail(f"workflow partition groups={workflow_groups} want={expected_groups}")
    required_fragments = (
        "fail-fast: false",
        "timeout-minutes: 130",
        "timeout-minutes: 115",
        "--check-contract",
        "--self-test",
        "--run-group",
        "--verify-events",
        "if: always()",
        "Require independent full-hostd fixture cleanup",
    )
    for fragment in required_fragments:
        if fragment not in job:
            fail(f"hosted CI partition job is missing {fragment!r}")
    cleanup_index = job.find("Require independent full-hostd fixture cleanup")
    upload_index = job.find("Upload owned crash-case failure evidence")
    if cleanup_index < 0 or upload_index < 0 or upload_index < cleanup_index:
        fail("hosted CI uploads failure evidence before independent cleanup")
    upload_match = re.search(
        r"^      - name: Upload owned crash-case failure evidence\n(?P<body>.*?)(?=^      - name:|^  [a-z0-9][a-z0-9-]+:\n|\Z)",
        job,
        re.DOTALL | re.MULTILINE,
    )
    if upload_match is None or "timeout-minutes: 5" not in upload_match.group("body"):
        fail("hosted CI failure-evidence upload has no bounded timeout")
    aggregate_match = re.search(
        r"^  supported-hostd-rebind-crash-matrix-complete:\n(?P<body>.*?)(?=^  [a-z0-9][a-z0-9-]+:\n|\Z)",
        workflow,
        re.DOTALL | re.MULTILINE,
    )
    if aggregate_match is None:
        fail("hosted CI partition aggregate job is absent")
    aggregate = aggregate_match.group("body")
    for fragment in (
        "needs: [supported-hostd-rebind-crash-matrix]",
        "if: always()",
        "MATRIX_RESULT: ${{ needs.supported-hostd-rebind-crash-matrix.result }}",
        "test \"$MATRIX_RESULT\" = success",
    ):
        if fragment not in aggregate:
            fail(f"hosted CI partition aggregate is missing {fragment!r}")


def validate_contract(root: Path, contract: dict[str, Any], *, check_workflow: bool = True) -> list[str]:
    if contract.get("test_name") != TEST_NAME:
        fail(f"matrix test name={contract.get('test_name')!r} want={TEST_NAME!r}")
    if contract.get("package") != PACKAGE:
        fail(f"matrix package={contract.get('package')!r} want={PACKAGE!r}")
    max_cases = contract.get("max_cases_per_group")
    if not isinstance(max_cases, int) or max_cases != 8:
        fail(f"matrix max_cases_per_group={max_cases!r} want=8")
    go_timeout = contract.get("go_timeout_minutes")
    group_timeout = contract.get("group_timeout_minutes")
    if go_timeout != 30:
        fail(f"per-case package timeout={go_timeout!r} want=30 minutes")
    if group_timeout != 110:
        fail(f"partition timeout={group_timeout!r} want=110 minutes")
    if go_timeout >= 345 or group_timeout >= 345:
        fail("partition deadlines must stay below the former 345-minute package limit")

    expected_cases = source_cases(root)
    expected_set = set(expected_cases)
    assigned: list[str] = []
    for group in contract["groups"]:
        cases = group.get("cases") if isinstance(group, dict) else None
        if not isinstance(cases, list) or not cases:
            fail(f"matrix group {group.get('id') if isinstance(group, dict) else '<unknown>'!r} has no cases")
        if len(cases) > max_cases:
            fail(f"matrix group {group['id']!r} has {len(cases)} cases, limit is {max_cases}")
        for case in cases:
            if not isinstance(case, str) or CASE_PATTERN.fullmatch(case) is None:
                fail(f"matrix group {group['id']!r} has invalid case {case!r}")
            if case not in expected_set:
                fail(f"matrix group {group['id']!r} has unknown source case {case!r}")
            assigned.append(case)
    duplicates = sorted(case for case in set(assigned) if assigned.count(case) != 1)
    missing = sorted(expected_set - set(assigned))
    if duplicates or missing or len(assigned) != len(expected_cases):
        fail(f"matrix partition is not closed: duplicate={duplicates} missing={missing}")
    if check_workflow:
        validate_workflow(root, contract)
    return expected_cases


def selected_group(contract: dict[str, Any], group_id: str) -> dict[str, Any]:
    for group in contract["groups"]:
        if group["id"] == group_id:
            return group
    fail(f"unknown matrix group {group_id!r}")


def read_events(path: Path) -> list[dict[str, Any]]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        fail(f"cannot read test event stream {path}: {error}")
    if not lines:
        fail("test event stream is empty")
    events: list[dict[str, Any]] = []
    for line_number, line in enumerate(lines, start=1):
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            fail(f"test event stream line {line_number} is not JSON: {error.msg}")
        if not isinstance(event, dict):
            fail(f"test event stream line {line_number} is not an object")
        events.append(event)
    return events


def verify_events(contract: dict[str, Any], group_id: str, events_path: Path) -> None:
    group = selected_group(contract, group_id)
    cases = group["cases"]
    events = read_events(events_path)
    invocation = 0
    phase = "await-start"
    expected_child = ""
    for event in events:
        package = event.get("Package")
        action = event.get("Action")
        test = event.get("Test")
        if package != PACKAGE:
            fail(f"test event has unexpected package {package!r}")
        if action not in {"start", "run", "pass", "output"}:
            fail(f"test event has disallowed action={action!r} test={test!r}")
        if phase == "await-start":
            if action != "start" or test is not None:
                fail(f"invocation {invocation + 1} does not begin with a package start")
            if invocation >= len(cases):
                fail("test event stream has more package invocations than the owned cases")
            expected_child = f"{TEST_NAME}/{cases[invocation]}"
            phase = "await-parent-run"
            continue
        if action == "output":
            if test is not None and test not in {TEST_NAME, expected_child}:
                fail(f"invocation {invocation + 1} output escaped its owned case: {test!r}")
            continue
        if phase == "await-parent-run":
            if action != "run" or test != TEST_NAME:
                fail(f"invocation {invocation + 1} lacks the parent run before its child")
            phase = "await-child-run"
            continue
        if phase == "await-child-run":
            if action != "run" or test != expected_child:
                fail(f"invocation {invocation + 1} ran {test!r}, want {expected_child!r}")
            phase = "await-child-pass"
            continue
        if phase == "await-child-pass":
            if action != "pass" or test != expected_child:
                fail(f"invocation {invocation + 1} did not pass exactly its owned child")
            phase = "await-parent-pass"
            continue
        if phase == "await-parent-pass":
            if action != "pass" or test != TEST_NAME:
                fail(f"invocation {invocation + 1} did not pass its parent after the child")
            phase = "await-package-pass"
            continue
        if phase == "await-package-pass":
            if action != "pass" or test is not None:
                fail(f"invocation {invocation + 1} has no final package pass")
            invocation += 1
            phase = "await-start"
            continue
        fail(f"invalid event verifier phase {phase!r}")
    if phase != "await-start":
        fail(f"test event stream ended during invocation {invocation + 1} at {phase}")
    if invocation != len(cases):
        fail(f"package invocation count={invocation} want={len(cases)}")


def write_result(path: Path, command: list[str], returncode: int | None, elapsed_seconds: float,
                 *, timed_out: bool = False) -> None:
    path.write_text(
        json.dumps(
            {
                "command": command,
                "returncode": returncode,
                "elapsed_seconds": round(elapsed_seconds, 3),
                "timed_out": timed_out,
            },
            sort_keys=True,
        )
        + "\n",
        encoding="utf-8",
    )


def go_re2_literal(value: str) -> str:
    if "\\E" in value:
        fail(f"cannot quote a Go regexp literal containing \\E: {value!r}")
    return rf"\Q{value}\E"


def terminate_process_group(process: subprocess.Popen[bytes]) -> None:
    try:
        if os.name == "posix":
            os.killpg(process.pid, signal.SIGKILL)
        else:
            process.kill()
    except ProcessLookupError:
        return


def run_case_command(command: list[str], root: Path, timeout_seconds: float) -> tuple[bytes, int | None, bool]:
    popen_arguments: dict[str, Any] = {
        "cwd": root,
        "stdout": subprocess.PIPE,
        "stderr": subprocess.STDOUT,
    }
    if os.name == "posix":
        popen_arguments["start_new_session"] = True
    else:
        popen_arguments["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
    process = subprocess.Popen(command, **popen_arguments)
    try:
        output, _ = process.communicate(timeout=timeout_seconds)
        return output, process.returncode, False
    except subprocess.TimeoutExpired as error:
        partial_output = error.output or b""
        terminate_process_group(process)
        try:
            output, _ = process.communicate(timeout=10)
            return output or partial_output, process.returncode, True
        except subprocess.TimeoutExpired as second_error:
            partial_output = second_error.output or partial_output
            terminate_process_group(process)
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                pass
            if process.stdout is not None:
                process.stdout.close()
            return partial_output, process.returncode, True


def posix_process_ended(process_id: int, timeout_seconds: float) -> bool:
    deadline = time.monotonic() + timeout_seconds
    status_path = Path(f"/proc/{process_id}/stat")
    while True:
        try:
            status = status_path.read_text(encoding="utf-8")
        except FileNotFoundError:
            return True
        after_name = status.rsplit(") ", 1)
        if len(after_name) == 2 and after_name[1].startswith("Z"):
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.02)


def emit_case_output(output: bytes) -> None:
    if not output:
        return
    sys.stdout.buffer.write(output)
    if not output.endswith(b"\n"):
        sys.stdout.buffer.write(b"\n")
    sys.stdout.buffer.flush()


def run_group(root: Path, contract: dict[str, Any], group_id: str, events_path: Path,
              log_directory: Path, go_command: str, *, emit_output: bool = True) -> None:
    group = selected_group(contract, group_id)
    events_path.parent.mkdir(parents=True, exist_ok=True)
    log_directory.mkdir(parents=True, exist_ok=True)
    events_path.write_bytes(b"")
    started = time.monotonic()
    group_seconds = contract["group_timeout_minutes"] * 60
    go_timeout = f"{contract['go_timeout_minutes']}m"
    for index, case in enumerate(group["cases"], start=1):
        remaining = group_seconds - (time.monotonic() - started)
        case_window = contract["go_timeout_minutes"] * 60 + CASE_COMPLETION_ALLOWANCE_SECONDS
        if remaining < case_window:
            fail(
                f"partition {group_id!r} has only {remaining:.1f}s remaining, below the "
                f"{case_window}s complete-case window for {case!r}"
            )
        kind, identifier = case.split("/", 1)
        selector = f"^{go_re2_literal(TEST_NAME)}$/^{go_re2_literal(kind)}$/^{go_re2_literal(identifier)}$"
        command = [
            go_command,
            "test",
            "-tags",
            "live_docker",
            "-json",
            "-v",
            "-count=1",
            f"-timeout={go_timeout}",
            "./cmd/hostd",
            "-run",
            selector,
        ]
        prefix = f"{index:02d}-{kind}-{identifier}"
        case_log = log_directory / f"{prefix}.jsonl"
        case_result = log_directory / f"{prefix}.result.json"
        case_started = time.monotonic()
        try:
            output, returncode, timed_out = run_case_command(command, root, case_window)
        except OSError as error:
            case_log.write_bytes(b"")
            write_result(case_result, command, None, time.monotonic() - case_started)
            fail(f"cannot start case {case!r}: {error}")
        case_log.write_bytes(output)
        with events_path.open("ab") as all_events:
            all_events.write(output)
            if output and not output.endswith(b"\n"):
                all_events.write(b"\n")
        if emit_output:
            emit_case_output(output)
        write_result(case_result, command, returncode, time.monotonic() - case_started, timed_out=timed_out)
        if timed_out:
            fail(f"case {case!r} exceeded its bounded execution window; logs are in {log_directory}")
        if returncode is None:
            fail(f"case {case!r} exited without a status; logs are in {log_directory}")
        if returncode != 0:
            fail(f"case {case!r} command exited {returncode}; logs are in {log_directory}")


def expect_contract_error(name: str, operation: Any) -> None:
    try:
        operation()
    except ContractError:
        return
    fail(f"self-test {name!r} accepted an invalid input")


def synthetic_events(contract: dict[str, Any], group_id: str) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    for case in selected_group(contract, group_id)["cases"]:
        name = f"{TEST_NAME}/{case}"
        events.extend(
            [
                {"Action": "start", "Package": PACKAGE},
                {"Action": "run", "Package": PACKAGE, "Test": TEST_NAME},
                {"Action": "run", "Package": PACKAGE, "Test": name},
                {"Action": "pass", "Package": PACKAGE, "Test": name},
                {"Action": "pass", "Package": PACKAGE, "Test": TEST_NAME},
                {"Action": "pass", "Package": PACKAGE},
            ]
        )
    return events


def self_test(root: Path, contract: dict[str, Any]) -> None:
    validate_contract(root, contract)
    first_group = contract["groups"][0]["id"]

    omitted = copy.deepcopy(contract)
    omitted["groups"][-1]["cases"].pop()
    expect_contract_error("omission", lambda: validate_contract(root, omitted))

    overlap = copy.deepcopy(contract)
    overlap["groups"][-1]["cases"][-1] = overlap["groups"][0]["cases"][0]
    expect_contract_error("overlap", lambda: validate_contract(root, overlap))

    unknown = copy.deepcopy(contract)
    unknown["groups"][-1]["cases"][-1] = "scenario/not-a-closed-case"
    expect_contract_error("unknown", lambda: validate_contract(root, unknown))

    short_case_timeout = copy.deepcopy(contract)
    short_case_timeout["go_timeout_minutes"] = 20
    expect_contract_error("short per-case deadline", lambda: validate_contract(root, short_case_timeout))

    short_group_timeout = copy.deepcopy(contract)
    short_group_timeout["group_timeout_minutes"] = 90
    expect_contract_error("short group deadline", lambda: validate_contract(root, short_group_timeout))

    expect_contract_error("unknown group", lambda: selected_group(contract, "partition-99"))

    with tempfile.TemporaryDirectory(prefix="rig-supported-rebind-matrix-") as temporary:
        temporary_path = Path(temporary)
        events_path = temporary_path / "events.jsonl"
        events = synthetic_events(contract, first_group)
        events_path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
        verify_events(contract, first_group, events_path)

        skipped = copy.deepcopy(events)
        skipped[2]["Action"] = "skip"
        events_path.write_text("".join(json.dumps(event) + "\n" for event in skipped), encoding="utf-8")
        expect_contract_error("JSON skip", lambda: verify_events(contract, first_group, events_path))

        failed = copy.deepcopy(events)
        failed[2]["Action"] = "fail"
        events_path.write_text("".join(json.dumps(event) + "\n" for event in failed), encoding="utf-8")
        expect_contract_error("JSON fail", lambda: verify_events(contract, first_group, events_path))

        missing = copy.deepcopy(events)
        missing.pop(2)
        events_path.write_text("".join(json.dumps(event) + "\n" for event in missing), encoding="utf-8")
        expect_contract_error("JSON missing", lambda: verify_events(contract, first_group, events_path))

        missing_start = copy.deepcopy(events)
        missing_start.pop(0)
        events_path.write_text("".join(json.dumps(event) + "\n" for event in missing_start), encoding="utf-8")
        expect_contract_error("JSON missing package start", lambda: verify_events(contract, first_group, events_path))

        duplicate_child = copy.deepcopy(events)
        duplicate_child.insert(4, copy.deepcopy(duplicate_child[3]))
        events_path.write_text("".join(json.dumps(event) + "\n" for event in duplicate_child), encoding="utf-8")
        expect_contract_error("JSON duplicate child completion", lambda: verify_events(contract, first_group, events_path))

        reordered = copy.deepcopy(events)
        reordered[1], reordered[2] = reordered[2], reordered[1]
        events_path.write_text("".join(json.dumps(event) + "\n" for event in reordered), encoding="utf-8")
        expect_contract_error("JSON reordered lifetime", lambda: verify_events(contract, first_group, events_path))

        other_package = copy.deepcopy(events)
        other_package[0]["Package"] = "example.invalid/other"
        events_path.write_text("".join(json.dumps(event) + "\n" for event in other_package), encoding="utf-8")
        expect_contract_error("JSON unknown package", lambda: verify_events(contract, first_group, events_path))

        unknown_action = copy.deepcopy(events)
        unknown_action[0]["Action"] = "pause"
        events_path.write_text("".join(json.dumps(event) + "\n" for event in unknown_action), encoding="utf-8")
        expect_contract_error("JSON unknown action", lambda: verify_events(contract, first_group, events_path))

        command_events = temporary_path / "command-events.jsonl"
        command_logs = temporary_path / "command-logs"
        expect_contract_error(
            "shell failure",
            lambda: run_group(
                root, contract, first_group, command_events, command_logs, sys.executable, emit_output=False
            ),
        )
        if not list(command_logs.glob("*.result.json")):
            fail("self-test shell failure did not leave per-case exit evidence")

        if os.name == "posix":
            marker = temporary_path / "pipe-holder-child.pid"
            child_code = (
                "from pathlib import Path\n"
                "import os, sys, time\n"
                "Path(sys.argv[1]).write_text(str(os.getpid()), encoding='utf-8')\n"
                "time.sleep(60)\n"
            )
            parent_code = (
                "from pathlib import Path\n"
                "import subprocess, sys, time\n"
                "marker = Path(sys.argv[1])\n"
                "subprocess.Popen([sys.executable, '-c', sys.argv[2], str(marker)])\n"
                "deadline = time.monotonic() + 3\n"
                "while not marker.exists() and time.monotonic() < deadline:\n"
                "    time.sleep(0.01)\n"
                "if not marker.exists():\n"
                "    raise SystemExit(3)\n"
            )
            pipe_holder = [
                sys.executable,
                "-c",
                parent_code,
                str(marker),
                child_code,
            ]
            started = time.monotonic()
            _, returncode, timed_out = run_case_command(pipe_holder, root, 5)
            try:
                child_process_id = int(marker.read_text(encoding="utf-8"))
            except (OSError, ValueError) as error:
                fail(f"self-test pipe-holding child did not publish its process id: {error}")
            if (
                returncode != 0
                or not timed_out
                or time.monotonic() - started > 20
                or not posix_process_ended(child_process_id, 5)
            ):
                fail("self-test process-group timeout did not kill a pipe-holding child")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parent.parent)
    actions = parser.add_mutually_exclusive_group(required=True)
    actions.add_argument("--check-contract", action="store_true")
    actions.add_argument("--self-test", action="store_true")
    actions.add_argument("--run-group", metavar="GROUP")
    actions.add_argument("--verify-events", metavar="GROUP")
    parser.add_argument("--events", type=Path)
    parser.add_argument("--log-directory", type=Path)
    parser.add_argument("--go-command", default="go")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    root = args.repo.resolve()
    contract = read_json(root / CONTRACT_PATH)
    try:
        validate_contract(root, contract)
        if args.check_contract:
            return 0
        if args.self_test:
            self_test(root, contract)
            return 0
        if args.run_group:
            if args.events is None or args.log_directory is None:
                fail("--run-group requires --events and --log-directory")
            run_group(root, contract, args.run_group, args.events, args.log_directory, args.go_command)
            return 0
        if args.verify_events:
            if args.events is None:
                fail("--verify-events requires --events")
            verify_events(contract, args.verify_events, args.events)
            return 0
        fail("no action selected")
    except ContractError as error:
        print(f"supported rebind matrix contract failure: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
