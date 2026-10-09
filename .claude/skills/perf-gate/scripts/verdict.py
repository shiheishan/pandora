#!/usr/bin/env python3
"""perf-gate 一页结论：同一台面板机上改前、改后（可带 A/A）各场景的数据 → 回退闸门 + 新标准 → verdict.md。

用法（仓库根目录，只读本机拉回的数据）：
  verdict.py [--title 标题] [--loadtest 二进制] --quiet 改前 改后 [A/A] ... --steady 改前 改后 [A/A] ...
  verdict.py pgss 改前-total.csv 改后-total.csv [N=10]     诊断轮两边 pg_stat_statements 前 N 并排

--quiet 的目录：一场静默拉回的场景目录（pull.sh 的 <场景>/，或任何含 cpu-A/ 与 nodes.json 的目录，向下找）；
  档位按 nodes.json 的 meta.online_ratio 定（0 → A，其余 → B），判定调 `loadtest quiet-report -json`（口径只在 Go 里一份）。
--steady 的目录：prod-retest 的场景目录（panel/ 与 loadgen/），分档线复用 prod-retest/scripts/targets.py 的 classify()。
A/A 是改前版本再跑一场同档，用来算噪声底；没有就按无噪声判，并在结论里写明。
线（用户 2026-10-09；每请求 CPU 取 stack-migration-design.md §6）：p50 ≤ +5%，p99 ≤ +10%，内存 ≤ +10%，
  每请求 CPU ≤ +5%，节流次数不增。落在「线 + 噪声底」以内判「复测」，超出判「不过」。
"""
import argparse, glob, json, os, re, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", "..", "..", ".."))
sys.path.insert(0, os.path.join(HERE, "..", "..", "prod-retest", "scripts"))
import targets as T  # noqa: E402  prod-retest 的分档与稳态读法，不另抄一份

LINE = {"p50": 0.05, "p99": 0.10, "mem": 0.10, "cpu": 0.05}
MIN_P50, MIN_P99 = 100, 1000  # 样本少于此数不判该分位（p99 要至少 10 个尾部样本）
NODE_META = ("nodes", "node_behavior", "online_ratio", "manifest_users", "traffic_mib", "stagger_s", "key_check_interval_s")
USER_META = ("users", "rate_admin", "rate_login", "rate_portal", "rate_sub", "portal_pool", "sub_interval_s", "duration_target_s")
PANEL_DB = ("aegis-public", "aegis-admin", "aegis-node", "postgres", "valkey-server", "redis-server", "valkey")


def find(root, pattern):
    hits = sorted(glob.glob(os.path.join(root, "**", pattern), recursive=True))
    return hits[0] if hits else None


def kv(path):
    if not path or not os.path.exists(path):
        return {}
    return dict(l.strip().split("=", 1) for l in open(path) if "=" in l)


def diag_on(root):
    return bool(find(root, "pgstat-*-total.csv") or find(root, "*-cpu-*.pprof"))


def endpoints(j):
    out = {}
    for e in (j or {}).get("endpoints", []):
        s, kind = T.st(e)
        if s.get("count"):
            out[e["endpoint"]] = (e, s, kind)
    return out


