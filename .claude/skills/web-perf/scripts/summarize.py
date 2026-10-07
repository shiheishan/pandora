#!/usr/bin/env python3
"""把 nodes-bench / lh.mjs 的原始 JSON 汇总成表。

用法：
  summarize.py bench <bench-*.json ...>   每个文件一行：首屏、加载期长任务、静置期事件与重拉、CPU、滚动、全选、搜索
  summarize.py lh <lh.jsonl>              按 (预设, 缓存) 分组取中位数
"""
import json
import statistics
import sys


def r(v, nd=0):
    return "-" if v is None else (round(v, nd) if nd else round(v))


def bench(paths):
    print("| 场景 | 降速 | 首屏 ms | 行数 | DOM | 加载长任务(最长 ms) | 列表 KB | 静置秒 | nodes.changed | 重拉 | 每次重拉脚本 ms | 长任务(最长) | 主线程 Task/Script/Style/Layout ms | 滚动 fps / p95 / 卡顿帧 | 全选 ms | 搜索 ms |")
    print("|" + "---|" * 16)
    for p in paths:
        try:
            d = json.load(open(p))
        except Exception as e:  # 浏览器崩溃、超时时文件里是报错文本
            print(f"| {p} | 解析失败: {str(e)[:60]} |")
            continue
        l, i, s = d["load"], d["idle"], d["scroll"]
        name = p.rsplit("/", 1)[-1].removeprefix("bench-").removesuffix(".json")
        search = d.get("search") or {}
        print("| " + " | ".join(str(x) for x in [
            name, "x" + str(d["throttle"]), r(l["rowsAt"]), l["rows"], l["dom"],
            f"{l['loadLongTasks']} ({r(l['loadLongMax'])})",
            r((l.get("listBytes") or 0) / 1024),
            r(i["windowMs"] / 1000), i.get("nodesChanged", "-"), i["nodeRefetches"], r(i.get("scriptPerRefetch")),
            f"{i['longTasks']} ({r(i['longMax'])})",
            f"{r(i['cpu_TaskDuration'])}/{r(i['cpu_ScriptDuration'])}/{r(i['cpu_RecalcStyleDuration'])}/{r(i['cpu_LayoutDuration'])}",
            f"{s['fps']} / {s['p95']} / {s['janky']}",
            r(d.get("selectAllMs")), r(search.get("ms")),
        ]) + " |")


def lh(path):
    groups = {}
    for line in open(path):
        line = line.strip()
        if not line.startswith("{"):
            continue
        d = json.loads(line)
        groups.setdefault((d["url"], d["preset"], d["cache"]), []).append(d)
    keys = ["score", "ttfb", "fcp", "lcp", "tbt", "cls", "si", "requests", "transferKB"]
    print("| URL | 预设 | 缓存 | 次数 | " + " | ".join(keys) + " | 协议 | 最慢的 3 个接口（中位 ms） |")
    print("|" + "---|" * (len(keys) + 6))
    for (url, preset, cache), runs in sorted(groups.items()):
        med = []
        for k in keys:
            vals = [x[k] for x in runs if x.get(k) is not None]
            med.append("-" if not vals else round(statistics.median(vals), 3 if k == "cls" else (2 if k == "score" else 0)))
        apis = {}
        for x in runs:
            for k, v in (x.get("api") or {}).items():
                apis.setdefault(k, []).append(v)
        top = sorted(((statistics.median(v), k) for k, v in apis.items()), reverse=True)[:3]
        protos = ",".join(sorted({p for x in runs for p in (x.get("protocols") or "").split(",") if p}))
        print(f"| {url} | {preset} | {cache} | {len(runs)} | " + " | ".join(str(m) for m in med)
              + f" | {protos} | " + "，".join(f"{k} {round(v)}" for v, k in top) + " |")


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    if sys.argv[1] == "bench":
        bench(sys.argv[2:])
    elif sys.argv[1] == "lh":
        lh(sys.argv[2])
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
