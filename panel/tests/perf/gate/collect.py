#!/usr/bin/env python3
"""性能闸门的机上采集：快照、内存采样、等 nodesim 连齐、汇总一次运行的结果。由 remote.sh 调用，以 root 运行。

子命令：
  snap --out F                         取一次快照：各服务 cgroup 的 CPU 与内存、PG 计数、serve/worker 的 metrics、Redis 计数
  sample-start --out F --pidfile P     后台每秒记一次各服务内存；sample-stop --pidfile P 停
  wait-connected --status F --want N --since T --timeout S
                                       等 nodesim 状态里 N 台都在 T（epoch 秒）之后连上；输出用时，超时退出 1
  nodes --status F                     nodesim 状态汇总：连上几台、收到的全量与增量名单数
  pull-saturated --k6 F --rate R       这一级拉取是否饱和（是则退出 0）
  meta …                               这次运行的元数据
  report --run D --access-stats P      汇总 D 下的快照、k6 结果、访问日志，输出 result.json

Redis 口令只从环境变量 REDISCLI_AUTH 来（remote.sh 设好），不进参数表、不写进任何输出。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import signal
import statistics
import subprocess
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional

UNITS = {
    "serve": "pandora-serve.service",
    "worker": "pandora-worker.service",
    "pg": "postgresql@18-main.service",
    "redis": "redis-server.service",
}
METRICS_URLS = {"serve": "http://127.0.0.1:9464/metrics", "worker": "http://127.0.0.1:9465/metrics"}
# 从 metrics 里留下的指标（前缀）：连接池、进程、Go 堆与 GC
METRIC_PREFIXES = ("go_sql_", "process_cpu_seconds_total", "process_resident_memory_bytes",
                   "go_memstats_heap_inuse_bytes", "go_goroutines", "go_gc_duration_seconds_count")
DEPLOY_ENV = "/etc/pandora/deploy.env"
PULL_ROUTE = "/:prefix/:token"


def kv(path: str, key: str) -> str:
    val = ""
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.rstrip("\r\n")
            if line.startswith(key + "="):
                val = line[len(key) + 1:]
    return val


def cgroup_dir(unit: str) -> Path:
    out = subprocess.run(["systemctl", "show", "-p", "ControlGroup", "--value", unit],
                         check=True, capture_output=True, text=True).stdout.strip()
    return Path("/sys/fs/cgroup" + out)


def read_kv_file(p: Path) -> Dict[str, int]:
    out: Dict[str, int] = {}
    try:
        for line in p.read_text().splitlines():
            k, _, v = line.partition(" ")
            if v.strip().lstrip("-").isdigit():
                out[k] = int(v)
    except FileNotFoundError:
        pass
    return out


def read_int(p: Path) -> Optional[int]:
    try:
        return int(p.read_text().strip())
    except (FileNotFoundError, ValueError):
        return None


def cgroup_snap(dirs: Dict[str, Path]) -> Dict[str, Any]:
    out = {}
    for name, d in dirs.items():
        cpu = read_kv_file(d / "cpu.stat")
        mem = read_kv_file(d / "memory.stat")
        out[name] = {
            "usage_usec": cpu.get("usage_usec", 0),
            "nr_throttled": cpu.get("nr_throttled", 0),
            "mem_current": read_int(d / "memory.current") or 0,
            "mem_anon": mem.get("anon", 0),
        }
    return out


def pg_snap() -> Dict[str, Any]:
    port = kv(DEPLOY_ENV, "POSTGRES_PORT")
    sql = """
SELECT json_build_object(
  'xact', xact_commit + xact_rollback, 'blks_read', blks_read, 'blks_hit', blks_hit,
  'tup_returned', tup_returned, 'tup_fetched', tup_fetched, 'tup_inserted', tup_inserted,
  'tup_updated', tup_updated, 'tup_deleted', tup_deleted, 'temp_bytes', temp_bytes,
  'sessions', sessions, 'numbackends', numbackends,
  'stmt_calls', (SELECT coalesce(sum(calls), 0) FROM pg_stat_statements WHERE dbid = d.datid),
  'stmt_ms', (SELECT coalesce(sum(total_exec_time), 0) FROM pg_stat_statements WHERE dbid = d.datid))