# ---------------------------------------------------------------- 读一场
class Quiet:
    def __init__(self, root, loadtest):
        self.root = root
        cpu_a = find(root, "cpu-A")
        nj = find(root, "nodes.json")
        if not cpu_a or not nj:
            sys.exit(f"{root}：找不到 cpu-A/ 或 nodes.json")
        self.panel = os.path.dirname(cpu_a)
        self.nodes = json.load(open(nj))
        self.meta = self.nodes.get("meta", {})
        self.tier = "A" if float(self.meta.get("online_ratio", 0.3)) == 0 else "B"
        out = subprocess.run([loadtest, "quiet-report", "-dir", self.panel, "-tier", self.tier, "-json"],
                             capture_output=True, text=True)
        if out.returncode != 0:
            sys.exit(f"quiet-report 失败（{self.panel}）：{out.stderr.strip()}")
        self.q = json.loads(out.stdout)
        self.host = kv(os.path.join(self.panel, "host.txt"))
        self.diag = diag_on(root)
        self.eps = endpoints(self.nodes)

    def requests(self):
        return sum(s["count"] for ep, (e, s, k) in self.eps.items() if k == "稳态")

    def metrics(self):
        m = {}
        req = self.requests()
        cpu = self.q["cpu"]
        m[("CPU", "面板 + 数据库每请求 CPU（µs）")] = ("cpu", cpu["panel_db"] / 100 * self.q["window_s"] / req * 1e6 if req else None)
        m[("CPU", "面板 + 数据库（单核 = 100，只列不判）")] = (None, cpu["panel_db"])
        m[("CPU", "nginx（只列不判）")] = (None, cpu["nginx"])
        th = cpu.get("throttled", {})
        m[("节流", "三网关 Δnr_throttled")] = ("throttle", sum(v for k, v in th.items() if k.startswith("aegis-")))
        if self.q.get("mem"):
            mem = self.q["mem"]
            m[("内存", "整机已用 MiB")] = ("mem", mem["used_mib"])
            pss = sum(v for k, v in mem["pss_after_mib"].items() if k in PANEL_DB)
            m[("内存", "面板 + 数据库 PSS MiB（窗口后）")] = ("mem", pss)
        for ep, (e, s, kind) in self.eps.items():
            if kind != "稳态":
                continue
            m[(ep, "p50")] = ("p50" if s["count"] >= MIN_P50 else None, s.get("p50_ms"))
            m[(ep, "p99")] = ("p99" if s["count"] >= MIN_P99 else None, s.get("p99_ms"))
        return m

    def absolute(self):
        rows = []
        cpu, mem = self.q["cpu"], self.q.get("mem")
        rows.append(("面板 + 数据库 CPU", f"{cpu['panel_db']:.2f}", f"≤ {cpu['limit']:g}", cpu["pass"] and not cpu["problems"]))
        rows.append(("nginx CPU", f"{cpu['nginx']:.2f}", f"≤ {cpu['nginx_limit']:g}", cpu["nginx_pass"]))
        if mem:
            rows.append(("整机已用", f"{mem['used_mib']:.0f} MiB", f"≤ {mem['limit_mib']:.0f} MiB", mem["used_pass"]))
            rows.append(("窗口内换入 / 换出", f"{mem['swap_in']} / {mem['swap_out']}", "0 / 0", mem["swap_pass"]))
        else:
            rows.append(("内存", "没有快照", "—", False))
        rows += node_rows(self.eps, self.meta.get("nodes"))
        return rows


class Steady:
    def __init__(self, root):
        self.root = root
        if not os.path.isdir(os.path.join(root, "panel")) or not os.path.isdir(os.path.join(root, "loadgen")):
            sys.exit(f"{root}：不是 prod-retest 的场景目录（缺 panel/ 或 loadgen/）")
        self.nodes = T.load(root, "nodes.json") or {}
        self.users = T.load(root, "users.json") or {}
        self.meta = self.nodes.get("meta", {})
        self.umeta = self.users.get("meta", {})
        self.host = kv(os.path.join(root, "panel", "host.txt"))
        # 稳态场景照 prod-retest 一律带采样器与 pgss、pprof，两边同口径，不单列诊断开关
        self.eps = {**endpoints(self.nodes), **endpoints(self.users)}
        self.cpu, self.sw = T.proc_cpu(root)

    def metrics(self):
        m = {}
        req = sum(s["count"] for ep, (e, s, k) in self.eps.items() if k == "稳态")
        pdb = sum(self.cpu[p][0] for p in ("aegis-public", "aegis-admin", "aegis-node", "postgres", "valkey") if p in self.cpu)
        m[("CPU", "面板 + 数据库每请求 CPU（µs）")] = ("cpu", pdb / 100 * 1800 / req * 1e6 if req else None)
        m[("CPU", "面板 + 数据库（单核 = 100，只列不判）")] = (None, pdb)
        th = T.throttled(self.root)
        if th is not None:
            m[("节流", "三网关 Δnr_throttled")] = ("throttle", sum(th.values()))
        if self.sw:
            m[("内存", "整机已用峰值 MiB")] = ("mem", self.sw["mem_peak_mb"])
        for ep, (e, s, kind) in self.eps.items():
            if kind != "稳态":
                continue
            m[(ep, "p50")] = ("p50" if s["count"] >= MIN_P50 else None, s.get("p50_ms"))
            m[(ep, "p99")] = ("p99" if s["count"] >= MIN_P99 else None, s.get("p99_ms"))
        return m

    def absolute(self):
        rows = node_rows({k: v for k, v in self.eps.items() if k.startswith("node:")}, self.meta.get("nodes"))
        for ep, (e, s, kind) in sorted(self.eps.items()):
            if ep.startswith("node:"):
                continue
            tier, l50, l99 = T.classify(ep, self.meta.get("nodes"))
            if tier == "login" or s.get("p99_ms") is None:
                continue
            ok = s["p99_ms"] <= l99 and (l50 is None or s["p50_ms"] <= l50)
            rows.append((f"{ep}（{tier}）", f"p50 {T.fm(s['p50_ms'])} / p99 {T.fm(s['p99_ms'])}", f"p50 ≤ {l50} / p99 ≤ {l99}", ok))
        if "_system" in self.cpu:
            pct = self.cpu["_system"][0] / T.cores(self.root)
            rows.append(("1 倍负载整机 CPU", f"{pct:.1f}%", "≤ 60%", pct <= 60))
        return rows


