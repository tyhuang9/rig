#!/usr/bin/env python3
"""Require the exact Linux cold-restart manifest symlink control to pass."""

from __future__ import annotations

import argparse
from collections import Counter
import json
from pathlib import Path
import sys


PACKAGE = "github.com/hostd/hostd/cmd/hostd"
PARENT = "TestLiveColdRuntimeRestartManifestRejectsCrossedOrSensitiveShape"
CHILD = f"{PARENT}/symlink_manifest_and_source"
TESTS = (PARENT, CHILD)


def verify(text: str) -> None:
    counts: Counter[tuple[str | None, str]] = Counter()
    for line in text.splitlines():
        if not line.strip():
            continue
        event = json.loads(line)
        if not isinstance(event, dict) or event.get("Package") != PACKAGE:
            raise ValueError("event must belong to the hostd package")
        test = event.get("Test")
        action = event.get("Action")
        if test is not None and test not in TESTS:
            raise ValueError("unexpected test in exact symlink selection")
        if action not in ("start", "run", "pause", "cont", "output", "pass"):
            raise ValueError("failed, skipped or invalid event")
        counts[(test, action)] += 1
    for test in TESTS:
        if counts[(test, "run")] != 1 or counts[(test, "pass")] != 1:
            raise ValueError("each required test must run and pass exactly once")
    if counts[(None, "pass")] != 1:
        raise ValueError("hostd package must pass exactly once")


def self_test() -> None:
    def event(action: str, test: str | None = None) -> dict:
        value = {"Package": PACKAGE, "Action": action}
        if test is not None:
            value["Test"] = test
        return value

    def encode(events: list) -> str:
        return "\n".join(json.dumps(value) for value in events) + "\n"

    valid = [event("start"), event("run", PARENT), event("run", CHILD),
             event("pass", CHILD), event("pass", PARENT), event("pass")]
    verify(encode(valid))
    # Output is diagnostic text, including nested process results, not an Action.
    noise = event("output", CHILD)
    noise["Output"] = '{"Action":"fail","Test":"nested"}\n--- SKIP: nested\n'
    verify(encode(valid[:3] + [noise] + valid[3:]))

    failures: dict[str, str] = {"empty": "", "malformed": "not JSON\n",
                                "array": "[]\n", "null": "null\n"}
    for index in range(1, len(valid)):
        failures[f"missing event {index}"] = encode(valid[:index] + valid[index + 1:])
        failures[f"duplicate event {index}"] = encode(valid + [valid[index]])
    for test in (None, *TESTS):
        for action in ("fail", "skip"):
            failures[f"{action} {test}"] = encode(valid + [event(action, test)])
    failures["foreign package"] = encode(valid + [{"Package": "other", "Action": "output"}])
    failures["missing package"] = encode(valid + [{"Action": "output"}])
    failures["unexpected test"] = encode(valid + [event("run", PARENT + "/other")])
    failures["invalid action"] = encode(valid + [event("unknown")])
    failures["invalid test type"] = encode(valid + [{**event("run"), "Test": []}])
    failures["invalid action type"] = encode(valid + [{**event("run"), "Action": []}])
    for name, text in failures.items():
        try:
            verify(text)
        except ValueError:
            continue
        raise AssertionError(f"negative control unexpectedly passed: {name}")
    print(f"Manifest symlink contract: 2 positive and {len(failures)} refusal controls passed")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--self-test", action="store_true")
    action.add_argument("--events", type=Path)
    args = parser.parse_args()
    try:
        if args.self_test:
            self_test()
        else:
            verify(args.events.read_text(encoding="utf-8"))
    except (OSError, ValueError, AssertionError) as error:
        print(f"Manifest symlink contract failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
