#!/usr/bin/env python3
"""把渲染计数与四类检查结果拼成一张矩阵。

用法：matrix.py <渲染输出目录> <结果目录>
读：<out>/fixtures_index.json、<out>/summary.json、<out>/node_config_errors.json，
    <res>/clash.txt（含 clash-premium）、singbox.txt、uri.txt、e2e.txt（没跑 E2E 时可以没有）。
输出 Markdown 表格与问题明细；有任何 FAIL 时退出码为 1。

单元格：
  OK    写出了节点，检查通过（E2E 列是真连通）
  skip  渲染器没写出这个节点（计数为 0），文件本身仍合法
  WARN  静态检查有疑点（字段缺失等），见明细
  FAIL  解析/校验/启动失败，或 E2E 不通，或渲染与 E2E 结论自相矛盾
  -     这一项没跑
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


def main():
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    out, res = sys.argv[1], sys.argv[2]
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

    header = ["夹具", "协议", "来源", "clash", "premium", "sing-box", "sing-box E2E", "uri", "下发"]
    rows, problems = [], []
    failed = False
    tally = {k: {} for k in ["clash", "clash-premium", "singbox", "e2e", "uri"]}

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
            cell, why = check_cell(checks.get(f"{fid}.{fmt}"), counts.get(fmt, 0))
            cells[fmt] = cell
            if fid not in ("ALL", "EMPTY"):
                bump(fmt, cell)
            for w in why:
                problems.append(f"{fid} [{fmt}] {cell}: {w}")
            if cell == "FAIL":
                failed = True
        if fid in ("ALL", "EMPTY"):
            ecell = "-"
            src = f"{counts.get('singbox', 0)} 个出站" if fid == "ALL" else "空订阅"
            nodecfg = "-"
        else:
            ecell, why = e2e_cell(e2e.get(fid) if e2e is not None else None, counts.get("singbox", 0))
            if e2e is not None and ecell == "-":
                ecell, why = "FAIL", ["E2E 没有输出这个夹具（超时或崩溃）"]
            if e2e is not None and ecell != "-":
                bump("e2e", ecell)
            for w in why:
                problems.append(f"{fid} [e2e] {ecell}: {w}")
            if ecell == "FAIL":
                failed = True
            valid = meta.get("admin_valid")
            src = meta.get("source", "?") + ("" if valid else " (后台校验不过)")
            if meta.get("source") == "form" and not valid:
                failed = True
                problems.append(f"{fid} [夹具] FAIL: 表单夹具没过后台校验 {meta.get('admin_fields')}")
            nodecfg = "拒绝" if fid in ncerr else "OK"
            if fid in ncerr:
                problems.append(f"{fid} [下发] BuildNodeConfig 拒绝: {ncerr[fid]}")
        rows.append([fid, meta.get("type", ""), src, cells["clash"], cells["clash-premium"],
                     cells["singbox"], ecell, cells["uri"], nodecfg])

    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        print("| " + " | ".join(str(c) for c in r) + " |")
    print()
    names = {"clash": "clash", "clash-premium": "premium", "singbox": "sing-box 校验", "e2e": "sing-box E2E", "uri": "uri"}
    print(f"夹具 {len(ids)} 个。按列汇总（不含 ALL / EMPTY）：")
    for col in ["clash", "clash-premium", "singbox", "e2e", "uri"]:
        if tally[col]:
            print(f"  {names[col]}: " + "，".join(f"{k} {v}" for k, v in sorted(tally[col].items())))
    if problems:
        print()
        print("明细：")
        for p in problems:
            print("  - " + p)
    print()
    print("结论：" + ("有 FAIL" if failed else "全部通过（skip 是渲染器按规则跳过）"))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