FROM pg_stat_database d WHERE datname = 'pandora'"""
    r = subprocess.run(["runuser", "-u", "postgres", "--", "psql", "-X", "-q", "-At", "-p", port, "-d", "pandora",
                        "-c", sql], capture_output=True, text=True, cwd="/")
    if r.returncode != 0 or not r.stdout.strip():
        return {"error": r.stderr.strip()[:200]}
    return json.loads(r.stdout.strip().splitlines()[-1])


_METRIC_RE = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+([^\s]+)")


def metrics_snap(url: str) -> Dict[str, float]:
    try:
        with urllib.request.urlopen(url, timeout=3) as resp:
            text = resp.read().decode("utf-8", "replace")
    except OSError as e:
        return {"error": str(e)[:200]}  # type: ignore[dict-item]
    out: Dict[str, float] = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = _METRIC_RE.match(line)
        if not m or not m.group(1).startswith(METRIC_PREFIXES):
            continue
        try:
            out[m.group(1) + (m.group(2) or "")] = float(m.group(3))
        except ValueError:
            pass
    return out


def redis_snap() -> Dict[str, Any]:
    host, port, db = os.environ.get("REDIS_HOST"), os.environ.get("REDIS_PORT"), os.environ.get("REDIS_DB", "0")
    if not host:
        return {"error": "没有 REDIS_HOST"}
    r = subprocess.run(["redis-cli", "--no-auth-warning", "-h", host, "-p", port or "6379", "-n", db, "INFO"],
                       capture_output=True, text=True)
    out: Dict[str, Any] = {}
    for line in r.stdout.splitlines():
        k, _, v = line.strip().partition(":")
        if k in ("total_commands_processed", "used_memory", "total_connections_received"):
            out[k] = int(v)
    return out


def cmd_snap(a: argparse.Namespace) -> None:
    dirs = {n: cgroup_dir(u) for n, u in UNITS.items()}
    dirs["system"] = Path("/sys/fs/cgroup/system.slice")
    snap = {
        "t": time.time(),
        "cgroup": cgroup_snap(dirs),
        "pg": pg_snap(),
        "metrics": {n: metrics_snap(u) for n, u in METRICS_URLS.items()},
        "redis": redis_snap(),
    }
    Path(a.out).write_text(json.dumps(snap))


#---- 内存采样

def cmd_sample_loop(a: argparse.Namespace) -> None:
    stop = False

    def on_term(*_: Any) -> None:
        nonlocal stop
        stop = True

    signal.signal(signal.SIGTERM, on_term)
    dirs: Dict[str, Path] = {}
    with open(a.out, "a", encoding="utf-8") as f:
        while not stop:
            if not dirs:
                try:
                    dirs = {n: cgroup_dir(u) for n, u in UNITS.items()}
                except subprocess.CalledProcessError:
                    dirs = {}
            row: Dict[str, Any] = {"t": time.time()}
            for n, d in dirs.items():
                mem = read_kv_file(d / "memory.stat")
                row[n] = {"cur": read_int(d / "memory.current") or 0, "anon": mem.get("anon", 0)}
            f.write(json.dumps(row) + "\n")
            f.flush()
            time.sleep(1)


def cmd_sample_start(a: argparse.Namespace) -> None:
    p = subprocess.Popen([sys.executable, "-I", os.path.abspath(__file__), "sample-loop", "--out", a.out],
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                         start_new_session=True)
    Path(a.pidfile).write_text(str(p.pid))


def cmd_sample_stop(a: argparse.Namespace) -> None:
    try:
        pid = int(Path(a.pidfile).read_text().strip())
    except (FileNotFoundError, ValueError):
        return
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    Path(a.pidfile).unlink(missing_ok=True)


#---- nodesim 状态

_TS_RE = re.compile(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?(Z|[+-]\d\d:\d\d)$")


def parse_ts(raw: Optional[str]) -> Optional[float]:
    """解析 Go 的 RFC3339Nano 时间（小数位可到 9 位）为 epoch 秒。"""
    if not raw:
        return None
    m = _TS_RE.match(raw)
    if not m:
        return None
    frac = (m.group(2) or ".0")[:7]
    tz = "+00:00" if m.group(3) == "Z" else m.group(3)
    return datetime.fromisoformat(m.group(1) + frac + tz).timestamp()


def load_status(path: str) -> Optional[Dict[str, Any]]:
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return None


def nodes_summary(st: Dict[str, Any]) -> Dict[str, Any]:
    servers = st.get("servers") or {}
    out = {"servers": len(servers), "enrolled": 0, "connected": 0, "fulls": 0, "deltas": 0,
           "errors": st.get("errors", 0), "connected_at_max": None}
    for srv in servers.values():
        if not srv:
            continue
        out["enrolled"] += 1 if srv.get("enrolled") else 0
        if srv.get("connected"):
            out["connected"] += 1
            t = parse_ts(srv.get("connected_at"))
            if t and (out["connected_at_max"] is None or t > out["connected_at_max"]):
                out["connected_at_max"] = t
        for node in (srv.get("nodes") or {}).values():
            out["fulls"] += node.get("fulls_received", 0)
            out["deltas"] += node.get("deltas_received", 0)
    return out


def cmd_nodes(a: argparse.Namespace) -> None:
    st = load_status(a.status)
    print(json.dumps(nodes_summary(st) if st else {"error": "读不到状态文件"}))


def cmd_wait_connected(a: argparse.Namespace) -> None:
    deadline = time.time() + a.timeout
    since = float(a.since)
    last: Dict[str, Any] = {}
    while time.time() < deadline:
        st = load_status(a.status)
        if st:
            n, latest = 0, None
            for srv in (st.get("servers") or {}).values():
                if not srv or not srv.get("connected"):
                    continue
                t = parse_ts(srv.get("connected_at"))
                if t is None or t < since:
                    continue
                n += 1
                latest = t if latest is None or t > latest else latest
            last = {"connected": n, "want": a.want}
            if n >= a.want and latest is not None:
                last["t_all"] = round(latest - since, 3) if since > 0 else None
                last["at"] = latest
                print(json.dumps(last))
                return
        time.sleep(0.5)
    last["timeout"] = True
    print(json.dumps(last))
    sys.exit(1)


#---- k6

def k6_trend(m: Dict[str, Any]) -> Dict[str, float]:
    """兼容 summary-export 的两种写法：平铺的 {avg, med, p(95)…}，或包在 values 里。"""
    v = m.get("values", m)
    return {"avg": v.get("avg"), "p50": v.get("med"), "p90": v.get("p(90)"), "p95": v.get("p(95)"),
            "p99": v.get("p(99)"), "max": v.get("max")}


def k6_summary(path: str) -> Dict[str, Any]:
    try:
        d = json.loads(Path(path).read_text())
    except (FileNotFoundError, json.JSONDecodeError):
        return {"error": "没有 k6 结果"}
    ms = d.get("metrics", {})

    def val(name: str, *keys: str) -> Optional[float]:
        m = ms.get(name)
        if not m:
            return None
        v = m.get("values", m)
        for k in keys:
            if k in v:
                return v[k]
        return None

    route = next((k for k in ms if k.startswith("http_req_duration{") and "name:" in k), None)
    return {
        "duration": k6_trend(ms.get(route or "http_req_duration", {})),
        "iterations": val("iterations", "count"),
        "iter_rate": val("iterations", "rate"),
        "dropped": val("dropped_iterations", "count") or 0,
        "failed_rate": val("http_req_failed", "rate", "value"),
        "reqs": val("http_reqs", "count"),
    }


def pull_saturated(k6: Dict[str, Any], rate: int, secs: int) -> bool:
    if "error" in k6:
        return True
    expected = rate * secs
    p95 = k6["duration"].get("p95") or 0
    return (p95 > 1000 or (k6.get("failed_rate") or 0) > 0.01 or (k6.get("dropped") or 0) > 0.01 * expected
            or (k6.get("iterations") or 0) < 0.95 * expected)


def cmd_pull_saturated(a: argparse.Namespace) -> None:
    sys.exit(0 if pull_saturated(k6_summary(a.k6), a.rate, a.secs) else 1)


#---- 元数据与汇总

def cmd_meta(a: argparse.Namespace) -> None:
    sha256 = subprocess.run(["sha256sum", a.binary], capture_output=True, text=True).stdout.split()[0]
    print(json.dumps({
        "label": a.label, "sha": a.sha, "binary_sha256": sha256, "servers": a.servers, "users": a.users,
        "started": float(a.started), "kernel": os.uname().release,
        "system_cpus": subprocess.run(["systemctl", "show", "system.slice", "-p", "AllowedCPUs", "--value"],
                                      capture_output=True, text=True).stdout.strip(),
    }))


def window_delta(s0: Dict[str, Any], s1: Dict[str, Any]) -> Dict[str, Any]:
    secs = s1["t"] - s0["t"]
    cpu_s, cpu_pct, mem_mb = {}, {}, {}
    for n in list(UNITS) + ["system"]:
        c0, c1 = s0["cgroup"].get(n, {}), s1["cgroup"].get(n, {})
        d = (c1.get("usage_usec", 0) - c0.get("usage_usec", 0)) / 1e6
        cpu_s[n] = round(d, 3)
        cpu_pct[n] = round(d / secs * 100, 2) if secs > 0 else None
        mem_mb[n] = round(c1.get("mem_anon", 0) / 2**20, 1)
    cpu_s["total"] = round(sum(cpu_s[n] for n in UNITS), 3)
    cpu_pct["total"] = round(cpu_s["total"] / secs * 100, 2) if secs > 0 else None
    p0, p1 = s0.get("pg", {}), s1.get("pg", {})
    pg = {k: p1[k] - p0[k] for k in ("xact", "blks_read", "blks_hit", "tup_returned", "tup_fetched", "tup_inserted",
                                     "tup_updated", "tup_deleted", "temp_bytes", "sessions", "stmt_calls")
          if isinstance(p1.get(k), (int, float)) and isinstance(p0.get(k), (int, float))}
    if isinstance(p1.get("stmt_ms"), (int, float)) and isinstance(p0.get("stmt_ms"), (int, float)):
        pg["stmt_ms"] = round(p1["stmt_ms"] - p0["stmt_ms"], 1)
    pg["numbackends"] = p1.get("numbackends")
    pools = {}
    for svc in METRICS_URLS:
        m0, m1 = s0["metrics"].get(svc, {}), s1["metrics"].get(svc, {})
        out = {}
        for short, name in (("max_idle_closed", "go_sql_max_idle_closed_total"),
                            ("idle_time_closed", "go_sql_max_idle_time_closed_total"),
                            ("lifetime_closed", "go_sql_max_lifetime_closed_total"),
                            ("wait_count", "go_sql_wait_count_total"),
                            ("wait_s", "go_sql_wait_duration_seconds_total")):
            key = next((k for k in m1 if k.startswith(name + "{") or k == name), None)
            if key is not None and key in m0:
                out[short] = round(m1[key] - m0[key], 4)
        key = next((k for k in m1 if k.startswith("go_sql_open_connections")), None)
        if key:
            out["open"] = m1[key]
        for short, name in (("rss_mb", "process_resident_memory_bytes"), ("heap_mb", "go_memstats_heap_inuse_bytes")):
            if name in m1:
                out[short] = round(m1[name] / 2**20, 1)
        if "go_goroutines" in m1:
            out["goroutines"] = m1["go_goroutines"]
        pools[svc] = out
    r0, r1 = s0.get("redis", {}), s1.get("redis", {})
    redis = {k: r1[k] - r0[k] for k in ("total_commands_processed", "total_connections_received")
             if k in r0 and k in r1}
    return {"secs": round(secs, 2), "cpu_s": cpu_s, "cpu_pct": cpu_pct, "mem_anon_mb": mem_mb, "pg": pg,
            "go": pools, "redis": redis}


def samples_stats(path: Path, t0: float, t1: float) -> Dict[str, Any]:
    vals: Dict[str, List[int]] = {}
    if not path.exists():
        return {}
    for line in path.read_text().splitlines():
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not t0 <= row.get("t", 0) <= t1:
            continue
        for n in UNITS:
            if n in row:
                vals.setdefault(n, []).append(row[n]["anon"])
    return {n: {"mean_mb": round(statistics.fmean(v) / 2**20, 1), "max_mb": round(max(v) / 2**20, 1)}
            for n, v in vals.items() if v}


def access(stats_py: str, log: Path, t0: float, t1: float, prefix: Optional[str]) -> Dict[str, Any]:
    iso = lambda t: datetime.fromtimestamp(t, timezone.utc).isoformat()  # noqa: E731
    cmd = [sys.executable, "-I", stats_py, str(log), "--from", iso(t0), "--to", iso(t1), "--json", "--top", "200"]
    if prefix:
        cmd += ["--route-prefix", prefix]
    r = subprocess.run(cmd, capture_output=True, text=True)
    try:
        rows = json.loads(r.stdout)
    except json.JSONDecodeError:
        return {}
    keep = ("n", "err_n", "p50", "p95", "p99", "max", "sql_avg", "sqlms_avg", "redis_avg")
    return {f"{x['method']} {x['route']}": {k: x.get(k) for k in keep} for x in rows}


def cmd_report(a: argparse.Namespace) -> None:
    run = Path(a.run)
    windows = {}
    for line in (run / "windows.jsonl").read_text().splitlines():
        w = json.loads(line)
        windows[w["name"]] = w

    def delta(name: str) -> Dict[str, Any]:
        s0 = json.loads((run / "snap" / f"{name}.0.json").read_text())
        s1 = json.loads((run / "snap" / f"{name}.1.json").read_text())
        return window_delta(s0, s1)

    log = run / "serve.log"
    samples = run / "samples.jsonl"
    res: Dict[str, Any] = {"meta": json.loads((run / "meta.json").read_text())}
    conn = run / "connect.json"
    res["connect"] = json.loads(conn.read_text()) if conn.exists() else {}

    if "idle" in windows:
        w = windows["idle"]
        d = delta("idle")
        mins = d["secs"] / 60
        res["idle"] = {
            "secs": d["secs"], "cpu_pct": d["cpu_pct"], "mem_anon_mb": d["mem_anon_mb"],
            "mem": samples_stats(samples, w["t0"], w["t1"]),
            "pg_new_sessions_per_min": round(d["pg"].get("sessions", 0) / mins, 2) if mins else None,
            "pg_xact_per_min": round(d["pg"].get("xact", 0) / mins, 1) if mins else None,
            "pg_stmt_per_min": round(d["pg"].get("stmt_calls", 0) / mins, 1) if mins else None,
            "pool": d["go"], "redis_cmds_per_min": round(d["redis"].get("total_commands_processed", 0) / mins, 1)
            if mins else None,
        }

    if "admin" in windows:
        w = windows["admin"]
        res["admin"] = {"routes": access(a.access_stats, log, w["t0"], w["t1"], "/v1/"),
                        "k6": k6_summary(str(run / "k6" / "admin.json")), "window": delta("admin")}

    pulls = []
    for name in sorted((n for n in windows if n.startswith("pull-")), key=lambda n: int(n.split("-")[1])):
        rate = int(name.split("-")[1])
        w = windows[name]
        d = delta(name)
        k6 = k6_summary(str(run / "k6" / f"{name}.json"))
        srv = access(a.access_stats, log, w["t0"], w["t1"], None).get(f"GET {PULL_ROUTE}", {})
        ok = (k6.get("iterations") or 0) * (1 - (k6.get("failed_rate") or 0))
        per_req = {n: round(d["cpu_s"][n] / ok * 1000, 3) if ok else None for n in ("serve", "pg", "redis", "total")}
        pulls.append({
            "rate": rate, "k6": k6, "server": srv, "cpu_ms_per_req": per_req, "cpu_pct": d["cpu_pct"],
            "pg_new_sessions": d["pg"].get("sessions"), "pg_stmt_per_req": round(d["pg"].get("stmt_calls", 0) / ok, 2)
            if ok else None, "pool": d["go"].get("serve", {}), "mem": samples_stats(samples, w["t0"], w["t1"]),
            "saturated": pull_saturated(k6, rate, int(a.pull_secs)),
        })
    res["pull"] = pulls
    ok_rates = [p["rate"] for p in pulls if not p["saturated"]]
    res["pull_max_ok_rate"] = max(ok_rates) if ok_rates else 0

    recs = []
    for name in sorted(n for n in windows if n.startswith("reconnect-")):
        k = name.split("-")[1]
        info = json.loads((run / f"reconnect-{k}.json").read_text())
        wait = json.loads((run / f"reconnect-{k}.wait.json").read_text() or "{}")
        before = json.loads((run / f"reconnect-{k}.before.json").read_text())
        after = json.loads((run / f"reconnect-{k}.after.json").read_text())
        d = delta(name)
        recs.append({
            "t_all": wait.get("t_all"), "t_ready": round(info["t_ready"] - info["t0"], 2),
            "align_busy": info["align_busy"], "connected": wait.get("connected"),
            "fulls": after.get("fulls", 0) - before.get("fulls", 0),
            "deltas": after.get("deltas", 0) - before.get("deltas", 0),
            "cpu_s": d["cpu_s"], "pg_stmt": d["pg"].get("stmt_calls"),
        })
    res["reconnect"] = recs
    ts = [r["t_all"] for r in recs if r["t_all"] is not None]
    res["reconnect_t_all_median"] = statistics.median(ts) if ts else None
    res["reconnect_align_busy_total"] = sum(r["align_busy"] for r in recs)
    notes = run / "notes.txt"
    res["notes"] = notes.read_text().splitlines() if notes.exists() else []
    print(json.dumps(res, ensure_ascii=False, indent=1))


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("snap"); p.add_argument("--out", required=True); p.set_defaults(fn=cmd_snap)
    p = sub.add_parser("sample-start"); p.add_argument("--out", required=True); p.add_argument("--pidfile", required=True)
    p.set_defaults(fn=cmd_sample_start)
    p = sub.add_parser("sample-loop"); p.add_argument("--out", required=True); p.set_defaults(fn=cmd_sample_loop)
    p = sub.add_parser("sample-stop"); p.add_argument("--pidfile", required=True); p.set_defaults(fn=cmd_sample_stop)
    p = sub.add_parser("wait-connected")
    p.add_argument("--status", required=True); p.add_argument("--want", type=int, required=True)
    p.add_argument("--since", required=True); p.add_argument("--timeout", type=float, required=True)
    p.set_defaults(fn=cmd_wait_connected)
    p = sub.add_parser("nodes"); p.add_argument("--status", required=True); p.set_defaults(fn=cmd_nodes)
    p = sub.add_parser("pull-saturated")
    p.add_argument("--k6", required=True); p.add_argument("--rate", type=int, required=True)
    p.add_argument("--secs", type=int, default=60); p.set_defaults(fn=cmd_pull_saturated)
    p = sub.add_parser("meta")
    for k in ("label", "sha", "binary", "servers", "users", "started"):
        p.add_argument("--" + k, required=True)
    p.set_defaults(fn=cmd_meta)
    p = sub.add_parser("report")
    p.add_argument("--run", required=True); p.add_argument("--access-stats", required=True)
    p.add_argument("--pull-secs", default="60"); p.set_defaults(fn=cmd_report)
    a = ap.parse_args()
    a.fn(a)


if __name__ == "__main__":
    main()
