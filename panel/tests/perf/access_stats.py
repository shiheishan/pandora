#!/usr/bin/env python3
"""按路由聚合 pandora serve 访问日志（msg=access 的 JSON 行）。"""

from __future__ import annotations

import argparse
import gzip
import json
import math
import sys
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Dict, Iterable, List, Optional, TextIO, Tuple


def parse_ts(raw: str) -> Optional[datetime]:
    """解析日志 ts，兼容 +0800 与 +08:00。"""
    if not raw:
        return None
    try:
        return datetime.strptime(raw, "%Y-%m-%dT%H:%M:%S.%f%z")
    except ValueError:
        pass
    try:
        return datetime.fromisoformat(raw)
    except ValueError:
        return None


def parse_bound(raw: str) -> datetime:
    dt = datetime.fromisoformat(raw)
    if dt.tzinfo is None:
        raise SystemExit(f"--from/--to 必须带时区: {raw}")
    return dt


def open_log(path: str) -> TextIO:
    if path == "-":
        return sys.stdin
    if path.endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8")
    return open(path, "r", encoding="utf-8")


def quantile_nearest_rank(values: List[float], p: float) -> float:
    if not values:
        return 0.0
    xs = sorted(values)
    n = len(xs)
    idx = int(math.ceil(p * n) - 1)
    if idx < 0:
        idx = 0
    if idx >= n:
        idx = n - 1
    return xs[idx]


@dataclass
class RouteAgg:
    n: int = 0
    err_n: int = 0
    dur_ms: List[float] = field(default_factory=list)
    sql_n_vals: List[float] = field(default_factory=list)
    sql_ms_vals: List[float] = field(default_factory=list)
    redis_n_vals: List[float] = field(default_factory=list)
    missing_obs: int = 0

    def add(self, row: Dict[str, Any]) -> None:
        self.n += 1
        status = int(row.get("status") or 0)
        if status >= 500:
            self.err_n += 1
        self.dur_ms.append(float(row.get("dur_ms") or 0))
        if "sql_n" not in row:
            self.missing_obs += 1
            sql_n = 0.0
        else:
            sql_n = float(row["sql_n"])
        self.sql_n_vals.append(sql_n)
        self.sql_ms_vals.append(float(row.get("sql_ms") or 0))
        self.redis_n_vals.append(float(row.get("redis_n") or 0))


def mean(vals: List[float]) -> float:
    if not vals:
        return 0.0
    return sum(vals) / len(vals)


def max_val(vals: List[float]) -> float:
    return max(vals) if vals else 0.0


def iter_access_rows(
    paths: Iterable[str],
    t_from: Optional[datetime],
    t_to: Optional[datetime],
    route_prefix: Optional[str],
) -> Tuple[Iterable[Tuple[str, str, Dict[str, Any]]], int]:
    """yield (method, route, row)；返回全局 missing_obs 计数在聚合里按路由累加，此处只过滤。"""

    def gen() -> Iterable[Tuple[str, str, Dict[str, Any]]]:
        for path in paths:
            with open_log(path) as fh:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        row = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if row.get("msg") != "access":
                        continue
                    ts = parse_ts(str(row.get("ts") or ""))
                    if ts is None:
                        continue
                    if t_from is not None and ts < t_from:
                        continue
                    if t_to is not None and ts >= t_to:
                        continue
                    route = str(row.get("route") or "")
                    if route_prefix is not None and not route.startswith(route_prefix):
                        continue
                    method = str(row.get("method") or "")
                    yield method, route, row

    return gen(), 0


def build_stats(
    paths: List[str],
    t_from: Optional[datetime],
    t_to: Optional[datetime],
    route_prefix: Optional[str],
) -> Dict[Tuple[str, str], RouteAgg]:
    groups: Dict[Tuple[str, str], RouteAgg] = defaultdict(RouteAgg)
    stream, _ = iter_access_rows(paths, t_from, t_to, route_prefix)
    for method, route, row in stream:
        key = (method, route)
        groups[key].add(row)
    return groups


