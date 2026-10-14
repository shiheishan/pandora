#!/usr/bin/env python3
"""把渲染计数与四类检查结果拼成一张矩阵。

用法：matrix.py [--no-naive] <渲染输出目录> <结果目录>
读：<out>/fixtures_index.json、<out>/summary.json、<out>/node_config_errors.json，
    <res>/clash.txt（含 clash-premium）、singbox.txt、uri.txt、e2e.txt、
    e2e-mihomo.txt（没跑对应 E2E 时可以没有）。
输出 Markdown 表格与问题明细；有任何 FAIL 时退出码为 1。

--no-naive：构建不带 cronet 时 sing-box 认不得 naive 出站；naive 夹具行的 sing-box
与 sing-box E2E 列显示 -，含 naive 出站的 ALL 行 sing-box 列在仅因 naive 失败时也显示 -，
不算 FAIL。

单元格：
  OK      写出了节点，检查通过（E2E 列是真连通）
  skip    渲染器没写出这个节点（计数为 0），文件本身仍合法
  SKIPPED 客户端明确不支持这个组合（mihomo 列），见明细；不算失败也不算通过
  WARN    静态检查有疑点（字段缺失等），见明细
  FAIL    解析/校验/启动失败，或 E2E 不通，或渲染与 E2E 结论自相矛盾
  -       这一项没跑
"""
import json
import os
import sys

FORMATS = ["clash", "clash-premium", "singbox", "uri"]
SUFFIXES = sorted(FORMATS, key=len, reverse=True)  # clash-premium 要先于 clash 匹配


def load_json(path, default):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return default


def load_checks(path):
    """<文件名> <状态> [说明]，同一文件可以有多行（每条静态问题一行）。"""
    res = {}
    if not os.path.exists(path):
        return res
    with open(path, encoding="utf-8") as f:
        for line in f:
            parts = line.rstrip("\n").split(None, 2)
            if len(parts) < 2:
                continue
            name, status = parts[0], parts[1]
            detail = parts[2] if len(parts) > 2 else ""
            res.setdefault(name, []).append((status, detail))
    return res


def check_cell(entries, count):
    if not entries:
        return "FAIL", ["没有检查结果（文件缺失或检查程序崩溃）"]
    statuses = [s for s, _ in entries]
    fails = [f"{s} {d}".strip() for s, d in entries if s.endswith("FAIL")]
    warns = [d for s, d in entries if s.endswith("WARN")]
    if fails:
        return "FAIL", fails
    if warns:
        return "WARN", warns
    if count == 0 and all(s.endswith("OK") for s in statuses):
        return "skip", []
    return "OK", []


def e2e_cell(line, singbox_count):
    if line is None:
        return "-", []
    status, detail = line
    if status == "NOT-SELECTED":
        return "-", []
    if status == "CONNECT-OK":
        return ("OK", []) if singbox_count > 0 else ("FAIL", ["计数为 0 却连通了：结果对不上"])
    if status == "SKIPPED-IN-SINGBOX":
        if singbox_count == 0:
            return "skip", []
        return "FAIL", ["sing-box 订阅里写了这个节点，E2E 却没找到对应出站"]
    return "FAIL", [f"{status} {detail}".strip()]


def mihomo_cell(line, clash_count):
    if line is None:
        return "-", []
    status, detail = line
    if status == "NOT-SELECTED":
        return "-", []
    if status == "CONNECT-OK":
        return ("OK", []) if clash_count > 0 else ("FAIL", ["计数为 0 却连通了：结果对不上"])
    if status == "SKIPPED-IN-CLASH":
        if clash_count == 0:
            return "skip", []
        return "FAIL", ["Clash 订阅里写了这个节点，E2E 却没找到对应条目"]
    if status == "MIHOMO-UNSUPPORTED":
        return "SKIPPED", [detail] if detail else ["客户端不支持"]
    return "FAIL", [f"{status} {detail}".strip()]


def parse_argv(argv):
    args = list(argv)
    no_naive = False
    while "--no-naive" in args:
        args.remove("--no-naive")
        no_naive = True
    if len(args) != 2:
        return None, None, None
    return args[0], args[1], no_naive