def node_rows(eps, nodes):
    rows, s5, to = [], 0, 0
    for ep, (e, s, kind) in sorted(eps.items()):
        s5 += s.get("server_5xx", 0)
        to += T.timeouts(e) if kind == "稳态" else 0
        tier, l50, l99 = T.classify(ep, nodes)
        if tier == "stream" or kind != "稳态":
            continue
        rows.append((f"{ep} p99", f"{T.fm(s['p99_ms'])} ms", f"≤ {l99} ms", s["p99_ms"] <= l99))
    rows.append(("5xx / 超时（超时取全程兜底）", f"{s5} / {to}", "0 / 0", s5 == 0 and to == 0))
    return rows


# ---------------------------------------------------------------- 判
def rel(a, b):
    if a is None or b is None:
        return None
    if a == 0:
        return 0.0 if b == 0 else float("inf")
    return (b - a) / a


def judge(kind, before, after, aa):
    """返回 (变化文字, 噪声文字, 判)。判：过 / 复测 / 不过 / —（不判）。"""
    if kind is None or before is None or after is None:
        d = rel(before, after)
        return ("—" if d is None else f"{d:+.1%}", "—", "—")
    if kind == "throttle":
        noise = abs(aa - before) if aa is not None else None
        d = after - before
        verdict = "过" if d <= 0 else ("复测" if noise is not None and d <= noise else "不过")
        return (f"{d:+d}", "—" if noise is None else f"±{noise}", verdict)
    line = LINE[kind]
    d = rel(before, after)
    noise = abs(rel(before, aa)) if aa is not None else None
    if d <= line:
        verdict = "过"
    elif noise is not None and d <= line + noise:
        verdict = "复测"
    else:
        verdict = "不过"
    ntxt = "—" if noise is None else (f"±{noise:.1%}" + ("（高于线）" if noise > line else ""))
    return (f"{d:+.1%}", ntxt, verdict)


def consistency(kind, runs):
    """同机、同数据、同负载核对：返回 [(项, 结果文字, ok, 硬性)]。"""
    b, a = runs[0], runs[1]
    out = []
    keys = NODE_META
    diff = [k for k in keys if any(r.meta.get(k) != b.meta.get(k) for r in runs[1:])]
    out.append(("节点负载参数（" + "、".join(keys) + "）", "一致" if not diff else "不一致：" + "、".join(f"{k} {b.meta.get(k)}→{a.meta.get(k)}" for k in diff), not diff, True))
    if kind == "steady":
        ud = [k for k in USER_META if any(r.umeta.get(k) != b.umeta.get(k) for r in runs[1:])]
        out.append(("用户负载参数", "一致" if not ud else "不一致：" + "、".join(ud), not ud, True))
    mid = [r.host.get("machine_id") for r in runs]
    if all(mid):
        same = len(set(mid)) == 1
        out.append(("机器指纹（host.txt 的 machine-id、核数、内存）", "同一台" if same else "不是同一台", same and len({(r.host.get('nproc'), r.host.get('mem_total_kb')) for r in runs}) == 1, True))
    else:
        out.append(("机器指纹", "旧数据没有 host.txt，按现场记录核对是同一台", True, False))
    rel_ = [r.host.get("panel_release", "?") for r in runs]
    if all(r.host for r in runs):
        out.append(("面板版本", " → ".join(rel_), rel_[0] != rel_[1] and (len(runs) < 3 or rel_[2] == rel_[0]), True))
    if kind == "quiet":
        w = [round(r.q["window_s"]) for r in runs]
        out.append(("窗口秒数", " / ".join(map(str, w)), max(w) - min(w) <= 5, True))
        mt = {r.q["mem"]["mem_total_kb"] for r in runs if r.q.get("mem")}
        out.append(("MemTotal", " / ".join(map(str, sorted(mt))), len(mt) <= 1, True))
        dg = [r.diag for r in runs]
        same = all(dg) == any(dg)
        out.append(("诊断开关（pgss / pprof）", "全关（正式轮）" if not any(dg) else ("全开：诊断轮，可比但不是正式数" if same else "各场不一致，开销不同不可比"), same, True))
    rq = [r.requests() if kind == "quiet" else sum(s["count"] for e, s, k in r.eps.values() if k == "稳态") for r in runs]
    d = abs(rel(rq[0], rq[1]) or 0)
    out.append(("稳态请求总数（负载一致）", " / ".join(map(str, rq)) + f"（改后 {rel(rq[0], rq[1]):+.1%}）", d <= 0.05, True))
    return out


