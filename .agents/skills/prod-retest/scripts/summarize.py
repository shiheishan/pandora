#!/usr/bin/env python3
"""按 runbook 第 9 节给一个场景出小结（只读本地拉回的数据）。

用法：summarize.py <场景目录> [稳态秒数=1800]   目录下 panel/（面板机产物）与可选 loadgen/（压测机产物）
输出 markdown 到 stdout。来源 IP 打码：从环境变量 PANEL_IP、LOADGEN_IP、NODE1_IP、NODE2_IP 读真实地址
（source ops-local 的 env.sh 后再跑），脚本里不写任何真实值。
MemTotal 与核数取自 panel/meminfo.txt（run-collect.sh 写），没有就读环境变量 MEMTOTAL_KB、CORES。
"""
import csv, json, re, sys, glob, os, datetime as dt
from collections import defaultdict

root = sys.argv[1]
P = os.path.join(root, "panel"); L = os.path.join(root, "loadgen")
MEMTOTAL_KB = int(os.environ.get("MEMTOTAL_KB", "0")); CORES = int(os.environ.get("CORES", "2"))
mi = os.path.join(P, "meminfo.txt")
if os.path.exists(mi):
    for line in open(mi):
        if line.startswith("MemTotal:"): MEMTOTAL_KB = int(line.split()[1])
        if line.startswith("nproc:"): CORES = int(line.split()[1])
if not MEMTOTAL_KB:
    sys.exit("缺 MemTotal：panel/meminfo.txt 不存在时请设环境变量 MEMTOTAL_KB")
IPS = {os.environ[k]: f"<{k[:-3]}>" for k in ("PANEL_IP", "LOADGEN_IP", "NODE1_IP", "NODE2_IP") if os.environ.get(k)}
scen = os.path.basename(os.path.normpath(root))

def ts(s): return dt.datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc).timestamp()

T = None
for line in open(os.path.join(P, "timeline.txt")):
    m = re.search(r"T=(\S+)", line)
    if m: T = ts(m.group(1))
WIN = int(sys.argv[2]) if len(sys.argv) > 2 else 1800
T1 = T + WIN
out = []
w = out.append
w(f"## {scen} 小结\n")
w(f"稳态窗口 T={dt.datetime.fromtimestamp(T, dt.timezone.utc):%H:%M:%S}Z 到 T+{WIN//60}m\n")

# ---- procs.csv ----
rows = list(csv.DictReader(open(os.path.join(P, "procs.csv"))))
win = [r for r in rows if T <= int(r["unix_s"]) <= T1]
sysr = [r for r in win if r["proc"] == "_system"]
cpu = [float(r["cpu_pct"]) for r in sysr]
used = [int(r["rss_kb"]) for r in sysr]
cpu_avg = sum(cpu) / len(cpu) / (CORES * 100) * 100
cpu_max = max(cpu) / (CORES * 100) * 100
mem_peak = max(used) / MEMTOTAL_KB * 100
pin = [int(r["pswpin"]) for r in sysr if r.get("pswpin")]
pout = [int(r["pswpout"]) for r in sysr if r.get("pswpout")]
def steps(v): return sum(1 for a, b in zip(v, v[1:]) if b > a)
swap_used = max(int(r["swap_total_kb"]) - int(r["swap_free_kb"]) for r in sysr if r.get("swap_total_kb"))
dpin, dpout = (pin[-1] - pin[0], pout[-1] - pout[0]) if pin else (None, None)
swap_bad = pin and (steps(pin) > 3 or steps(pout) > 3) and (dpin > 50 or dpout > 50)

w("### 及格线\n")
w("| 及格线 | 实测 | 结论 |\n|---|---|---|")
w(f"| CPU 平均 < 50%（15k 档才判） | 平均 {cpu_avg:.1f}%，峰值格 {cpu_max:.1f}%（{len(sysr)} 格 × 5s） | {'过' if cpu_avg < 50 else '不过'} |")
w(f"| 内存峰值 ≤ 75% MemTotal | 峰值 {max(used)/1024:.0f} MB = {mem_peak:.1f}% | {'过' if mem_peak <= 75 else '不过'} |")
w(f"| swap 换页不持续增长 | Δpswpin={dpin}、Δpswpout={dpout}（增长格数 {steps(pin)}/{steps(pout)}），swap 已用峰值 {swap_used/1024:.1f} MB | {'不过' if swap_bad else '过'} |")

# ---- loadgen JSON ----
lg5 = 0; lg_rows = []
for f in sorted(glob.glob(os.path.join(L, "*.json"))):
    try: j = json.load(open(f))
    except Exception: continue
    if "endpoints" not in j: continue
    name = os.path.basename(f)
    lg5 += j.get("totals", {}).get("server_5xx", 0)
    for e in j["endpoints"]:
        lg_rows.append((name, e))
