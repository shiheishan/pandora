#!/usr/bin/env python3
"""Fail closed when the NativeCore capability and panel stable catalogs drift."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


CAPABILITY_RE = re.compile(r'\{Protocol:\s*"([^"]+)"')
SCHEMA_RE = re.compile(
    r'\{NodeType:\s*"([^"]+)",\s*Version:\s*1,\s*Status:\s*"stable"'
)
ALLOW_BLOCK_RE = re.compile(
    r"var stableTypes = \[\.\.\.\]string\{(?P<body>.*?)\n\}",
    re.DOTALL,
)
QUOTED_RE = re.compile(r'"([^"]+)"')


# 面板侧的稳定 schema 与下发名单都在纯函数包 node/protocol 的 schema.go 里。
CAPABILITIES_FILE = "pdnd/kernel/capabilities.go"
PANEL_SCHEMA_FILE = "panel/internal/module/node/protocol/schema.go"


class ParityInputError(Exception):
    """对齐门读不到输入：文件挪了或声明方式改了，必须失败而不是静默通过。"""


def read(root: Path, relative: str) -> str:
    path = root / relative
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ParityInputError(f"cannot read {relative}: {exc}") from exc


def require(label: str, values: set[str], relative: str) -> set[str]:
    if not values:
        raise ParityInputError(f"{label} not found in {relative}; update this parity gate")
    return values


def report(label: str, values: set[str]) -> None:
    print(f"{label} ({len(values)}): {', '.join(sorted(values))}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    root = args.root.resolve()

    try:
        capabilities = require(
            "NativeCore capabilities",
            set(CAPABILITY_RE.findall(read(root, CAPABILITIES_FILE))),
            CAPABILITIES_FILE,
        )
        panel = read(root, PANEL_SCHEMA_FILE)
        schemas = require("stable schemas", set(SCHEMA_RE.findall(panel)), PANEL_SCHEMA_FILE)
        match = ALLOW_BLOCK_RE.search(panel)
        if not match:
            raise ParityInputError(f"stableTypes declaration not found in {PANEL_SCHEMA_FILE}")
        allowlist = require(
            "stableTypes entries", set(QUOTED_RE.findall(match.group("body"))), PANEL_SCHEMA_FILE
        )
    except ParityInputError as exc:
        print(f"PARITY_INPUT_ERROR {exc}", file=sys.stderr)
        return 2

    report("NativeCore capabilities", capabilities)
    report("Panel stable schemas", schemas)
    report("Serving allowlist", allowlist)

    failed = False
    for left_name, left, right_name, right in (
        ("capabilities", capabilities, "panel schemas", schemas),
        ("capabilities", capabilities, "serving allowlist", allowlist),
        ("panel schemas", schemas, "serving allowlist", allowlist),
    ):
        if left != right:
            failed = True
            print(
                f"DRIFT {left_name} -> {right_name}: "
                f"missing={sorted(left - right)} extra={sorted(right - left)}",
                file=sys.stderr,
            )

    if failed:
        return 1
    print("NATIVE_PANEL_PARITY_OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
