#!/usr/bin/env python3
"""整机 CPU 拆分（本机读拉回的数据）：cpu.py <场景目录> [起 unix 秒] [止 unix 秒]
读 panel/cpu/{procstat,softirqs,allprocs}.csv 与 vmstat.txt，默认窗口 = 稳态 [T, T+30m) 与采样覆盖的交集。
口径：单核 = 100，两核整机 = 200（与 procs.csv 的 cpu_pct 一致）。CLK_TCK = 100。
"""
import csv, os, re, sys, datetime as dt
from collections import defaultdict

root = sys.argv[1]
P = os.path.join(root, "panel")
C = os.path.join(P, "cpu")
T = None
for line in open(os.path.join(P, "timeline.txt")):
    m = re.search(r"T=(\S+)", line)
    if m:
        T = dt.datetime.strptime(m.group(1), "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc).timestamp()
a = float(sys.argv[2]) if len(sys.argv) > 2 else T
b = float(sys.argv[3]) if len(sys.argv) > 3 else T + 1800


def rows(f):
    return [r for r in csv.DictReader(open(os.path.join(C, f))) if a <= int(r["unix_s"]) <= b]


w = print
ps = rows("procstat.csv")
if len(ps) < 2:
    w("（窗口内没有 /proc/stat 采样）")
    sys.exit()
f, l = ps[0], ps[-1]
secs = int(l["unix_s"]) - int(f["unix_s"])
keys = ["user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"]
d = {k: int(l[k]) - int(f[k]) for k in keys}
tot = sum(d.values())
fmtt = lambda t: dt.datetime.fromtimestamp(t, dt.timezone.utc).strftime("%H:%M:%SZ")
w(f"覆盖 {fmtt(int(f['unix_s']))} → {fmtt(int(l['unix_s']))}（{secs} 秒；稳态窗口 {fmtt(T)} → {fmtt(T + 1800)}）\n")
w("### /proc/stat 拆分（两核整机 = 200）\n")
w("| 类别 | 平均（单核 = 100） | 占忙时 |\n|---|---|---|")
busy = tot - d["idle"] - d["iowait"]
for k in keys:
    v = d[k] / tot * 200
    share = "" if k in ("idle", "iowait") else f"{d[k] / busy * 100:.1f}%"
    w(f"| {k} | {v:.1f} | {share} |")
w(f"| **忙（非 idle/iowait）** | **{busy / tot * 200:.1f}** | 100% |\n")

# softirq 各类
sq = rows("softirqs.csv")
if len(sq) >= 2:
    ks = [k for k in sq[0] if k != "unix_s"]
    dd = {k: int(sq[-1][k]) - int(sq[0][k]) for k in ks}
    s = int(sq[-1]["unix_s"]) - int(sq[0]["unix_s"])
    w("softirq 次数/秒（两核合计）：" + "，".join(f"{k} {v / s:.0f}" for k, v in sorted(dd.items(), key=lambda x: -x[1]) if v) + "\n")

# 按进程（comm）
ap = rows("allprocs.csv")
byt = defaultdict(dict)
byc = defaultdict(dict)
for r in ap:
    byt[int(r["unix_s"])][r["comm"]] = int(r["ticks"])
    if r.get("child_ticks"):
        byc[int(r["unix_s"])][r["comm"]] = int(r["child_ticks"])
ts = sorted(byt)
if len(ts) >= 2:
    t0, t1 = ts[0], ts[-1]
    span = t1 - t0
    comms = set(byt[t0]) | set(byt[t1])
    # 只算两端都在的进程名的增量；新起的进程（如 docker exec 起的 psql）取末端全量
    delta = {c: byt[t1].get(c, 0) - byt[t0].get(c, 0) for c in comms}
    delta = {c: v for c, v in delta.items() if v > 0}
    sm = sum(delta.values())
    w(f"### 按进程名（{fmtt(t0)} → {fmtt(t1)}，{span} 秒；utime+stime，单核 = 100）\n")
    w("| 进程名 | CPU | 占进程合计 |\n|---|---|---|")
    for c, v in sorted(delta.items(), key=lambda x: -x[1])[:15]:
        w(f"| {c} | {v / span:.1f} | {v / sm * 100:.1f}% |")
    w(f"| **进程合计** | **{sm / span:.1f}** | |")
    w(f"\n进程合计 {sm / span:.1f} 对 /proc/stat 的 user+nice+system {(d['user'] + d['nice'] + d['system']) / tot * 200:.1f}；"
      f"差额主要是短命进程（采样间隔内起停、退出后只记在父进程 cutime 里，如每 10 秒 docker exec 起的 psql、采样脚本的子进程）；"
      f"softirq {d['softirq'] / tot * 200:.1f} 与 steal {d['steal'] / tot * 200:.1f} 不记在任何进程名下。\n")
    if byc.get(t0) and byc.get(t1):
        cd = {c: byc[t1].get(c, 0) - byc[t0].get(c, 0) for c in set(byc[t0]) & set(byc[t1])}
        cd = {c: v for c, v in cd.items() if v > 0}
        if cd:
            w("已回收子进程的 CPU（cutime+cstime 增量，按父进程名；短命进程的 CPU 记在这里）：\n")
            w("| 父进程名 | 子进程 CPU（单核 = 100） |\n|---|---|")
            for c, v in sorted(cd.items(), key=lambda x: -x[1])[:10]:
                w(f"| {c} | {v / span:.1f} |")
            w(f"| **合计** | **{sum(cd.values()) / span:.1f}** |\n")

# vmstat
vf = os.path.join(C, "vmstat.txt")
if os.path.exists(vf):
    vs = []
    for line in open(vf):
        p = line.split()
        if len(p) >= 19 and p[0].isdigit():
            t = dt.datetime.strptime(p[-2] + " " + p[-1], "%Y-%m-%d %H:%M:%S").replace(tzinfo=dt.timezone.utc).timestamp()
            if a <= t <= b:
                vs.append(p)
    if vs:
        col = lambda i: [int(x[i]) for x in vs]
        avg = lambda v: sum(v) / len(v)
        w(f"vmstat 1（{len(vs)} 秒，百分比口径 = 两核合计的 %）：us {avg(col(12)):.1f}、sy {avg(col(13)):.1f}、id {avg(col(14)):.1f}、wa {avg(col(15)):.1f}、st {avg(col(16)):.1f}（st 最大 {max(col(16))}）；"
          f"上下文切换 {avg(col(11)):.0f}/s、中断 {avg(col(10)):.0f}/s；si/so {sum(col(6))}/{sum(col(7))}")