node_rows = [(n, e) for n, e in lg_rows if n == "nodes.json" and e["endpoint"].startswith("node:")]
# nodes.json 的分位数覆盖整次运行（含起跑与收尾齐射）；稳态内只能用 10 秒格的 max 做保守上界
starts = {}
for f in glob.glob(os.path.join(L, "*.json")):
    try: jj = json.load(open(f))
    except Exception: continue
    if "started_at" in jj: starts[os.path.basename(f)] = dt.datetime.fromisoformat(jj["started_at"].replace("Z", "+00:00")).timestamp()
def steady_max(n, e):
    st = starts.get(n)
    if st is None: return None
    ws = [x["max_ms"] for x in e.get("timeline", []) if T <= st + x["offset_s"] and st + x["offset_s"] + 10 <= T1]
    return max(ws) if ws else None
if node_rows and all(e.get("steady") for _, e in node_rows if e["count"] > 1000):
    sw = max((e["steady"]["p99_ms"], e["endpoint"]) for _, e in node_rows if e.get("steady") and e["steady"]["count"] > 0)
    worst = max(e["p99_ms"] for _, e in node_rows)
    w(f"| 节点接口 p99 < 300ms（逐端点） | 稳态 p99 最差 {sw[0]:.1f}ms（{sw[1]}）；全程 p99 最差 {worst:.1f}ms | {'过' if sw[0] < 300 else '不过'} |")
elif node_rows:
    worst = max(e["p99_ms"] for _, e in node_rows)
    sm = [steady_max(n, e) for n, e in node_rows]
    smw = max(x for x in sm if x is not None) if any(x is not None for x in sm) else None
    w(f"| 节点接口 p99 < 300ms（逐端点） | 全程 p99 最差 {worst:.0f}ms；稳态窗口内单请求最大值最差 {smw:.0f}ms（上界） | {'过' if worst < 300 else ('全程不过；稳态见端点表' if smw is not None else '不过')} |")
else:
    w("| 节点接口 p99 < 300ms | 本场景无压测工具 JSON（空载只有两台真节点） | — |")

# ---- nginx ----
ng5 = 0; ng_total = 0; by_ip = defaultdict(int); st = defaultdict(int)
nwf = os.path.join(P, "nginx-window.txt")
if os.path.exists(nwf):  # 面板机上按窗口算好的统计（访问日志太大时不拉回）
    for line in open(nwf):
        k, _, v = line.strip().partition("=")
        if k == "lines": ng_total = int(v)
        elif k.startswith("status_"): st[int(k[7])] = int(v); ng5 += int(v) if k == "status_5xx" else 0
        elif k.startswith("src_"): by_ip["<" + k[4:] + ">"] = int(v)
