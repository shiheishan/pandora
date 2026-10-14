#!/usr/bin/env python3
"""pdnd compare.py 单元测试"""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

# 加载同目录下的 compare.py
_HERE = Path(__file__).resolve().parent
_COMPARE_PATH = _HERE / "compare.py"
_spec = importlib.util.spec_from_file_location("node_gate_compare", _COMPARE_PATH)
if _spec is None or _spec.loader is None:
    raise RuntimeError(f"无法加载 {_COMPARE_PATH}")
node_gate_compare = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(node_gate_compare)

metrics_for = node_gate_compare.metrics_for
lookup = node_gate_compare.lookup
panel_gate_compare = node_gate_compare.panel_gate_compare
judge = panel_gate_compare.judge
verdict = panel_gate_compare.verdict


class TestCompareMetricsFor(unittest.TestCase):
    def test_metrics_for_filtering_and_order(self):
        # 包含每类至少一个、带两个协议（vless_vision 和 hy2）、两档 UDP 速率（50 和 100）、加一个不认识的键
        raw_keys = [
            "unknown.metric.foo",
            "failover.conn_drops",
            "udp.hy2.100.loss_pct",
            "udp.hy2.50.mbps",
            "lat.vless_vision.p50_extra_ms",
            "lat.direct.p50",
            "tput.vless_vision.mbps",
            "handshake.hy2.rate",
            "handshake.vless_vision.p50",
            "idle.conns",
            "roster.enroll_s",
        ]
        base = {"metrics": {k: 1.0 for k in raw_keys[:6]}}
        cand = {"metrics": {k: 2.0 for k in raw_keys[6:]}}

        ms = metrics_for(base, cand)
        keys_out = [m[0] for m in ms]

        # 断言不认识的键被跳过
        self.assertNotIn("unknown.metric.foo", keys_out)

        # 断言场景顺序与协议顺序、UDP速率顺序
        expected_keys = [
            "roster.enroll_s",
            "idle.conns",
            "handshake.vless_vision.p50",
            "handshake.hy2.rate",
            "tput.vless_vision.mbps",
            "lat.direct.p50",
            "lat.vless_vision.p50_extra_ms",
            "udp.hy2.50.mbps",
            "udp.hy2.100.loss_pct",
            "failover.conn_drops",
        ]
        self.assertEqual(keys_out, expected_keys)

        # 核对部分规则的字段 (键, 中文名, 单位, 越大越好, 缺省相对阈值, 最少样本数, 绝对容差)
        m_map = {m[0]: m for m in ms}
        # roster.enroll_s: ('roster.enroll_s', '接入（enroll）用时', 's', False, 0.10, 0, 0.2)
        self.assertEqual(m_map["roster.enroll_s"], ("roster.enroll_s", "接入（enroll）用时", "s", False, 0.10, 0, 0.2))
        # idle.conns: ('idle.conns', '空闲长连接条数', '条', True, 0.0, 0, 0.0)
        self.assertEqual(m_map["idle.conns"], ("idle.conns", "空闲长连接条数", "条", True, 0.0, 0, 0.0))
        # handshake.vless_vision.p50: ('handshake.vless_vision.p50', 'vless_vision 建连 p50', 'ms', False, 0.05, 100, 0.05)
        self.assertEqual(m_map["handshake.vless_vision.p50"], ("handshake.vless_vision.p50", "vless_vision 建连 p50", "ms", False, 0.05, 100, 0.05))
        # udp.hy2.50.mbps: ('udp.hy2.50.mbps', 'hy2 UDP 50M 实际吞吐', 'Mbps', True, 0.05, 0, 1.0)
        self.assertEqual(m_map["udp.hy2.50.mbps"], ("udp.hy2.50.mbps", "hy2 UDP 50M 实际吞吐", "Mbps", True, 0.05, 0, 1.0))


