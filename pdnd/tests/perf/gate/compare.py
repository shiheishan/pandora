#!/usr/bin/env python3
"""节点端性能闸门对比工具：对比两次闸门运行（基线 vs 候选），按指标给出判定。

用法：
  compare.py [--root 目录] <基线> <候选> [--aa <基线第二遍>] [--target <指标前缀>] [--json]

  <基线>、<候选> 是结果目录名（相对 --root）或目录路径。
  --aa      同一提交的第二遍运行：两遍基线之差当噪声，阈值取「缺省阈值」与「2 倍噪声」中大的那个。
  --target  这次优化要改善的指标（键的前缀，如 handshake.、tput.）：没有一项改善就判「未达预期」。

判定规则（阈值、--aa 取缺省阈值与 2 倍噪声中大的、样本不足判「不能判」、总评 退回 / 未达预期 / 可合）复用面板闸门。
阈值口径：延迟 p50 +5%（n≥100）、p99 +10%（n≥1000）、内存 +10%、每次操作 CPU +5%、计数类 0 阈值、0 附近有绝对容差。
指标来自 report.py 的 result.json（metrics 与 n 两个字典）。
"""

from __future__ import annotations

import argparse
import importlib.util
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

# 加载面板 compare.py
_PANEL_COMPARE_PATH = Path(__file__).resolve().parents[4] / "panel" / "tests" / "perf" / "gate" / "compare.py"
if not _PANEL_COMPARE_PATH.exists():
    sys.stderr.write(f"错误：找不到面板 compare.py 文件：{_PANEL_COMPARE_PATH}\n")
    sys.exit(2)

_spec = importlib.util.spec_from_file_location("panel_gate_compare", _PANEL_COMPARE_PATH)
if _spec is None or _spec.loader is None:
    sys.stderr.write(f"错误：无法加载面板 compare.py 模块规格：{_PANEL_COMPARE_PATH}\n")
    sys.exit(2)

panel_gate_compare = importlib.util.module_from_spec(_spec)
try:
    _spec.loader.exec_module(panel_gate_compare)
except Exception as err:
    sys.stderr.write(f"错误：加载面板 compare.py 失败：{err}\n")
    sys.exit(2)

Metric = Tuple[str, str, str, bool, float, int, float]

SCENARIO_ORDER = ["roster", "idle", "handshake", "tput", "lat", "udp", "failover"]

PROTO_ORDER = [
    "direct",
    "vless_vision",
    "vless_grpc",
    "trojan_tcp",
    "trojan_ws",
    "ss",
    "vmess_ws",
    "hy2",
    "tuic",
    "anytls",
]

ROSTER_RULES: List[Tuple[str, str, str, bool, float, int, float]] = [
    ("roster.enroll_s", "接入（enroll）用时", "s", False, 0.10, 0, 0.2),
    ("roster.connect_s", "冷启动到全部入站可连", "s", False, 0.10, 0, 0.3),
    ("roster.hello_s", "冷启动到 Hello", "s", False, 0.10, 0, 0.2),
    ("roster.full_ack_s", "全量名单下发到全部回执", "s", False, 0.10, 0, 0.1),
    ("roster.health_s", "冷启动到全部入站健康", "s", False, 0.10, 0, 0.5),
    ("roster.cpu_ms", "接入阶段节点 CPU", "ms", False, 0.10, 0, 20.0),
    ("roster.rss_mb", "接入后节点 RSS", "MB", False, 0.10, 0, 5.0),
    ("roster.delta_ack_s", "增量名单回执用时", "s", False, 0.10, 0, 0.05),
    ("roster.delta_cpu_ms", "增量名单节点 CPU", "ms", False, 0.10, 0, 10.0),
    ("roster.delta_conn_drops", "增量期间长连接断开", "条", False, 0.0, 0, 0.0),
    ("roster.delta_full_resent", "增量后被迫重发全量", "次", False, 0.0, 0, 0.0),
]

IDLE_RULES: List[Tuple[str, str, str, bool, float, int, float]] = [
    ("idle.conns", "空闲长连接条数", "条", True, 0.0, 0, 0.0),
    ("idle.open_fail", "空闲长连接开失败", "条", False, 0.0, 0, 0.0),
    ("idle.drops", "空闲长连接断开", "条", False, 0.0, 0, 0.0),
    ("idle.rss_kb_per_conn", "每条空闲连接 RSS", "KB", False, 0.10, 0, 1.0),
    ("idle.threads_per_kconn", "每千条空闲连接线程数", "", False, 0.10, 0, 1.0),
    ("idle.fds_per_conn", "每条空闲连接 fd", "", False, 0.05, 0, 0.05),
    ("idle.cpu_pct", "空闲长连接心跳期 CPU（单核 %）", "%", False, 0.10, 0, 0.5),
    ("idle.connect_p50", "长连接建连 p50", "ms", False, 0.05, 100, 0.1),
    ("idle.connect_p99", "长连接建连 p99", "ms", False, 0.10, 1000, 0.5),
    ("idle.rtt_p50", "长连接心跳往返 p50", "ms", False, 0.05, 100, 0.05),
    ("idle.rtt_p99", "长连接心跳往返 p99", "ms", False, 0.10, 1000, 0.2),
]

