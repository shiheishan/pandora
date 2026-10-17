#!/usr/bin/env python3
"""节点端性能闸门采集：pdnd 的 cgroup、进程与 /proc/net/snmp 快照。由 remote.sh 以 root 在节点机上调用。

子命令：
  snap --unit U
      → stdout 一行 JSON（§7 字段）
  sample-start --unit U --out F --pidfile P [--interval 1.0]
      前台循环采样（remote.sh 用 setsid 后台起）；启动时把本进程 pid 写入 pidfile
  sample-stop --pidfile P
      对 pidfile 中的进程发 SIGTERM，至多等 5 秒

字段来源（§7）：
  cpu_usec、mem_current：unit 的 cgroup（systemctl ControlGroup → cpu.stat usage_usec、memory.current）
  pid、rss_kb、threads、fds：MainPID 与 /proc/<pid>/status、/proc/<pid>/fd
  tcp_*、udp_*：/proc/<pid>/net/snmp 的 Tcp/Udp 计数
  t：time.time()
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any, Dict


def _run_show(unit: str) -> Dict[str, str]:
    """读取 ControlGroup、MainPID。按 KEY=VALUE 解析（systemctl show 多个 -p 的输出顺序不保证与参数一致）。"""
    out: Dict[str, str] = {}
    try:
        r = subprocess.run(
            ["systemctl", "show", "-p", "ControlGroup", "-p", "MainPID", unit],
            capture_output=True,
            text=True,
            check=False,
        )
        if r.returncode != 0:
            return out
        for ln in r.stdout.splitlines():
            k, sep, v = ln.partition("=")
            if sep:
                out[k.strip()] = v.strip()
    except OSError:
        pass
    return out


def cgroup_dir(unit: str) -> Path | None:
    cg = _run_show(unit).get("ControlGroup", "")
    if not cg:
        return None
    return Path("/sys/fs/cgroup" + cg)


def read_int_file(p: Path) -> int:
    try:
        return int(p.read_text().strip())
    except (OSError, ValueError):
        return 0


def read_kv_line_file(p: Path, key: str) -> int:
    try:
        for line in p.read_text().splitlines():
            k, _, v = line.partition(" ")
            if k == key:
                try:
                    return int(v.strip())
                except ValueError:
                    return 0
    except OSError:
        pass
    return 0


def parse_proc_status(pid: int) -> tuple[int, int]:
    """rss_kb, threads"""
    rss_kb = 0
    threads = 0
    try:
        for line in Path(f"/proc/{pid}/status").read_text().splitlines():
            if line.startswith("VmRSS:"):
                parts = line.split()
                if len(parts) >= 2:
                    rss_kb = int(parts[1])
            elif line.startswith("Threads:"):
                parts = line.split()
                if len(parts) >= 2:
                    threads = int(parts[1])
    except OSError:
        pass
    return rss_kb, threads


def count_fds(pid: int) -> int:
    try:
        return len(os.listdir(f"/proc/{pid}/fd"))
    except OSError:
        return 0


SNMP_FIELDS = {
    ("Tcp", "RetransSegs"): "tcp_retrans",
    ("Tcp", "OutSegs"): "tcp_out_segs",
    ("Udp", "InDatagrams"): "udp_in",
    ("Udp", "InErrors"): "udp_in_errors",
    ("Udp", "RcvbufErrors"): "udp_rcvbuf_errors",
}


def parse_snmp_text(text: str) -> Dict[str, int]:
    """/proc/net/snmp 每组两行：「Tcp: 名字…」与「Tcp: 数值…」，按列对齐取值。"""
    out = {v: 0 for v in SNMP_FIELDS.values()}
    lines = text.splitlines()
    for i in range(len(lines) - 1):
        names = lines[i].split()
        vals = lines[i + 1].split()
        if not names or not vals or names[0] != vals[0] or not names[0].endswith(":"):
            continue
        group = names[0][:-1]
        if not vals[1:2] or not vals[1].lstrip("-").isdigit():
            continue  # 这一行是数值行，下一行是下一组的表头
        for name, val in zip(names[1:], vals[1:]):
            key = SNMP_FIELDS.get((group, name))
            if key is not None:
                try:
                    out[key] = int(val)
                except ValueError:
                    pass
    return out


def parse_snmp(pid: int) -> Dict[str, int]:
    """Tcp RetransSegs/OutSegs；Udp InDatagrams/InErrors/RcvbufErrors（pdnd 所在网络命名空间的计数）。"""
    try:
        return parse_snmp_text(Path(f"/proc/{pid}/net/snmp").read_text())
    except OSError:
        return {v: 0 for v in SNMP_FIELDS.values()}


def snapshot(unit: str) -> Dict[str, Any]:
    show = _run_show(unit)
    pid = 0
    try:
        pid = int(show.get("MainPID", "0") or "0")
    except ValueError:
        pid = 0

    cpu_usec = 0
    mem_current = 0
    cg = cgroup_dir(unit)
    if cg is not None:
        cpu_usec = read_kv_line_file(cg / "cpu.stat", "usage_usec")
        mem_current = read_int_file(cg / "memory.current")

    rss_kb = 0
    threads = 0
    fds = 0
    snmp = {v: 0 for v in SNMP_FIELDS.values()}
    if pid > 0:
        rss_kb, threads = parse_proc_status(pid)
        fds = count_fds(pid)
        snmp = parse_snmp(pid)

    return {
        "t": time.time(),
        "pid": pid,
        "cpu_usec": cpu_usec,
        "mem_current": mem_current,
        "rss_kb": rss_kb,
        "threads": threads,
        "fds": fds,
        **snmp,
    }


def cmd_snap(args: argparse.Namespace) -> None:
    print(json.dumps(snapshot(args.unit)), flush=True)


def cmd_sample_start(args: argparse.Namespace) -> None:
    # remote.sh 已 setsid 后台起本进程；写 pidfile 后循环追加采样行
    Path(args.pidfile).write_text(str(os.getpid()))
    stop = False

    def on_term(*_: Any) -> None:
        nonlocal stop
        stop = True

    signal.signal(signal.SIGTERM, on_term)
    interval = float(args.interval)
    out_path = Path(args.out)
    with out_path.open("a", encoding="utf-8") as f:
        while not stop:
            f.write(json.dumps(snapshot(args.unit)) + "\n")
            f.flush()
            deadline = time.time() + interval
            while time.time() < deadline and not stop:
                time.sleep(0.05)


def cmd_sample_stop(args: argparse.Namespace) -> None:
    try:
        pid = int(Path(args.pidfile).read_text().strip())
    except (OSError, ValueError):
        return
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        Path(args.pidfile).unlink(missing_ok=True)
        return
    for _ in range(50):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.1)
    Path(args.pidfile).unlink(missing_ok=True)


def main() -> None:
    p = argparse.ArgumentParser(description="节点端性能闸门采集")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("snap", help="单次快照")
    s.add_argument("--unit", required=True)
    s.set_defaults(func=cmd_snap)

    s = sub.add_parser("sample-start", help="循环采样（前台）")
    s.add_argument("--unit", required=True)
    s.add_argument("--out", required=True)
    s.add_argument("--pidfile", required=True)
    s.add_argument("--interval", type=float, default=1.0)
    s.set_defaults(func=cmd_sample_start)

    s = sub.add_parser("sample-stop", help="停止采样")
    s.add_argument("--pidfile", required=True)
    s.set_defaults(func=cmd_sample_stop)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
