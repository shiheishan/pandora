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
    r"var stableProtocolTypes = \[\.\.\.\]string\{(?P<body>.*?)\n\}",
    re.DOTALL,
)
QUOTED_RE = re.compile(r'"([^"]+)"')


def read(root: Path, relative: str) -> str:
    return (root / relative).read_text(encoding="utf-8")


def report(label: str, values: set[str]) -> None:
    print(f"{label} ({len(values)}): {', '.join(sorted(values))}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    root = args.root.resolve()

    capabilities = set(CAPABILITY_RE.findall(read(root, "pdnd/kernel/capabilities.go")))
    schemas = set(SCHEMA_RE.findall(read(root, "panel/internal/domain/nodefabric/protocol_schema.go")))
    admin = read(root, "panel/internal/domain/nodefabric/node_admin.go")
    match = ALLOW_BLOCK_RE.search(admin)
    if not match:
        print("stableProtocolTypes declaration not found", file=sys.stderr)
        return 2
    allowlist = set(QUOTED_RE.findall(match.group("body")))

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