HANDSHAKE_SUBMETRICS = [
    ("rate", "{P} 每秒新建", "/s", True, 0.05, 0, 0.0),
    ("cpu_ms", "{P} 每次新建节点 CPU", "ms", False, 0.05, 0, 0.002),
    ("p50", "{P} 建连 p50", "ms", False, 0.05, 100, 0.05),
    ("p99", "{P} 建连 p99", "ms", False, 0.10, 1000, 0.2),
    ("fail_pct", "{P} 新建失败率", "%", False, 0.0, 0, 0.1),
]

TPUT_SUBMETRICS = [
    ("mbps", "{P} 定速实际吞吐", "Mbps", True, 0.05, 0, 1.0),
    ("cores_per_gbps", "{P} 定速每 Gbps 核数", "核", False, 0.05, 0, 0.01),
    ("retrans", "{P} 定速 TCP 重传", "段", False, 0.20, 0, 100.0),
    ("single_mbps", "{P} 单流满速", "Mbps", True, 0.10, 0, 5.0),
    ("single_cores_per_gbps", "{P} 单流每 Gbps 核数", "核", False, 0.05, 0, 0.01),
]

LAT_SUBMETRICS = [
    ("p50_extra_ms", "{P} 附加延迟 p50", "ms", False, 0.05, 100, 0.02),
    ("p99_extra_ms", "{P} 附加延迟 p99", "ms", False, 0.10, 1000, 0.1),
]

UDP_SUBMETRICS = [
    ("mbps", "{P} UDP {R}M 实际吞吐", "Mbps", True, 0.05, 0, 1.0),
    ("loss_pct", "{P} UDP {R}M 丢包", "%", False, 0.20, 0, 0.5),
    ("cores_per_gbps", "{P} UDP {R}M 每 Gbps 核数", "核", False, 0.05, 0, 0.01),
    ("rcvbuf_errors", "{P} UDP {R}M 收包缓冲丢包", "包", False, 0.20, 0, 100.0),
]

FAILOVER_RULES: List[Tuple[str, str, str, bool, float, int, float]] = [
    ("failover.conn_drops", "面板宕机期间长连接断开", "条", False, 0.0, 0, 0.0),
    ("failover.new_conn_fail_pct", "面板宕机期间新建失败率", "%", False, 0.0, 0, 0.0),
    ("failover.cached_start_s", "面板宕机时重启靠缓存起来", "s", False, 0.10, 0, 0.5),
    ("failover.session_recover_s", "面板恢复后会话恢复用时", "s", False, 0.10, 0, 5.0),
    ("failover.config_resent", "会话恢复后重发配置", "次", False, 0.0, 0, 0.0),
]


def lookup(res: Dict[str, Any], key: str) -> Tuple[Optional[float], Optional[int]]:
    metrics = res.get("metrics") or {}
    ns = res.get("n") or {}
    return metrics.get(key), ns.get(key)


def _proto_sort_key(proto: str) -> Tuple[int, int, str]:
    if proto in PROTO_ORDER:
        return (0, PROTO_ORDER.index(proto), "")
    return (1, 0, proto)