def page(args, loadtest):
    W = print
    pairs = [("quiet", g) for g in args.quiet or []] + [("steady", g) for g in args.steady or []]
    if not pairs:
        sys.exit("至少给一组 --quiet 或 --steady")
    gate, absl, cons, notes = [], [], [], []
    for kind, dirs in pairs:
        if len(dirs) not in (2, 3):
            sys.exit(f"--{kind} 要 2 或 3 个目录（改前 改后 [A/A]），给了 {len(dirs)} 个")
        runs = [Quiet(d, loadtest) if kind == "quiet" else Steady(d) for d in dirs]
        name = f"静默 {runs[0].tier} 档" if kind == "quiet" else "稳态"
        b, a = runs[0], runs[1]
        aa = runs[2] if len(runs) == 3 else None
        if aa is None:
            notes.append(f"{name}：没有 A/A，噪声底未知，比值线按无噪声判")
        for item, txt, ok, hard in consistency(kind, runs):
            cons.append((name, item, txt, ok, hard))
        mb, ma = b.metrics(), a.metrics()
        maa = aa.metrics() if aa else {}
        for key in mb:
            if key not in ma:
                continue
            k, vb = mb[key]
            va = ma[key][1]
            vaa = maa.get(key, (None, None))[1]
            ch, nz, v = judge(k, vb, va, vaa)
            gate.append((name, key[0], key[1], vb, va, ch, f"{LINE[k]:+.0%}" if k in LINE else ("不增" if k == "throttle" else "—"), nz, v))
        ab = {r[0]: r for r in b.absolute()}
        for item, val, std, ok in a.absolute():
            absl.append((name, item, val, ab.get(item, (None, "—"))[1], std, ok))

    hard_bad = [c for c in cons if c[4] and not c[3]]
    soft_bad = [c for c in cons if not c[4] and not c[3]] + [c for c in cons if c[1].startswith("诊断开关") and "全开" in c[2]]
    for c in cons:
        if c[1].startswith("诊断开关") and "全开" in c[2]:
            notes.append(f"{c[0]}：诊断开关全开（pgss / pprof），两边同口径可比，但不是正式轮")
    fails = [g for g in gate if g[8] == "不过"]
    retest = [g for g in gate if g[8] == "复测"]
    miss = [x for x in absl if not x[5]]
    if hard_bad:
        concl = "不能判"
        why = "同机同数据核对没过：" + "；".join(f"{c[0]} {c[1]} {c[2]}" for c in hard_bad)
    elif fails:
        concl = "退回"
        why = f"{len(fails)} 项比改前变差超过线与噪声底"
    elif retest:
        concl = "复测"
        why = f"{len(retest)} 项落在「线 + 噪声底」以内，加一轮 A/A 或改前再跑一场再判"
    elif miss:
        concl = f"可合，未达新标准 {len(miss)} 项"
        why = "没比改前变差；没达标的项记进 TASKS，若本路 brief 承诺了其中某项则按退回处理"
    else:
        concl, why = "达标", "没比改前变差，新标准全部达到"
    if soft_bad:
        concl += "（非正式数据，仅供参考）"

    W(f"# perf-gate 结论：{args.title}\n")
    W(f"**结论：{concl}**。{why}。\n")
    for n in notes:
        W(f"- {n}")
    W("")
    W("| 场景 | 改前 | 改后 | A/A |\n|---|---|---|---|")
    for kind, dirs in pairs:
        rr = [os.path.relpath(d, REPO) if d.startswith(REPO) else d for d in dirs] + ["—"]
        W(f"| {kind} | `{rr[0]}` | `{rr[1]}` | `{rr[2]}` |")
    W("\n## 1. 同机、同数据、同负载\n")
    W("| 场景 | 项 | 结果 | 判 |\n|---|---|---|---|")
    for name, item, txt, ok, hard in cons:
        warn = item.startswith("诊断开关") and "全开" in txt
        W(f"| {name} | {item} | {txt} | {'⚠️' if warn else ('✅' if ok else ('❌' if hard else '⚠️'))} |")
    W("\n## 2. 回退闸门（改后对改前）\n")
    W(f"线：p50 ≤ +5%、p99 ≤ +10%（样本 ≥ {MIN_P99} 才判 p99、≥ {MIN_P50} 才判 p50）、内存 ≤ +10%、每请求 CPU ≤ +5%、节流不增；"
      "超线但在「线 + 噪声底」以内为复测。延迟是压测机端口径。\n")
    W("| 场景 | 项 | 指标 | 改前 | 改后 | 变化 | 线 | 噪声底 | 判 |\n|---|---|---|---|---|---|---|---|---|")
    order = {"不过": 0, "复测": 1, "过": 2, "—": 3}
    for g in sorted(gate, key=lambda g: (g[0], order[g[8]], g[1], g[2])):
        f = lambda v: "—" if v is None else (T.fm(v) if isinstance(v, float) else str(v))
        W(f"| {g[0]} | {g[1]} | {g[2]} | {f(g[3])} | {f(g[4])} | {g[5]} | {g[6]} | {g[7]} | {g[8]} |")
    W("\n## 3. 新标准（改后绝对值；用户 2026-10-09，`.claude/perf-plan/PLAN.md`「新标准」）\n")
    W("节点、订阅、后台的线本是服务端计时，这里按压测机端判（偏严）；超线不到 2ms 的项写进报告待服务端计时复核。\n")
    W("| 场景 | 项 | 改后 | 改前 | 标准 | 判 |\n|---|---|---|---|---|---|")
    for name, item, val, bval, std, ok in absl:
        W(f"| {name} | {item} | {val} | {bval} | {std} | {'✅' if ok else '❌'} |")
    W("\n## 4. 往返与分配（CI 守卫）\n")
    W("P0 往返记账器与逐路由预算、分配上限守卫还没落地，本节空缺：落地后按 perf-gate SKILL.md「CI 守卫层」核 CI 里的守卫测试，红即退回。")


