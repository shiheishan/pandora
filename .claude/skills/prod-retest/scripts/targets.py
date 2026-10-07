#!/usr/bin/env python3
"""分档用户目标表、与基线对比、CPU 随节点数外推、pprof CPU 前 10（只读本机拉回的数据）。

用法：
  targets.py targets <场景目录>                 → 用户目标表 + pprof CPU 前 10（markdown）
  targets.py compare <场景目录> <基线场景目录>   → 逐项对比（markdown；两轮差别写在环境变量 CMP_NOTE）
  targets.py scale <场景目录> <场景目录>...      → CPU 随节点数的表与线性外推（用户侧参数要相同）
场景目录下 panel/（面板机产物）与 loadgen/（压测机产物），同 summarize.py。
"""
import csv, glob, json, os, re, subprocess, sys, datetime as dt

CORES = 2


def ts(s):
    return dt.datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc).timestamp()


def window(root):
    T = None
    for line in open(os.path.join(root, "panel", "timeline.txt")):
        m = re.search(r"T=(\S+)", line)
        if m:
            T = ts(m.group(1))
    return T, T + 1800


def load(root, name):
    f = os.path.join(root, "loadgen", name)
    return json.load(open(f)) if os.path.exists(f) else None


def eps(root):
    """(文件, 端点) → 端点统计；稳态优先。"""
    out = {}
    for n in ("nodes.json", "users.json", "users-warmup.json", "burst-http.json"):
        j = load(root, n)
        if not j:
            continue
        for e in j["endpoints"]:
            out[(n, e["endpoint"])] = e
    return out


def st(e):
    """取稳态窗口统计；没有稳态（预热、burst）就用全程。返回 (统计, 口径)。"""
    s = e.get("steady")
    if s and s.get("count"):
        return s, "稳态"
    return e, "全程"


def fm(v):
    return "" if v is None else (f"{v:.1f}" if v < 100 else f"{v:.0f}")


def timeouts(e):
    return sum(v for k, v in e.get("codes", {}).items() if k.startswith("transport:"))


def proc_cpu(root):
    T, T1 = window(root)
    rows = [r for r in csv.DictReader(open(os.path.join(root, "panel", "procs.csv"))) if T <= int(r["unix_s"]) <= T1]
    res = {}
    for p in ("_system", "aegis-public", "aegis-admin", "aegis-node", "postgres", "valkey", "nginx"):
        c = [float(r["cpu_pct"]) for r in rows if r["proc"] == p]
        if c:
            res[p] = (sum(c) / len(c), max(c))
    sysr = [r for r in rows if r["proc"] == "_system" and r.get("pswpin")]
    sw = None
    if sysr:
        sw = dict(
            dpin=int(sysr[-1]["pswpin"]) - int(sysr[0]["pswpin"]),
            dpout=int(sysr[-1]["pswpout"]) - int(sysr[0]["pswpout"]),
            used_mb=max(int(r["swap_total_kb"]) - int(r["swap_free_kb"]) for r in sysr) / 1024,
            used_first=(int(sysr[0]["swap_total_kb"]) - int(sysr[0]["swap_free_kb"])) / 1024,
            used_last=(int(sysr[-1]["swap_total_kb"]) - int(sysr[-1]["swap_free_kb"])) / 1024,
            mem_peak_mb=max(int(r["rss_kb"]) for r in sysr) / 1024,
        )
    return res, sw


def vmswap(root):
    """稳态内各进程 VmSwap 峰值（MB）。"""
    f = os.path.join(root, "panel", "vmswap.csv")
    if not os.path.exists(f):
        return {}
    T, T1 = window(root)
    peak = {}
    for r in csv.DictReader(open(f)):
        t = ts(r["ts_utc"])
        if T <= t <= T1:
            peak[r["proc"]] = max(peak.get(r["proc"], 0), int(r["vmswap_kb"]) / 1024)
    return peak


def pgtop(root, n=10):
    fs = sorted(glob.glob(os.path.join(root, "panel", "pgstat-*-total.csv")))
    if not fs:
        return []
    return list(csv.DictReader(open(fs[-1])))[:n]