def _parse_metric_key(key: str) -> Optional[Tuple[Tuple[int, Tuple[int, int, str], float, int], Metric]]:
    """解析指标键，返回 (排序键, Metric)；无法匹配规则时返回 None。"""
    parts = key.split(".")
    scen = parts[0]
    if scen not in SCENARIO_ORDER:
        return None
    scen_idx = SCENARIO_ORDER.index(scen)

    if scen == "roster":
        rule_map = {r[0]: (idx, r) for idx, r in enumerate(ROSTER_RULES)}
        if key in rule_map:
            idx, r = rule_map[key]
            sort_key = (scen_idx, (0, 0, ""), 0.0, idx)
            return (sort_key, r)
        return None

    if scen == "idle":
        rule_map = {r[0]: (idx, r) for idx, r in enumerate(IDLE_RULES)}
        if key in rule_map:
            idx, r = rule_map[key]
            sort_key = (scen_idx, (0, 0, ""), 0.0, idx)
            return (sort_key, r)
        return None

    if scen == "handshake":
        if len(parts) == 3:
            _, proto, sub = parts
            sub_map = {s[0]: (idx, s) for idx, s in enumerate(HANDSHAKE_SUBMETRICS)}
            if sub in sub_map:
                sub_idx, s = sub_map[sub]
                label = s[1].format(P=proto)
                m: Metric = (key, label, s[2], s[3], s[4], s[5], s[6])
                sort_key = (scen_idx, _proto_sort_key(proto), 0.0, sub_idx)
                return (sort_key, m)
        return None

    if scen == "tput":
        if len(parts) == 3:
            _, proto, sub = parts
            sub_map = {s[0]: (idx, s) for idx, s in enumerate(TPUT_SUBMETRICS)}
            if sub in sub_map:
                sub_idx, s = sub_map[sub]
                label = s[1].format(P=proto)
                m = (key, label, s[2], s[3], s[4], s[5], s[6])
                sort_key = (scen_idx, _proto_sort_key(proto), 0.0, sub_idx)
                return (sort_key, m)
        return None

    if scen == "lat":
        # 按段比较（写成 key == "..." 会被 gitleaks 的 generic-api-key 规则误报）
        if parts == ["lat", "direct", "p50"]:
            m = ("lat.direct.p50", "直连回显 p50（参照）", "ms", False, 0.05, 100, 0.01)
            sort_key = (scen_idx, _proto_sort_key("direct"), 0.0, 0)
            return (sort_key, m)
        if len(parts) == 3:
            _, proto, sub = parts
            sub_map = {s[0]: (idx, s) for idx, s in enumerate(LAT_SUBMETRICS)}
            if sub in sub_map:
                sub_idx, s = sub_map[sub]
                label = s[1].format(P=proto)
                m = (key, label, s[2], s[3], s[4], s[5], s[6])
                # 在 lat 场景中，direct 排 0，其余附加延迟排在后
                sort_key = (scen_idx, _proto_sort_key(proto), 0.0, sub_idx + 1)
                return (sort_key, m)
        return None

    if scen == "udp":
        if len(parts) == 4:
            _, proto, rate_str, sub = parts
            try:
                rate_val = float(rate_str)
            except ValueError:
                return None
            sub_map = {s[0]: (idx, s) for idx, s in enumerate(UDP_SUBMETRICS)}
            if sub in sub_map:
                sub_idx, s = sub_map[sub]
                # 格式化中的速率如 50、100
                rate_display = f"{int(rate_val)}" if rate_val.is_integer() else f"{rate_val}"
                label = s[1].format(P=proto, R=rate_display)
                m = (key, label, s[2], s[3], s[4], s[5], s[6])
                sort_key = (scen_idx, _proto_sort_key(proto), rate_val, sub_idx)
                return (sort_key, m)
        return None

    if scen == "failover":
        rule_map = {r[0]: (idx, r) for idx, r in enumerate(FAILOVER_RULES)}
        if key in rule_map:
            idx, r = rule_map[key]
            sort_key = (scen_idx, (0, 0, ""), 0.0, idx)
            return (sort_key, r)
        return None

    return None


def metrics_for(base: Dict[str, Any], cand: Dict[str, Any]) -> List[Metric]:
    keys = set((base.get("metrics") or {}).keys()) | set((cand.get("metrics") or {}).keys())
    parsed: List[Tuple[Tuple[int, Tuple[int, int, str], float, int], Metric]] = []
    for k in keys:
        res = _parse_metric_key(k)
        if res is not None:
            parsed.append(res)
    parsed.sort(key=lambda x: x[0])
    return [m for _, m in parsed]


def main() -> None:
    ap = argparse.ArgumentParser(add_help=False)
    ap.add_argument("base", nargs="?")
    ap.add_argument("cand", nargs="?")
    ap.add_argument("--root", default=".")
    known, _ = ap.parse_known_args()
    if known.base and known.cand:
        try:
            root = Path(known.root)
            base_data = panel_gate_compare.load(root, known.base)
            cand_data = panel_gate_compare.load(root, known.cand)
            b_tid = (base_data.get("meta") or {}).get("tools_id")
            c_tid = (cand_data.get("meta") or {}).get("tools_id")
            if b_tid and c_tid and b_tid != c_tid:
                sys.stderr.write(f"注意：两次运行的压测工具版本不同（{b_tid} vs {c_tid}），结果不能直接比\n")
        except Exception:
            pass

    panel_gate_compare.run(None, metrics_for, lookup, __doc__)


if __name__ == "__main__":
    main()