def pgss(a, b, n):
    import csv
    for f in (a, b):
        rows = list(csv.DictReader(open(f)))
        tot = sum(float(r["total_exec_ms"]) for r in rows)
        print(f"\n### {f}\n\n前 {len(rows)} 条总耗时 {tot/1000:.1f} s，调用 {sum(int(r['calls']) for r in rows)} 次\n")
        print("| # | 语句（截断） | 次数 | 均值 ms | 总 s |\n|---|---|---|---|---|")
        for i, r in enumerate(rows[:n], 1):
            q = re.sub(r"\s+", " ", r["query"])[:90].replace("|", "/")
            print(f"| {i} | `{q}` | {r['calls']} | {float(r['mean_exec_ms']):.3f} | {float(r['total_exec_ms'])/1000:.1f} |")


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "pgss":
        pgss(sys.argv[2], sys.argv[3], int(sys.argv[4]) if len(sys.argv) > 4 else 10)
        return
    ap = argparse.ArgumentParser(description="perf-gate 一页结论")
    ap.add_argument("--title", default="（未命名）")
    ap.add_argument("--loadtest", help="loadtest 二进制；不给就在仓库 panel/ 下 go build 一份到临时目录")
    ap.add_argument("--quiet", nargs="+", action="append", metavar="DIR")
    ap.add_argument("--steady", nargs="+", action="append", metavar="DIR")
    args = ap.parse_args()
    lt = args.loadtest
    if args.quiet and not lt:
        lt = os.path.join(tempfile.mkdtemp(prefix="perf-gate-"), "loadtest")
        subprocess.run(["go", "build", "-o", lt, "./tools/loadtest"], cwd=os.path.join(REPO, "panel"), check=True)
    page(args, lt)


if __name__ == "__main__":
    main()