def pprof_top(f, n=10):
    try:
        out = subprocess.run(["go", "tool", "pprof", "-top", f"-nodecount={n}", f], capture_output=True, text=True, timeout=120).stdout
    except Exception as ex:
        return f"(pprof 失败：{ex})"
    keep = []
    started = False
    for line in out.splitlines():
        if line.lstrip().startswith("flat"):
            started = True
        if started or line.startswith(("Duration", "Showing", "Dropped")):
            keep.append(line)
    return "\n".join(keep)


def goroutine_acquire(root):
    res = {}
    for g in ("public", "admin", "node"):
        fs = sorted(glob.glob(os.path.join(root, "panel", f"aegis-{g}-goroutine-*.pprof")))
        if not fs:
            continue

        def top(extra):
            try:
                return subprocess.run(["go", "tool", "pprof", "-top"] + extra + [fs[-1]], capture_output=True, text=True, timeout=60).stdout
            except Exception:
                return ""
        tot = re.search(r"of (\d+) total", top([]))
        acq = re.search(r"Showing nodes accounting for (\d+)", top(["-focus", "pgxpool.*Acquire|puddle"]))
        res[g] = (int(tot.group(1)) if tot else None, int(acq.group(1)) if acq else 0)
    return res


def pgact(root):
    f = os.path.join(root, "panel", "pgact.csv")
    if not os.path.exists(f):
        return None
    T, T1 = window(root)
    rows = [r for r in csv.DictReader(open(f)) if r.get("active") and T <= int(r["unix_s"]) <= T1]
    return rows


# 分档：返回 (档名, p50 上限, p99 上限)；None 表示该分位不设线
ADMIN_BY_ID = re.compile(r"^admin:(GET /v1/me$|GET /v1/[a-z-]+/\{id\}$|POST /v1/[a-z-]+/\{id\}/|POST /v1/user-groups$)")


def classify(ep):
    if ep.startswith("node:"):
        return ("stream", None, None) if ep.endswith("/stream") else ("节点", None, 20)
    if ep.endswith("POST /v1/auth/login"):
        return ("login", None, None)
    if ep.startswith("admin:"):
        if ADMIN_BY_ID.match(ep):
            return ("后台单条", 5, 50)
        return ("后台列表/看板", 50, 200)
    return ("门户", 5, 50)


