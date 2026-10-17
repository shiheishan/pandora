#!/usr/bin/env python3
"""对比两次闸门运行（基线 vs 候选），按指标给出判定。本机运行，读 gate.sh fetch 取回的 result.json。

用法：
  compare.py [--root 目录] <基线> <候选> [--aa <基线第二遍>] [--target <指标前缀>] [--json]

  <基线>、<候选> 是结果目录名（相对 --root）或目录路径。
  --aa      同一提交的第二遍运行：两遍基线之差当噪声，阈值取「缺省阈值」与「2 倍噪声」中大的那个。
  --target  这次优化要改善的指标（键的前缀，如 pull.、reconnect.、admin.timeseries）：没有一项改善就判「未达预期」。

判定（指标都是越小越好，除非标了「越大越好」）：
  改善 / 持平 / 退化：相对变化超出阈值才算改善或退化。
  不能判：样本数不够（p50 至少 100 个、p99 至少 1000 个），或任一边没有数。
总评：有退化 → 退回；有 --target 且没有改善 → 未达预期；否则 → 可合（列出改善项）。
阈值沿用 P8 前的闸门规则：延迟 p50 +5%、p95/p99 +10%、内存 +10%、每请求 CPU +5%。

判定与输出的函数（judge、verdict、run）也被 pdnd/tests/perf/gate/compare.py 按路径加载复用，改它们要两边一起看。
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

# (键, 中文名, 单位, 越大越好, 缺省相对阈值, 最少样本数, 绝对容差)
# 绝对容差：两边都在它以内时直接算持平（计数类指标在 0 附近相对变化没有意义）
Metric = Tuple[str, str, str, bool, float, int, float]


def metrics_for(base: Dict[str, Any], cand: Dict[str, Any]) -> List[Metric]:
    ms: List[Metric] = [
        ("idle.cpu.total", "静默 CPU 合计（单核 %）", "%", False, 0.10, 0, 0.5),
        ("idle.cpu.serve", "静默 CPU serve", "%", False, 0.10, 0, 0.3),
        ("idle.cpu.pg", "静默 CPU PostgreSQL", "%", False, 0.10, 0, 0.3),
        ("idle.cpu.worker", "静默 CPU worker", "%", False, 0.10, 0, 0.3),
        ("idle.cpu.redis", "静默 CPU Redis", "%", False, 0.10, 0, 0.3),
        ("idle.mem.serve", "静默 serve 内存（anon 均值 MB）", "MB", False, 0.10, 0, 5),
        ("idle.mem.pg", "静默 PostgreSQL 内存（anon 均值 MB）", "MB", False, 0.10, 0, 5),
        ("idle.pg_new_sessions_per_min", "静默每分钟新建 PG 连接", "", False, 0.20, 0, 1),
        ("idle.pg_stmt_per_min", "静默每分钟 SQL 条数", "", False, 0.10, 0, 5),
        ("admin.timeseries.p50", "后台行为趋势 p50", "ms", False, 0.05, 100, 1),
        ("admin.timeseries.p95", "后台行为趋势 p95", "ms", False, 0.10, 100, 2),
        ("admin.timeseries.sql", "后台行为趋势每请求 SQL", "", False, 0.0, 0, 0.5),
        ("admin.other.p50", "后台其余列表 p50（中位）", "ms", False, 0.05, 100, 1),
        ("pull.max_ok_rate", "订阅拉取最高不饱和速率", "/s", True, 0.0, 0, 0),
    ]
    rates = sorted({p["rate"] for p in base.get("pull", [])} & {p["rate"] for p in cand.get("pull", [])})
    for r in rates:
        ms += [
            (f"pull.{r}.p50", f"拉取 {r}/s 服务端 p50", "ms", False, 0.05, 100, 1),
            (f"pull.{r}.p95", f"拉取 {r}/s 服务端 p95", "ms", False, 0.10, 100, 2),
            (f"pull.{r}.p99", f"拉取 {r}/s 服务端 p99", "ms", False, 0.10, 1000, 2),
            (f"pull.{r}.cpu_ms", f"拉取 {r}/s 每请求 CPU（四个服务）", "ms", False, 0.05, 0, 0.05),
            (f"pull.{r}.pg_cpu_ms", f"拉取 {r}/s 每请求 PG CPU", "ms", False, 0.05, 0, 0.05),
            (f"pull.{r}.sql", f"拉取 {r}/s 每请求 SQL", "", False, 0.0, 0, 0.2),
            (f"pull.{r}.pg_sessions", f"拉取 {r}/s 新建 PG 连接", "", False, 0.20, 0, 2),
            (f"pull.{r}.mem_max", f"拉取 {r}/s serve 内存峰值", "MB", False, 0.10, 0, 5),
        ]
    ms += [
        ("reconnect.t_all", "重启后全部连回（中位数）", "s", False, 0.10, 0, 0.5),
        ("reconnect.align_busy", "重启重连的建流对齐超时（合计）", "次", False, 0.0, 0, 0),
        ("reconnect.pg_stmt", "重启重连的 SQL 条数（中位数）", "", False, 0.10, 0, 20),
        ("connect.t_all", "冷启动全部连上", "s", False, 0.10, 0, 0.5),
    ]
    return ms


def median(xs: List[float]) -> Optional[float]:
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        return None
    n = len(xs)
    return xs[n // 2] if n % 2 else (xs[n // 2 - 1] + xs[n // 2]) / 2


def lookup(res: Dict[str, Any], key: str) -> Tuple[Optional[float], Optional[int]]:
    """取一个指标的值与样本数（样本数不适用时为 None）。"""
    parts = key.split(".")
    if parts[0] == "idle":
        idle = res.get("idle") or {}
        if parts[1] == "cpu":
            return (idle.get("cpu_pct") or {}).get(parts[2]), None
        if parts[1] == "mem":
            return ((idle.get("mem") or {}).get(parts[2]) or {}).get("mean_mb"), None
        return idle.get(parts[1]), None
    if parts[0] == "admin":
        routes = (res.get("admin") or {}).get("routes") or {}
        if parts[1] == "timeseries":
            r = routes.get("GET /v1/stats/timeseries") or {}
            field = {"p50": "p50", "p95": "p95", "sql": "sql_avg"}[parts[2]]
            return r.get(field), r.get("n")
        others = [r for k, r in routes.items() if k != "GET /v1/stats/timeseries" and r.get("n")]
        return median([r["p50"] for r in others]), sum(r["n"] for r in others) or None
    if parts[0] == "pull":
        if parts[1] == "max_ok_rate":
            return res.get("pull_max_ok_rate"), None
        p = next((x for x in res.get("pull", []) if x["rate"] == int(parts[1])), None)
        if not p:
            return None, None
        srv = p.get("server") or {}
        f = parts[2]
        if f in ("p50", "p95", "p99"):
            return srv.get(f), srv.get("n")
        if f == "cpu_ms":
            return (p.get("cpu_ms_per_req") or {}).get("total"), None
        if f == "pg_cpu_ms":
            return (p.get("cpu_ms_per_req") or {}).get("pg"), None
        if f == "sql":
            return srv.get("sql_avg"), srv.get("n")
        if f == "pg_sessions":
            return p.get("pg_new_sessions"), None
        if f == "mem_max":
            return ((p.get("mem") or {}).get("serve") or {}).get("max_mb"), None
    if parts[0] == "reconnect":
        if parts[1] == "t_all":
            return res.get("reconnect_t_all_median"), None
        if parts[1] == "align_busy":
            return res.get("reconnect_align_busy_total"), None
        if parts[1] == "pg_stmt":
            return median([r.get("pg_stmt") for r in res.get("reconnect", [])]), None
    if parts[0] == "connect":
        return (res.get("connect") or {}).get("t_all"), None
    return None, None


def load(root: Path, name: str) -> Dict[str, Any]:
    p = Path(name)
    if not p.exists():
        p = root / name
    if p.is_dir():
        p = p / "result.json"
    return json.loads(p.read_text())


def rel(a: float, b: float) -> Optional[float]:
    if a == 0:
        return None if b == 0 else float("inf")
    return (b - a) / abs(a)


def judge(
    m: Metric,
    base: Dict[str, Any],
    cand: Dict[str, Any],
    aa: Optional[Dict[str, Any]],
    lookup_fn: Optional[Any] = None,
) -> Dict[str, Any]:
    lookup_fn = lookup_fn or lookup
    key, label, unit, higher, thr, min_n, abs_tol = m
    b, bn = lookup_fn(base, key)
    c, cn = lookup_fn(cand, key)
    row: Dict[str, Any] = {"key": key, "label": label, "unit": unit, "base": b, "cand": c}
    if b is None or c is None:
        row["verdict"] = "不能判"
        row["why"] = "缺数据"
        return row
    if min_n and ((bn or 0) < min_n or (cn or 0) < min_n):
        row["verdict"] = "不能判"
        row["why"] = f"样本 {bn}/{cn} < {min_n}"
        return row
    noise = None
    if aa is not None:
        a2, _ = lookup_fn(aa, key)
        if a2 is not None:
            r = rel(b, a2)
            noise = abs(r) if r is not None and r != float("inf") else None
    eff = max(thr, 2 * noise) if noise is not None else thr
    row["noise"] = noise
    row["threshold"] = eff
    diff = c - b
    if abs(diff) <= abs_tol:
        row["change"] = rel(b, c)
        row["verdict"] = "持平"
        return row
    r = rel(b, c)
    row["change"] = r
    worse = (r if not higher else -r) if r is not None else 0
    if worse > eff:
        row["verdict"] = "退化"
    elif worse < -eff:
        row["verdict"] = "改善"
    else:
        row["verdict"] = "持平"
    if noise is not None and noise > 3 * max(thr, 0.01):
        row["why"] = "噪声大"
    return row


def fmt(v: Optional[float]) -> str:
    if v is None:
        return "—"
    if isinstance(v, float):
        return f"{v:.3g}" if abs(v) < 100 else f"{v:.0f}"
    return str(v)


def fmt_change(r: Optional[float]) -> str:
    if r is None:
        return "—"
    if r == float("inf"):
        return "+∞"
    return f"{r * 100:+.1f}%"


def verdict(rows: List[Dict[str, Any]], targets: List[str]) -> Tuple[str, List[Dict[str, Any]], List[Dict[str, Any]]]:
    worse = [r for r in rows if r["verdict"] == "退化"]
    better = [r for r in rows if r["verdict"] == "改善"]
    target_hit = [r for r in better if any(r["key"].startswith(t) for t in targets)]
    if worse:
        overall = "退回"
    elif targets and not target_hit:
        overall = "未达预期"
    else:
        overall = "可合"
    return overall, worse, better


def run(argv: Optional[List[str]], metrics_fn: Any, lookup_fn: Any, description: Optional[str]) -> None:
    ap = argparse.ArgumentParser(description=description, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("base")
    ap.add_argument("cand")
    ap.add_argument("--root", default=".")
    ap.add_argument("--aa")
    ap.add_argument("--target", action="append", default=[])
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args(argv)
    root = Path(a.root)
    base, cand = load(root, a.base), load(root, a.cand)
    aa = load(root, a.aa) if a.aa else None
    rows = [judge(m, base, cand, aa, lookup_fn) for m in metrics_fn(base, cand)]

    overall, worse, better = verdict(rows, a.target)
    if a.json:
        print(json.dumps({"overall": overall, "rows": rows}, ensure_ascii=False, indent=1))
        return
    bm, cm = base.get("meta", {}), cand.get("meta", {})
    print(f"基线 {bm.get('label')}（{bm.get('sha')}） vs 候选 {cm.get('label')}（{cm.get('sha')}）"
          + (f"；噪声取自 {aa.get('meta', {}).get('label')}" if aa else "；没给 --aa，只用缺省阈值"))
    print()
    print("| 指标 | 基线 | 候选 | 变化 | 阈值 | 判定 |")
    print("|---|---|---|---|---|---|")
    for r in rows:
        unit = f" {r['unit']}" if r["unit"] else ""
        thr = f"±{r['threshold'] * 100:.0f}%" if r.get("threshold") is not None else "—"
        why = f"（{r['why']}）" if r.get("why") else ""
        print(f"| {r['label']} | {fmt(r['base'])}{unit} | {fmt(r['cand'])}{unit} | {fmt_change(r.get('change'))} "
              f"| {thr} | {r['verdict']}{why} |")
    print()
    print(f"总评：{overall}。改善 {len(better)} 项、退化 {len(worse)} 项、"
          f"不能判 {sum(r['verdict'] == '不能判' for r in rows)} 项。")
    if worse:
        print("退化项：" + "、".join(r["label"] for r in worse))
    notes = (base.get("notes") or []) + (cand.get("notes") or [])
    if notes:
        print("运行备注：" + "；".join(notes))
    sys.exit(1 if worse else 0)


def main() -> None:
    run(None, metrics_for, lookup, __doc__)


if __name__ == "__main__":
    main()