class TestCompareJudgement(unittest.TestCase):
    def test_latency_p50_judge(self):
        m = ("handshake.vless_vision.p50", "vless_vision 建连 p50", "ms", False, 0.05, 100, 0.05)
        # 变慢 10%: base 100ms -> cand 110ms
        # n = 200 -> 退化
        base = {"metrics": {m[0]: 100.0}, "n": {m[0]: 200}}
        cand = {"metrics": {m[0]: 110.0}, "n": {m[0]: 200}}
        row = judge(m, base, cand, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "退化")

        # n = 50 -> 不能判（样本 50/50 < 100）
        base_low_n = {"metrics": {m[0]: 100.0}, "n": {m[0]: 50}}
        cand_low_n = {"metrics": {m[0]: 110.0}, "n": {m[0]: 50}}
        row = judge(m, base_low_n, cand_low_n, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "不能判")
        self.assertIn("样本 50/50 < 100", row.get("why", ""))

    def test_tput_judge(self):
        # 越大越好：吞吐
        m = ("tput.vless_vision.mbps", "vless_vision 定速实际吞吐", "Mbps", True, 0.05, 0, 1.0)
        base = {"metrics": {m[0]: 100.0}}
        # 下降 10% (90.0) -> 退化
        cand_down = {"metrics": {m[0]: 90.0}}
        row = judge(m, base, cand_down, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "退化")

        # 上升 10% (110.0) -> 改善
        cand_up = {"metrics": {m[0]: 110.0}}
        row = judge(m, base, cand_up, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "改善")

    def test_count_judge(self):
        # 计数类 0 -> 1 -> 退化（阈值 0、容差 0）
        m = ("roster.delta_full_resent", "增量后被迫重发全量", "次", False, 0.0, 0, 0.0)
        base = {"metrics": {m[0]: 0.0}}
        cand = {"metrics": {m[0]: 1.0}}
        row = judge(m, base, cand, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "退化")

    def test_aa_noise(self):
        # 给 aa：基线与 aa 差 8% 时，变化 12% 的 p50 判持平（阈值放宽到 16%），并且 threshold == 0.16
        m = ("handshake.vless_vision.p50", "vless_vision 建连 p50", "ms", False, 0.05, 100, 0.05)
        base = {"metrics": {m[0]: 100.0}, "n": {m[0]: 200}}
        aa = {"metrics": {m[0]: 108.0}, "n": {m[0]: 200}}  # 差 8%
        cand = {"metrics": {m[0]: 112.0}, "n": {m[0]: 200}}  # 变化 12%
        row = judge(m, base, cand, aa, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "持平")
        self.assertAlmostEqual(row["threshold"], 0.16, places=4)

    def test_abs_tol_and_missing_data(self):
        # 两边都在绝对容差内 -> 持平
        m = ("lat.direct.p50", "直连回显 p50（参照）", "ms", False, 0.05, 100, 0.01)
        base = {"metrics": {m[0]: 0.100}, "n": {m[0]: 200}}
        cand = {"metrics": {m[0]: 0.108}, "n": {m[0]: 200}}  # 差 0.008 <= 0.01，虽然相对变化 8% > 5%
        row = judge(m, base, cand, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "持平")

        # 缺键 -> 不能判（缺数据）
        cand_empty = {"metrics": {}}
        row = judge(m, base, cand_empty, None, lookup_fn=lookup)
        self.assertEqual(row["verdict"], "不能判")
        self.assertEqual(row.get("why"), "缺数据")


class TestVerdict(unittest.TestCase):
    def test_overall_verdicts(self):
        # 有退化 -> 退回
        rows_worse = [
            {"key": "handshake.vless_vision.p50", "verdict": "改善"},
            {"key": "tput.vless_vision.mbps", "verdict": "退化"},
        ]
        overall, worse, better = verdict(rows_worse, targets=["handshake."])
        self.assertEqual(overall, "退回")
        self.assertEqual(len(worse), 1)

        # 有 target 没改善 -> 未达预期
        rows_no_target_hit = [
            {"key": "idle.conns", "verdict": "改善"},
            {"key": "handshake.vless_vision.p50", "verdict": "持平"},
        ]
        overall, worse, better = verdict(rows_no_target_hit, targets=["handshake."])
        self.assertEqual(overall, "未达预期")

        # 有 target 且命中改善 -> 可合
        rows_target_hit = [
            {"key": "handshake.vless_vision.p50", "verdict": "改善"},
        ]
        overall, worse, better = verdict(rows_target_hit, targets=["handshake."])
        self.assertEqual(overall, "可合")
        self.assertEqual(len(better), 1)


class TestEndToEndSubprocess(unittest.TestCase):
    def test_e2e_json_output(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            tmp_path = Path(tmp_dir)
            base_data = {
                "meta": {"label": "a", "sha": "x", "tools_id": "t"},
                "metrics": {"roster.enroll_s": 1.0, "tput.vless_vision.mbps": 100.0},
                "n": {},
                "notes": [],
            }
            cand_data = {
                "meta": {"label": "b", "sha": "y", "tools_id": "t"},
                "metrics": {"roster.enroll_s": 0.5, "tput.vless_vision.mbps": 120.0},  # 均改善
                "n": {},
                "notes": [],
            }
            dir_a = tmp_path / "a"
            dir_b = tmp_path / "b"
            dir_a.mkdir()
            dir_b.mkdir()
            (dir_a / "result.json").write_text(json.dumps(base_data), encoding="utf-8")
            (dir_b / "result.json").write_text(json.dumps(cand_data), encoding="utf-8")

            proc = subprocess.run(
                [
                    sys.executable,
                    "-I",
                    str(_COMPARE_PATH),
                    "--root",
                    tmp_dir,
                    "a",
                    "b",
                    "--target",
                    "tput.",
                    "--json",
                ],
                capture_output=True,
                text=True,
            )
            self.assertEqual(proc.returncode, 0, msg=f"stdout: {proc.stdout}, stderr: {proc.stderr}")
            res = json.loads(proc.stdout)
            self.assertEqual(res["overall"], "可合")
            keys = [r["key"] for r in res["rows"]]
            self.assertIn("roster.enroll_s", keys)
            self.assertIn("tput.vless_vision.mbps", keys)


if __name__ == "__main__":
    unittest.main()
