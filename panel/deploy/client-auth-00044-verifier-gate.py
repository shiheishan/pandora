#!/usr/bin/env python3
"""Independent Phase-A Golden20 envelope/receipt generator.

This helper intentionally does not import, execute, or reproduce the Go
verifier serializer.  It derives only the public detached envelope,
non-authorizing synthetic expectations, and expected receipt from exact input
bytes using Python's standard library.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import stat
import struct
import sys
from collections import OrderedDict
from pathlib import Path
from typing import NoReturn

ARTIFACT_FORMAT = "pandora-device-key-classification-v1"
HMAC_VERSION = "pandora-client-auth-00044-classification-artifact-hmac-v1"
RULE_VERSION = "pandora-client-auth-00044-classification-v1"
DETACHED_FORMAT = "pandora-client-auth-00044-classifier-detached-v1"
EXPECTATIONS_FORMAT = "pandora-client-auth-00044-artifact-expectations-v1"
RECEIPT_FORMAT = "pandora-client-auth-00044-artifact-verifier-receipt-v1"
RELEASE_ID = "phase-a-synthetic-not-authorizing"
ARTIFACT_KEY_ID = "artifact-2026-01"
EVIDENCE_KEY_ID = "evidence-2026-01"
GOLDEN_SOURCE_SHA = "9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8"
GOLDEN_ARTIFACT_SHA = "4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c"
GOLDEN_HMAC = "17607946658785fb135d5fc8019c702c2dd3c2b8833c2e42f06c19b3c16a8606"
SYNTHETIC_CLASSIFIER_SHA = hashlib.sha256(
    b"NON_AUTHORIZING_SYNTHETIC_CLASSIFIER_ELF_IDENTITY_V1\n"
).hexdigest()
PUBLIC_CAPTURE_LIMIT = 128 * 1024 * 1024


def die(message: str) -> NoReturn:
    raise SystemExit(f"gate_generator=FAIL reason={message}")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def canonical(value: OrderedDict[str, object]) -> bytes:
    return (json.dumps(value, ensure_ascii=True, separators=(",", ":")) + "\n").encode("ascii")


def read_regular_0600(path: Path, exact: int | None = None, limit: int | None = None) -> bytearray:
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(path, flags)
    except OSError:
        die(f"input_open:{path.name}")
    data = bytearray()
    try:
        before = os.fstat(fd)
        identity = (before.st_dev, before.st_ino, before.st_mode, before.st_uid, before.st_nlink, before.st_size)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != 0
            or before.st_nlink != 1
            or stat.S_IMODE(before.st_mode) != 0o600
            or before.st_size < 0
        ):
            die(f"input_metadata:{path.name}")
        cap = exact if exact is not None else limit
        if cap is not None and before.st_size > cap:
            die(f"input_length:{path.name}")
        while True:
            chunk = os.read(fd, 65536)
            if not chunk:
                break
            data.extend(chunk)
            if cap is not None and len(data) > cap:
                data[:] = b"\x00" * len(data)
                die(f"input_length:{path.name}")
        after = os.fstat(fd)
        after_identity = (after.st_dev, after.st_ino, after.st_mode, after.st_uid, after.st_nlink, after.st_size)
        if after_identity != identity:
            data[:] = b"\x00" * len(data)
            die(f"input_identity_changed:{path.name}")
        if len(data) != before.st_size or (exact is not None and len(data) != exact):
            data[:] = b"\x00" * len(data)
            die(f"input_length:{path.name}")
        return data
    except BaseException:
        data[:] = b"\x00" * len(data)
        raise
    finally:
        os.close(fd)


def read_public_regular(path: Path) -> bytearray:
    flags = (
        os.O_RDONLY
        | getattr(os, "O_CLOEXEC", 0)
        | getattr(os, "O_NOFOLLOW", 0)
        | getattr(os, "O_NONBLOCK", 0)
    )
    try:
        fd = os.open(path, flags)
    except OSError:
        die(f"scan_file_open:{path.name}")
    data = bytearray()
    try:
        before = os.fstat(fd)
        identity = (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_uid,
            before.st_nlink,
            before.st_size,
        )
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != 0
            or before.st_nlink != 1
            or stat.S_IMODE(before.st_mode) != 0o600
            or before.st_size < 0
            or before.st_size > PUBLIC_CAPTURE_LIMIT
        ):
            die(f"scan_file_metadata:{path.name}")
        while True:
            chunk = os.read(fd, 65536)
            if not chunk:
                break
            data.extend(chunk)
            if len(data) > PUBLIC_CAPTURE_LIMIT:
                data[:] = b"\x00" * len(data)
                die(f"scan_file_length:{path.name}")
        after = os.fstat(fd)
        after_identity = (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_uid,
            after.st_nlink,
            after.st_size,
        )
        if after_identity != identity or len(data) != before.st_size:
            data[:] = b"\x00" * len(data)
            die(f"scan_file_identity_changed:{path.name}")
        return data
    except BaseException:
        data[:] = b"\x00" * len(data)
        raise
    finally:
        os.close(fd)


def artifact_hmac(key: bytes | bytearray, artifact: bytes) -> str:
    # Go evidencecodec.ArtifactHMACMessage: domain, u16 key-id, u16 format,
    # u64 artifact length; all integers are unsigned big endian.
    message = HMAC_VERSION.encode() + b"\x00"
    for value in (ARTIFACT_KEY_ID.encode(), ARTIFACT_FORMAT.encode()):
        message += struct.pack(">H", len(value)) + value
    message += struct.pack(">Q", len(artifact)) + artifact
    return hmac.new(key, message, hashlib.sha256).hexdigest()


def strict_golden_counts(artifact: bytes) -> OrderedDict[str, int]:
    try:
        document = json.loads(artifact)
    except Exception:
        die("artifact_json")
    if not artifact.endswith(b"\n") or artifact.endswith(b"\n\n") or not isinstance(document, dict):
        die("artifact_wire")
    if list(document) != ["format", "source_sha256", "input_rows", "output_rows", "records"]:
        die("artifact_fields")
    records = document.get("records")
    if document.get("format") != ARTIFACT_FORMAT or not isinstance(records, list):
        die("artifact_format")
    counts: OrderedDict[str, int] = OrderedDict(
        (name, 0) for name in ("provable", "orphan", "cross_tenant", "unprovable_key")
    )
    record_fields = ["tenant_id", "id", "join_provenance", "key_algorithm", "classification", "reason", "source_sha256", "fingerprint_sha256"]
    for record in records:
        if not isinstance(record, dict) or list(record) != record_fields:
            die("artifact_record_fields")
        classification = record.get("classification")
        if classification not in counts:
            die("artifact_classification")
        counts[classification] += 1
    if document.get("input_rows") != len(records) or document.get("output_rows") != len(records):
        die("artifact_counts")
    return counts


def write_exclusive_0600(path: Path, data: bytes) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    try:
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                die(f"output_write:{path.name}")
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)


def generate(args: argparse.Namespace) -> int:
    if os.name != "posix" or os.geteuid() != 0:
        die("linux_root_required")
    out_dir = Path(args.out_dir)
    st = out_dir.stat(follow_symlinks=False)
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or stat.S_IMODE(st.st_mode) != 0o700:
        die("output_directory_metadata")
    source_buffer = bytearray()
    artifact_buffer = bytearray()
    artifact_key = bytearray()
    evidence_key = bytearray()
    try:
        source_buffer = read_regular_0600(Path(args.source), exact=784)
        artifact_buffer = read_regular_0600(Path(args.artifact), exact=1530)
        artifact_key = read_regular_0600(Path(args.artifact_key), exact=32)
        evidence_key = read_regular_0600(Path(args.evidence_key), exact=32)
        source = bytes(source_buffer)
        artifact = bytes(artifact_buffer)
        if hmac.compare_digest(artifact_key, evidence_key):
            die("key_material_alias")
        if len(source) != 784 or sha256(source) != GOLDEN_SOURCE_SHA:
            die("golden_source_identity")
        if len(artifact) != 1530 or sha256(artifact) != GOLDEN_ARTIFACT_SHA:
            die("golden_artifact_identity")
        counts = strict_golden_counts(artifact)
        digest = artifact_hmac(artifact_key, artifact)
        if digest != GOLDEN_HMAC:
            die("golden_hmac_identity")

        common = OrderedDict([
            ("source_format", "ndjson"), ("source_length", len(source)),
            ("source_sha256", sha256(source)), ("artifact_format", ARTIFACT_FORMAT),
            ("artifact_length", len(artifact)), ("artifact_sha256", sha256(artifact)),
            ("artifact_hmac_version", HMAC_VERSION), ("artifact_hmac_key_id", ARTIFACT_KEY_ID),
            ("artifact_hmac_sha256", digest), ("input_rows", sum(counts.values())),
            ("output_rows", sum(counts.values())), ("provable", counts["provable"]),
            ("orphan", counts["orphan"]), ("cross_tenant", counts["cross_tenant"]),
            ("unprovable_key", counts["unprovable_key"]),
        ])
        detached = canonical(OrderedDict([
            ("manifest_format", DETACHED_FORMAT), ("artifact_hmac_version", HMAC_VERSION),
            ("artifact_hmac_key_id", ARTIFACT_KEY_ID), ("artifact_format", ARTIFACT_FORMAT),
            *[(k, common[k]) for k in ("source_format", "source_length", "artifact_length", "source_sha256", "artifact_sha256", "artifact_hmac_sha256", "input_rows", "output_rows", "provable", "orphan", "cross_tenant", "unprovable_key")],
        ]))
        expectations = canonical(OrderedDict([
            ("expectations_format", EXPECTATIONS_FORMAT), ("release_id", RELEASE_ID),
            ("contract_sha256", args.contract_sha), ("classifier_sha256", SYNTHETIC_CLASSIFIER_SHA),
            ("verifier_sha256", args.verifier_sha), ("rule_version", RULE_VERSION),
            *[(k, common[k]) for k in ("source_format", "source_length", "source_sha256", "artifact_format", "artifact_length", "artifact_sha256", "artifact_hmac_version", "artifact_hmac_key_id", "artifact_hmac_sha256")],
            ("evidence_hmac_key_id", EVIDENCE_KEY_ID),
            *[(k, common[k]) for k in ("input_rows", "output_rows", "provable", "orphan", "cross_tenant", "unprovable_key")],
            ("detached_manifest_format", DETACHED_FORMAT), ("receipt_format", RECEIPT_FORMAT),
        ]))
        receipt = canonical(OrderedDict([
            ("receipt_format", RECEIPT_FORMAT), ("decision", "VERIFIED"),
            ("release_id", RELEASE_ID), ("contract_sha256", args.contract_sha),
            ("classifier_sha256", SYNTHETIC_CLASSIFIER_SHA), ("verifier_sha256", args.verifier_sha),
            ("rule_version", RULE_VERSION),
            *[(k, common[k]) for k in ("source_format", "source_length", "source_sha256", "artifact_format", "artifact_length", "artifact_sha256", "artifact_hmac_version", "artifact_hmac_key_id", "artifact_hmac_sha256")],
            ("evidence_hmac_key_id", EVIDENCE_KEY_ID),
            *[(k, common[k]) for k in ("input_rows", "output_rows", "provable", "orphan", "cross_tenant", "unprovable_key")],
            ("detached_manifest_sha256", sha256(detached)),
            ("release_expectations_sha256", sha256(expectations)),
        ]))
        for name, payload in (("detached.json", detached), ("expectations.json", expectations), ("expected-receipt.json", receipt)):
            write_exclusive_0600(out_dir / name, payload)
        print("gate_generator=PASS authorization=NONE identity=NON_AUTHORIZING_SYNTHETIC")
        return 0
    finally:
        source_buffer[:] = b"\x00" * len(source_buffer)
        artifact_buffer[:] = b"\x00" * len(artifact_buffer)
        artifact_key[:] = b"\x00" * len(artifact_key)
        evidence_key[:] = b"\x00" * len(evidence_key)


def encoded_forms(key: bytearray) -> list[bytearray]:
    raw = bytes(key)
    lower = raw.hex().encode("ascii")
    return [
        bytearray(raw),
        bytearray(lower),
        bytearray(lower.upper()),
        bytearray(base64.b64encode(raw)),
        bytearray(base64.b64encode(raw).rstrip(b"=")),
        bytearray(base64.urlsafe_b64encode(raw)),
        bytearray(base64.urlsafe_b64encode(raw).rstrip(b"=")),
    ]


def scan_public(args: argparse.Namespace) -> int:
    keys: list[bytearray] = []
    forms: list[bytearray] = []
    try:
        keys.append(read_regular_0600(Path(args.artifact_key), exact=32))
        keys.append(read_regular_0600(Path(args.evidence_key), exact=32))
        for key in keys:
            forms.extend(encoded_forms(key))
        for raw_path in args.path:
            root = Path(raw_path)
            st = root.stat(follow_symlinks=False)
            if (
                not stat.S_ISDIR(st.st_mode)
                or st.st_uid != 0
                or stat.S_IMODE(st.st_mode) != 0o700
            ):
                die("scan_directory_metadata")
            for current, directories, filenames in os.walk(root, topdown=True, followlinks=False):
                current_path = Path(current)
                current_st = current_path.stat(follow_symlinks=False)
                if (
                    not stat.S_ISDIR(current_st.st_mode)
                    or current_st.st_uid != 0
                    or stat.S_IMODE(current_st.st_mode) != 0o700
                ):
                    die("scan_directory_metadata")
                for directory in sorted(directories):
                    directory_path = current_path / directory
                    directory_st = directory_path.stat(follow_symlinks=False)
                    if (
                        not stat.S_ISDIR(directory_st.st_mode)
                        or directory_st.st_uid != 0
                        or stat.S_IMODE(directory_st.st_mode) != 0o700
                    ):
                        die(f"scan_directory_entry:{directory}")
                for filename in sorted(filenames):
                    path = current_path / filename
                    data = read_public_regular(path)
                    try:
                        if any(form in data for form in forms):
                            die(f"secret_material_in_public_capture:{path.name}")
                    finally:
                        data[:] = b"\x00" * len(data)
        print("gate_secret_scan=PASS forms=14")
        return 0
    finally:
        for form in forms:
            form[:] = b"\x00" * len(form)
        for key in keys:
            key[:] = b"\x00" * len(key)


def main() -> int:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    sub = parser.add_subparsers(dest="command", required=True)
    build = sub.add_parser("generate", allow_abbrev=False)
    for name in ("source", "artifact", "artifact-key", "evidence-key", "out-dir"):
        build.add_argument("--" + name, required=True)
    build.add_argument("--contract-sha", required=True, choices=["78fd468074f8ad528c2e406ae9fc83c8ae1f2da8c36b81eb386e8eeee38595c4"])
    build.add_argument("--verifier-sha", required=True)
    scan = sub.add_parser("scan-public", allow_abbrev=False)
    scan.add_argument("--artifact-key", required=True)
    scan.add_argument("--evidence-key", required=True)
    scan.add_argument("--path", required=True, action="append")
    args = parser.parse_args()
    if args.command == "generate":
        if len(args.verifier_sha) != 64 or any(c not in "0123456789abcdef" for c in args.verifier_sha):
            die("verifier_sha_syntax")
        return generate(args)
    if args.command == "scan-public":
        return scan_public(args)
    die("command")


if __name__ == "__main__":
    sys.exit(main())