def main():
    out, res, no_naive = parse_argv(sys.argv[1:])
    if out is None:
        print(__doc__, file=sys.stderr)
        return 2
    index = load_json(os.path.join(out, "fixtures_index.json"), {})
    summary = load_json(os.path.join(out, "summary.json"), {})
    ncerr = load_json(os.path.join(out, "node_config_errors.json"), {})
    checks = {}
    for name in ("clash.txt", "singbox.txt", "uri.txt"):
        for fname, entries in load_checks(os.path.join(res, name)).items():
            checks[fname] = entries
    e2e_path = os.path.join(res, "e2e.txt")
    e2e = None
    if os.path.exists(e2e_path):
        e2e = {}
        for fname, entries in load_checks(e2e_path).items():
            e2e[fname] = entries[0]
    mihomo_path = os.path.join(res, "e2e-mihomo.txt")
    mihomo = None
    if os.path.exists(mihomo_path):
        mihomo = {}
        for fname, entries in load_checks(mihomo_path).items():
            mihomo[fname] = entries[0]

    header = [
        "夹具", "协议", "来源", "clash", "premium", "mihomo E2E",
        "sing-box", "sing-box E2E", "uri", "下发",
    ]
    rows, problems = [], []
    failed = False
    skipped_any = False
    tally = {k: {} for k in ["clash", "clash-premium", "mihomo", "singbox", "e2e", "uri"]}

    def bump(col, cell):
        tally[col][cell] = tally[col].get(cell, 0) + 1

    ids = sorted(index)
    for fid in ids + ["ALL", "EMPTY"]:
        meta = index.get(fid, {})
        counts = summary.get(fid, {})
        cells = {}
        for fmt in FORMATS:
            if fmt not in counts:
                # 这次没渲染这个格式（--formats）
                cells[fmt] = "-"
                continue
            if (
                no_naive
                and fid not in ("ALL", "EMPTY")
                and meta.get("type") == "naive"
                and fmt == "singbox"
            ):
                cells[fmt] = "-"
                continue
            cell, why = check_cell(checks.get(f"{fid}.{fmt}"), counts.get(fmt, 0))
            if (
                fid == "ALL"
                and fmt == "singbox"
                and no_naive
                and cell == "FAIL"
                and any("naive" in w.lower() for w in why)
            ):
                cell = "-"
                why = [
                    "整份订阅含 naive 出站，不带 cronet 的构建校验不了整份 sing-box 配置"
                ]
            cells[fmt] = cell
            if fid not in ("ALL", "EMPTY"):
                bump(fmt, cell)
            for w in why:
                label = "sing-box" if fmt == "singbox" else fmt
                problems.append(f"{fid} [{label}] {cell}: {w}")
            if cell == "FAIL":
                failed = True

        if fid in ("ALL", "EMPTY"):
            mcell = "-"
            ecell = "-"
            src = f"{counts.get('singbox', 0)} 个出站" if fid == "ALL" else "空订阅"
            nodecfg = "-"
        else:
            clash_in_counts = "clash" in counts
            if clash_in_counts:
                mcell, mwhy = mihomo_cell(
                    mihomo.get(fid) if mihomo is not None else None,
                    counts.get("clash", 0),
                )
                if mihomo is not None and fid not in mihomo:
                    mcell, mwhy = "FAIL", [
                        "mihomo E2E 没有输出这个夹具（超时、崩溃或二进制核对失败）"
                    ]
                if mihomo is not None and mcell != "-":
                    bump("mihomo", mcell)
                    if mcell == "SKIPPED":
                        skipped_any = True
                for w in mwhy:
                    if mcell == "SKIPPED":
                        problems.append(f"{fid} [mihomo] SKIPPED: {w}")
                    else:
                        problems.append(f"{fid} [mihomo] {mcell}: {w}")
                if mcell == "FAIL":
                    failed = True
            else:
                mcell = "-"

            ecell = "-"
            if no_naive and meta.get("type") == "naive":
                problems.append(
                    f"{fid} [sing-box] -: 构建不带 cronet（--no-naive），"
                    "naive 出站在 sing-box 里校验与真连都没跑"
                )
            else:
                ecell, why = e2e_cell(
                    e2e.get(fid) if e2e is not None else None, counts.get("singbox", 0)
                )
                if e2e is not None and fid not in e2e and ecell == "-":
                    ecell, why = "FAIL", ["E2E 没有输出这个夹具（超时或崩溃）"]
                if e2e is not None and ecell != "-":
                    bump("e2e", ecell)
                for w in why:
                    problems.append(f"{fid} [sing-box e2e] {ecell}: {w}")
                if ecell == "FAIL":
                    failed = True

            valid = meta.get("admin_valid")
            src = meta.get("source", "?") + ("" if valid else " (后台校验不过)")
            if meta.get("source") == "form" and not valid:
                failed = True
                problems.append(f"{fid} [夹具] FAIL: 表单夹具没过后台校验 {meta.get('admin_fields')}")
            nodecfg = "拒绝" if fid in ncerr else "OK"
            if fid in ncerr:
                problems.append(f"{fid} [下发] buildNodeConfigDoc 拒绝: {ncerr[fid]}")

        rows.append([
            fid, meta.get("type", ""), src,
            cells["clash"], cells["clash-premium"], mcell,
            cells["singbox"], ecell, cells["uri"], nodecfg,
        ])

    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        print("| " + " | ".join(str(c) for c in r) + " |")
    print()
    names = {
        "clash": "clash",
        "clash-premium": "premium",
        "mihomo": "mihomo E2E",
        "singbox": "sing-box 校验",
        "e2e": "sing-box E2E",
        "uri": "uri",
    }
    print(f"夹具 {len(ids)} 个。按列汇总（不含 ALL / EMPTY）：")
    for col in ["clash", "clash-premium", "mihomo", "singbox", "e2e", "uri"]:
        if tally[col]:
            print(f"  {names[col]}: " + "，".join(f"{k} {v}" for k, v in sorted(tally[col].items())))
    if problems:
        print()
        print("明细：")
        for p in problems:
            print("  - " + p)
    print()
    conclusion = "有 FAIL" if failed else "全部通过（skip 是渲染器按规则跳过）"
    if skipped_any:
        conclusion += "；SKIPPED 的组合没有真连覆盖"
    print("结论：" + conclusion)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