pat = re.compile(r"^(\S+) .*?\[([^\]]+)\] method=\S+ status=(\d+)")
for f in ([] if os.path.exists(nwf) else glob.glob(os.path.join(P, "*access*.log"))):
    for line in open(f, errors="replace"):
        m = pat.match(line)
        if not m: continue
        t = dt.datetime.strptime(m.group(2), "%d/%b/%Y:%H:%M:%S %z").timestamp()
        if not (T <= t <= T1): continue
        ng_total += 1; s = int(m.group(3)); st[s // 100] += 1
        by_ip[IPS.get(m.group(1), "other")] += 1
        if s >= 500: ng5 += 1
w(f"| 零 5xx | 压测工具 server_5xx={lg5}；nginx 窗口内 {ng_total} 行，5xx {ng5} 行 | {'过' if lg5 == 0 and ng5 == 0 else '不过'} |\n")
w(f"nginx 状态分布：{dict(sorted(st.items()))}；来源：{dict(by_ip)}\n")

# ---- cgroup ----
cg = list(csv.DictReader(open(os.path.join(P, "cgroup.csv"))))
w(f"### 网关 cgroup（T 与 T+{WIN//60}m 最近两行做差）\n")
w("| 网关 | Δnr_throttled | 节流周期占比 | Δthrottled_usec | Δmem_max_events | Δoom_kill | memory_current 峰值 |\n|---|---|---|---|---|---|---|")
for u in ("aegis-public", "aegis-admin", "aegis-node"):
    ur = [r for r in cg if r["unit"] == u]
    a = min(ur, key=lambda r: abs(int(r["unix_s"]) - T)); b = min(ur, key=lambda r: abs(int(r["unix_s"]) - T1))
    d = lambda k: int(b[k]) - int(a[k])
    per = d("nr_periods")
    peak = max(int(r["memory_current"]) for r in ur if T <= int(r["unix_s"]) <= T1)
    w(f"| {u} | {d('nr_throttled')} | {d('nr_throttled')/per*100 if per else 0:.2f}% | {d('throttled_usec')} | {d('mem_max_events')} | {d('oom_kill')} | {peak/1048576:.1f} MB / {int(a['memory_max'])/1048576:.0f} MB |")

# ---- 各进程 ----
w("\n### 各进程（稳态窗口）\n")
w("| 进程 | CPU 平均（单核=100） | CPU 峰值 | PSS 峰值 | PSS 首→末 |\n|---|---|---|---|---|")
for p in ("aegis-public", "aegis-admin", "aegis-node", "postgres", "valkey", "nginx"):
    pr = [r for r in win if r["proc"] == p]
    if not pr: continue
    c = [float(r["cpu_pct"]) for r in pr]; ps = [int(r["pss_kb"] or 0) for r in pr]
    w(f"| {p} | {sum(c)/len(c):.1f} | {max(c):.1f} | {max(ps)/1024:.1f} MB | {ps[0]/1024:.1f} → {ps[-1]/1024:.1f} MB |")

# ---- 压测端点 ----
if lg_rows:
    w("\n### 压测工具端点\n")
    w("| 文件 | 端点 | count | QPS | p50 | p95 | p99 | max | 稳态 count | 稳态 p50 | 稳态 p99 | 稳态 max | 5xx | 非 2xx/3xx 码 |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
    for n, e in lg_rows:
        bad = {k: v for k, v in e.get("codes", {}).items() if not k.startswith(("2", "3"))}
        sd = e.get("steady") or {}
        f1 = lambda v: "" if v is None else (f"{v:.1f}" if v < 100 else f"{v:.0f}")
        w(f"| {n} | {e['endpoint']} | {e['count']} | {e['qps']:.2f} | {f1(e['p50_ms'])} | {f1(e['p95_ms'])} | {f1(e['p99_ms'])} | {f1(e['max_ms'])} | {sd.get('count','')} | {f1(sd.get('p50_ms'))} | {f1(sd.get('p99_ms'))} | {f1(sd.get('max_ms'))} | {e['server_5xx']} | {bad or ''} |")

# ---- pgxpool 等待 ----
import subprocess
w("\n### 等 pgxpool 的 goroutine（窗口中点的 goroutine profile）\n")
w("| 网关 | goroutine 总数 | 停在 pgxpool/puddle Acquire |\n|---|---|---|")
for g in ("public", "admin", "node"):
    fs = sorted(glob.glob(os.path.join(P, f"aegis-{g}-goroutine-*.pprof")))
    if not fs: continue
    def top(extra):
        try: return subprocess.run(["go", "tool", "pprof", "-top"] + extra + [fs[-1]], capture_output=True, text=True, timeout=60).stdout
        except Exception: return ""
    tot = re.search(r"of (\d+) total", top([]))
    acq = re.search(r"Showing nodes accounting for (\d+)", top(["-focus", "pgxpool.*Acquire|puddle"]))
    w(f"| aegis-{g} | {tot.group(1) if tot else '?'} | {acq.group(1) if acq else 0} |")

# ---- pg_stat_activity 采样 ----
pa = os.path.join(P, "pgact.csv")
if os.path.exists(pa):
    ar = [r for r in csv.DictReader(open(pa)) if r.get("active") and T <= int(r["unix_s"]) <= T1]
    if ar:
        g = lambda k: [float(r[k]) for r in ar if r[k] != ""]
        w(f"\n### pg_stat_activity（aegis_app，稳态内 {len(ar)} 次采样 × 10s）\n")
        w("| 指标 | 平均 | 最大 |\n|---|---|---|")
        for k, lab in (("active","active 连接"),("idle","idle 连接"),("total","连接总数"),("wait_lock","等 Lock"),("wait_lwlock","等 LWLock"),("wait_io","等 IO"),("max_active_s","最长在跑语句（秒）"),("conn_public","public 已建连接（池上限 16）"),("conn_admin","admin 已建连接（池上限 15）"),("conn_node","node 已建连接（池上限 15）")):
            v = g(k)
            if v: w(f"| {lab} | {sum(v)/len(v):.2f} | {max(v):g} |")

# ---- pgstat top10 ----
tot = sorted(glob.glob(os.path.join(P, "pgstat-*-total.csv")))
if tot:
    w("\n### pg_stat_statements 总耗时前 10\n")
    w("| calls | total_ms | mean_ms | query（截断） |\n|---|---|---|---|")
    for r in list(csv.DictReader(open(tot[-1])))[:10]:
        q = re.sub(r"\s+", " ", r["query"])[:110].replace("|", "\\|")
        w(f"| {r['calls']} | {float(r['total_exec_ms']):.0f} | {float(r['mean_exec_ms']):.2f} | `{q}` |")
print("\n".join(out))