def row_dict(method: str, route: str, agg: RouteAgg) -> Dict[str, Any]:
    return {
        "method": method,
        "route": route,
        "n": agg.n,
        "err_n": agg.err_n,
        "p50": quantile_nearest_rank(agg.dur_ms, 0.50),
        "p95": quantile_nearest_rank(agg.dur_ms, 0.95),
        "p99": quantile_nearest_rank(agg.dur_ms, 0.99),
        "max": max_val(agg.dur_ms),
        "sql_avg": mean(agg.sql_n_vals),
        "sql_max": max_val(agg.sql_n_vals),
        "sqlms_avg": mean(agg.sql_ms_vals),
        "redis_avg": mean(agg.redis_n_vals),
        "redis_max": max_val(agg.redis_n_vals),
        "missing_obs": agg.missing_obs,
    }


def print_table(rows: List[Dict[str, Any]], top: int) -> None:
    rows = sorted(rows, key=lambda r: r["n"], reverse=True)[:top]
    if not rows:
        print("（无匹配访问行）")
        return
    headers = (
        "n",
        "err",
        "p50",
        "p95",
        "p99",
        "max",
        "sql_avg",
        "sql_max",
        "sqlms_avg",
        "redis_avg",
        "redis_max",
        "route",
    )
    fmt_cells: List[List[str]] = []
    for r in rows:
        route_col = f"{r['method']} {r['route']}".strip()
        fmt_cells.append(
            [
                str(r["n"]),
                str(r["err_n"]),
                f"{r['p50']:.1f}",
                f"{r['p95']:.1f}",
                f"{r['p99']:.1f}",
                f"{r['max']:.1f}",
                f"{r['sql_avg']:.2f}",
                f"{r['sql_max']:.0f}",
                f"{r['sqlms_avg']:.2f}",
                f"{r['redis_avg']:.2f}",
                f"{r['redis_max']:.0f}",
                route_col,
            ]
        )
    widths = [len(h) for h in headers]
    for cells in fmt_cells:
        for i, c in enumerate(cells):
            widths[i] = max(widths[i], len(c))
    line = " ".join(h.ljust(widths[i]) for i, h in enumerate(headers))
    print(line)
    for cells in fmt_cells:
        print(" ".join(cells[i].ljust(widths[i]) for i in range(len(headers))))


def main() -> None:
    ap = argparse.ArgumentParser(description="按 (method, route) 统计 serve 访问日志")
    ap.add_argument("files", nargs="+", help="日志路径，- 表示 stdin，.gz 自动解压")
    ap.add_argument("--from", dest="t_from", metavar="ISO", help="起始时间（含），须带时区")
    ap.add_argument("--to", dest="t_to", metavar="ISO", help="结束时间（不含），须带时区")
    ap.add_argument("--route-prefix", dest="route_prefix", metavar="S", help="只统计 route 前缀匹配的行")
    ap.add_argument("--top", type=int, default=30, help="文本表最多行数（默认 30）")
    ap.add_argument("--json", action="store_true", help="输出 JSON 数组")
    args = ap.parse_args()

    t_from = parse_bound(args.t_from) if args.t_from else None
    t_to = parse_bound(args.t_to) if args.t_to else None

    groups = build_stats(args.files, t_from, t_to, args.route_prefix)
    out_rows = [row_dict(m, r, agg) for (m, r), agg in groups.items()]
    out_rows.sort(key=lambda x: x["n"], reverse=True)
    missing_total = sum(r["missing_obs"] for r in out_rows)

    if args.json:
        print(json.dumps(out_rows[: args.top], ensure_ascii=False, indent=2))
    else:
        print_table(out_rows, args.top)
        if missing_total > 0:
            print(
                f"有 {missing_total} 行没有 sql_n 字段（面板版本没有每请求计数？）",
                file=sys.stderr,
            )


if __name__ == "__main__":
    main()