# ---------------------------------------------------------------------------
def targets(root):
    w = print
    E = eps(root)
    w("## 用户目标表\n")
    w("分档目标（用户 2026-10-07 定）：门户 p50<5 p99<50；节点 p99<20；后台按 id 取一条（含单条写）p50<5 p99<50；后台列表、搜索、看板 p50<50 p99<200；登录不设速度目标，只看不超时、不排队（无 503）；所有端点零 5xx、不超时。")
    w("users / nodes 用稳态窗口 [T, T+30m) 的统计；预热（users-warmup）与 burst 只有全程统计。延迟是压测机侧测得的端到端时间（含 TLS 与新加坡同机房网络往返）。\n")
    w("| 文件 | 端点 | 口径 | count | p50 | p95 | p99 | max | 5xx | 超时（全程兜底） | 其他非 2xx/3xx | 目标 | 达标 |")
    w("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
    fails = []
    for (n, ep), e in sorted(E.items(), key=lambda kv: (kv[0][0] != "nodes.json", kv[0])):
        s, kind = st(e)
        if not s.get("count"):
            continue
        p50, p95, p99, mx = s.get("p50_ms"), s.get("p95_ms"), s.get("p99_ms"), s.get("max_ms")
        codes = s.get("codes", {}) or {}
        to = max(sum(v for k, v in codes.items() if k.startswith("transport:")), timeouts(e))  # 稳态 codes 可能不计 transport，取全程兜底
        other = {k: v for k, v in codes.items() if not k.startswith(("2", "3", "transport:")) and not k.startswith("5")}
        s5 = s.get("server_5xx", 0)
        tier, lim50, lim99 = classify(ep)
        clean = s5 == 0 and to == 0
        if tier == "stream":
            goal, ok = "长连接，只看建立", clean
        elif tier == "login":
            goal, ok = "登录：不超时、不排队", clean and not codes.get("503")
        else:
            goal = f"{tier} p50<{lim50} p99<{lim99}" if lim50 else f"{tier} p99<{lim99}"
            ok = clean and p99 < lim99 and (lim50 is None or p50 < lim50)
        if not ok:
            fails.append((n, ep, p50, p99, s5, to))
        w(f"| {n} | {ep} | {kind} | {s['count']} | {fm(p50)} | {fm(p95)} | {fm(p99)} | {fm(mx)} | {s5} | {to} | {other or ''} | {goal} | {'✅' if ok else '❌'} |")
    w("")
    # 其他目标
    res, sw = proc_cpu(root)
    vs = vmswap(root)
    ga = goroutine_acquire(root)
    pa = pgact(root)
    tot5 = sum((st(e)[0].get("server_5xx", 0)) for e in E.values())
    totto = sum(timeouts(e) for e in E.values())  # 全程口径
    w("| 目标 | 实测 | 达标 |\n|---|---|---|")
    w(f"| 零 5xx | 压测工具各端点（稳态/全程口径同上表）合计 {tot5} | {'✅' if tot5 == 0 else '❌'} |")
    w(f"| 不超时 | transport:*（全程）合计 {totto} | {'✅' if totto == 0 else '❌'} |")
    if sw:
        bad = sw["dpin"] > 50 or sw["dpout"] > 50
        w(f"| 不换页 | 稳态内 Δpswpin={sw['dpin']}、Δpswpout={sw['dpout']}；swap 已用 {sw['used_first']:.1f}→{sw['used_last']:.1f} MB（峰 {sw['used_mb']:.1f}）；进程 VmSwap 峰值 {', '.join(f'{k} {v:.1f}MB' for k, v in sorted(vs.items(), key=lambda x: -x[1]) if v > 0) or '全 0'} | {'❌' if bad else '✅'} |")
    if ga or pa:
        gtxt = "；".join(f"aegis-{g} {a}/{t} 个 goroutine 停在 Acquire" for g, (t, a) in ga.items())
        if pa:
            act = [int(r["active"]) for r in pa]
            cn = {g: max(int(r[f"conn_{g}"]) for r in pa) for g in ("public", "admin", "node")}
            ptxt = f"pg_stat_activity aegis_app active 平均 {sum(act)/len(act):.2f}、最大 {max(act)}（{len(pa)} 次 × 10s）；各网关已建连接最大 public {cn['public']}/16、admin {cn['admin']}/15、node {cn['node']}/15"
        else:
            ptxt = "pg_stat_activity 未采"
        q = any(a for _, a in ga.values()) or (pa and any(int(r[f"conn_{g}"]) >= (16 if g == "public" else 15) for r in pa for g in ("public", "admin", "node")))
        w(f"| 连接池不排队 | {gtxt}（T+15m 的 goroutine profile）；{ptxt}。面板不导出 pgxpool.Stat()（AcquireCount/EmptyAcquireCount/AcquireDuration 拿不到），只能用这两个旁证 | {'❌' if q else '✅'} |")
    w("")
    w("## pprof CPU 前 10（T+15m 起 30 秒）\n")
    for g in ("public", "admin", "node"):
        fs = sorted(glob.glob(os.path.join(root, "panel", f"aegis-{g}-cpu-*.pprof")))
        if fs:
            w(f"### aegis-{g}\n\n```\n{pprof_top(fs[-1])}\n```\n")
    return fails


def per_min(root, j):
    """每节点每分钟各端点请求数：优先 per_unit_per_min，否则按稳态 count / 节点数 / 30。"""
    out = {}
    units = 198
    if j.get("per_unit"):
        units = j["per_unit"].get("units", units)
    for e in j["endpoints"]:
        if not e["endpoint"].startswith("node:"):
            continue
        if e.get("per_unit_per_min"):
            out[e["endpoint"]] = e["per_unit_per_min"]
        elif e.get("steady"):
            out[e["endpoint"]] = e["steady"]["count"] / units / 30
    return out


def compare(a, b):
    w = print
    na, nb = os.path.basename(os.path.normpath(a)), os.path.basename(os.path.normpath(b))
    w(f"# {na} 对 {nb}\n")
    note = os.environ.get("CMP_NOTE")
    if note:
        w(note + "\n")
    else:
        w(f"两轮的差别（版本、节点数、节拍代际、库状态）请用环境变量 CMP_NOTE 写明。\n")
    ja, jb = load(a, "nodes.json"), load(b, "nodes.json")
    pa, pb = per_min(a, ja), per_min(b, jb)
    w("## 节点请求数（每节点每分钟，稳态）\n")
    w(f"| 端点 | {na} | {nb} |\n|---|---|---|")
    for k in sorted(set(pa) | set(pb)):
        w(f"| {k} | {pa.get(k, 0):.2f} | {pb.get(k, 0):.2f} |")
    w(f"| **合计** | **{sum(pa.values()):.2f}** | **{sum(pb.values()):.2f}** |")
    qps = lambda j: sum(e["steady"]["qps"] for e in j["endpoints"] if e["endpoint"].startswith("node:") and e.get("steady"))
    units = lambda j: (j.get("per_unit") or {}).get("units") or j.get("meta", {}).get("nodes")
    w(f"| 节点数 | {units(ja)} | {units(jb)} |")
    w(f"| **节点总 QPS（稳态）** | **{qps(ja):.1f}** | **{qps(jb):.1f}** |\n")
    # 5xx
    def five(root):
        E = eps(root)
        return sum(st(e)[0].get("server_5xx", 0) for e in E.values()), sum(timeouts(e) for e in E.values())
    def ngx(root):
        f = os.path.join(root, "panel", "nginx-window.txt")
        if os.path.exists(f):
            d = dict(l.strip().split("=", 1) for l in open(f) if "=" in l)
            return d.get("lines"), d.get("status_5xx")
        # 没有面板机侧算好的窗口统计就直接数访问日志
        T, T1 = window(root)
        pat = re.compile(r"^\S+ .*?\[([^\]]+)\] method=\S+ status=(\d+)")
        n = n5 = 0
        for f in glob.glob(os.path.join(root, "panel", "*access*.log")):
            for line in open(f, errors="replace"):
                m = pat.match(line)
                if not m:
                    continue
                t = dt.datetime.strptime(m.group(1), "%d/%b/%Y:%H:%M:%S %z").timestamp()
                if T <= t < T1:
                    n += 1
                    n5 += int(m.group(2)) >= 500
        return n, n5
    fa, fb = five(a), five(b)
    la, lb = ngx(a), ngx(b)
    w("## 5xx 与超时\n")
    w(f"| 项 | {na} | {nb} |\n|---|---|---|")
    w(f"| 压测工具 5xx（稳态口径，预热/burst 全程） | {fa[0]} | {fb[0]} |")
    w(f"| 压测工具 transport 超时（全程） | {fa[1]} | {fb[1]} |")
    w(f"| nginx 窗口内行数 / 5xx | {la[0]} / {la[1]} | {lb[0]} / {lb[1]} |\n")
    # p99
    Ea, Eb = eps(a), eps(b)
    w("## 各端点 p99（ms；users/nodes 稳态，预热/burst 全程）\n")
    w(f"| 文件 | 端点 | {na} p99 | {nb} p99 | {na} 5xx | {nb} 5xx |\n|---|---|---|---|---|---|")
    for k in sorted(set(Ea) | set(Eb), key=lambda k: (k[0] != "nodes.json", k)):
        sa = st(Ea[k])[0] if k in Ea else None
        sb = st(Eb[k])[0] if k in Eb else None
        w(f"| {k[0]} | {k[1]} | {fm(sa['p99_ms']) if sa else '—'} | {fm(sb['p99_ms']) if sb else '—'} | {sa.get('server_5xx', 0) if sa else '—'} | {sb.get('server_5xx', 0) if sb else '—'} |")
    w("")
    ca, swa = proc_cpu(a)
    cb, swb = proc_cpu(b)
    w("## CPU（稳态平均 / 峰值格，单核 = 100）\n")
    w(f"| 进程 | {na} | {nb} |\n|---|---|---|")
    for p in ("_system", "postgres", "aegis-public", "aegis-admin", "aegis-node", "nginx", "valkey"):
        f = lambda c: f"{c[p][0]:.1f} / {c[p][1]:.1f}" if p in c else "—"
        w(f"| {'整机（两核 = 200）' if p == '_system' else p} | {f(ca)} | {f(cb)} |")
    w("")
    w("## swap 与内存\n")
    w(f"| 项 | {na} | {nb} |\n|---|---|---|")
    for k, lab in (("dpin", "Δpswpin（页）"), ("dpout", "Δpswpout（页）"), ("used_mb", "swap 已用峰值 MB"), ("mem_peak_mb", "MemTotal−MemAvailable 峰值 MB")):
        w(f"| {lab} | {swa[k] if isinstance(swa[k], int) else round(swa[k], 1)} | {swb[k] if isinstance(swb[k], int) else round(swb[k], 1)} |")
    ga, gb = goroutine_acquire(a), goroutine_acquire(b)
    w("\n## 等连接池的 goroutine（T+15m）\n")
    w(f"| 网关 | {na} | {nb} |\n|---|---|---|")
    for g in ("public", "admin", "node"):
        f = lambda d: f"{d[g][1]}/{d[g][0]}" if g in d else "—"
        w(f"| aegis-{g} | {f(ga)} | {f(gb)} |")
    for root, nm in ((a, na), (b, nb)):
        w(f"\n## pg_stat_statements 总耗时前 10：{nm}\n")
        w("| # | calls | total_ms | mean_ms | query（截断） |\n|---|---|---|---|---|")
        for i, r in enumerate(pgtop(root), 1):
            q = re.sub(r"\s+", " ", r["query"])[:120].replace("|", "\\|")
            w(f"| {i} | {r['calls']} | {float(r['total_exec_ms']):.0f} | {float(r['mean_exec_ms']):.2f} | `{q}` |")


def scale(dirs):
    """按节点数线性外推 CPU：scale <场景目录>...（至少两个，用户侧参数相同、只有节点数不同）。"""
    w = print
    pts = []
    for d in dirs:
        j = load(d, "nodes.json")
        n = (j.get("per_unit") or {}).get("units") or j.get("meta", {}).get("nodes")
        c, _ = proc_cpu(d)
        pts.append((n, c, os.path.basename(os.path.normpath(d))))
    pts.sort()
    w("## CPU 随节点数（稳态平均，单核 = 100；整机两核 = 200）\n")
    procs = ("_system", "postgres", "aegis-node", "aegis-public", "aegis-admin", "nginx", "valkey")
    w("| 场景 | 节点数 | " + " | ".join("整机" if p == "_system" else p for p in procs) + " |")
    w("|---|---|" + "---|" * len(procs))
    for n, c, nm in pts:
        w(f"| {nm} | {n} | " + " | ".join(f"{c[p][0]:.1f}" if p in c else "—" for p in procs) + " |")
    (n0, c0, a), (n1, c1, b) = pts[0], pts[-1]
    w(f"\n### 线性外推（{a} → {b}）\n")
    w("| 进程 | 每增加 100 个节点 | 截距（0 节点，即用户侧与常驻） |\n|---|---|---|")
    for p in procs:
        if p in c0 and p in c1 and n1 != n0:
            k = (c1[p][0] - c0[p][0]) / (n1 - n0)
            w(f"| {'整机' if p == '_system' else p} | {k*100:+.1f} | {c0[p][0] - k*n0:.1f} |")
    k = (c1["_system"][0] - c0["_system"][0]) / (n1 - n0)
    if k > 0:
        cap = n0 + (CORES * 50 - c0["_system"][0]) / k
        w(f"\n整机 CPU 平均 50%（= {CORES*50} 单核百分点）对应约 **{cap:.0f} 个节点**（用户侧不变、按 {a}→{b} 的斜率线性外推）。")
    else:
        w("\n整机 CPU 没有随节点数上升（斜率 ≤ 0），外推不成立：看采样噪声与 steal。")


if __name__ == "__main__":
    if sys.argv[1] == "targets":
        targets(sys.argv[2])
    elif sys.argv[1] == "scale":
        scale(sys.argv[2:])
    else:
        compare(sys.argv[2], sys.argv[3])
