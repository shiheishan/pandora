#!/usr/bin/env python3
"""report.py 单元测试。"""

from __future__ import annotations

import importlib.util
import json
import math
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

_GATE = Path(__file__).resolve().parent


def _load_report():
    spec = importlib.util.spec_from_file_location("report", _GATE / "report.py")
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


report = _load_report()


class TestHistPercentile(unittest.TestCase):
    def test_known_bucket(self) -> None:
        # 全部落在桶 0：上界 1.01^1 us = 0.00101 ms
        hist = {"0": 100}
        self.assertAlmostEqual(report.hist_percentile(hist, 0.5), math.pow(1.01, 1) / 1000.0)
        self.assertEqual(report.hist_percentile({}, 0.5), 0.0)


class TestMergeSummary(unittest.TestCase):
    def test_two_loaders_rate_and_hist(self) -> None:
        s1 = {
            "n": 10,
            "p50": 1.0,
            "p99": 9.0,
            "mean": 2.0,
            "max": 9.0,
            "hist": {"0": 10},
        }
        s2 = {
            "n": 10,
            "p50": 2.0,
            "p99": 8.0,
            "mean": 4.0,
            "max": 8.0,
            "hist": {"0": 10},
        }
        merged = report.merge_summary([s1, s2])
        self.assertIsNotNone(merged)
        self.assertEqual(merged["n"], 20)
        self.assertAlmostEqual(merged["p50"], report.hist_percentile({"0": 20}, 0.5))


class TestReportIntegration(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _write(self, rel: str, obj: object) -> None:
        p = self.tmp / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        # 字符串原样写（windows.jsonl 这类一行一个 JSON 的文件），其余按 JSON 写
        p.write_text(obj if isinstance(obj, str) else json.dumps(obj), encoding="utf-8")

    def _build_minimal_run(self) -> None:
        """最小运行目录：vless_vision + hy2，单 load1。"""
        self._write(
            "meta.json",
            {
                "label": "t",
                "sha": "abc",
                "tools_id": "tid",
                "mode": "single",
                "protocols": ["vless_vision", "hy2"],
            },
        )
        self._write(
            "events.json",
            {
                "enroll_t0": 100.0,
                "enroll_t1": 103.0,
                "pdnd_start": 200.0,
                "pdnd_restart": 900.0,
                "delta_sent_unix_ms": 5000,
                "fault_down_unix_ms": 8000,
                "fault_up_unix_ms": 86000,
            },
        )
        self._write(
            "windows.jsonl",
            '\n'.join(
                json.dumps({"name": n, "t0": 0.0, "t1": 1.0})
                for n in [
                    "connect",
                    "delta",
                    "idle",
                    "hs-vless_vision",
                    "tput-hy2",
                    "tput1-hy2",
                    "udp-hy2-100",
                    "failover",
                ]
            ),
        )
        for name, t0, t1 in [
            ("connect", 1000.0, 1010.0),
            ("delta", 2000.0, 2010.0),
            ("idle", 3000.0, 3060.0),
            ("hs-vless_vision", 3100.0, 3160.0),
            ("tput-hy2", 4000.0, 4030.0),
            ("tput1-hy2", 4100.0, 4120.0),
            ("udp-hy2-100", 4200.0, 4220.0),
            ("failover", 5000.0, 5100.0),
        ]:
            self._write(f"snap/{name}.0.json", {"t": t0, "cpu_usec": 0, "rss_kb": 1000, "threads": 10, "fds": 100, "tcp_retrans": 0, "udp_rcvbuf_errors": 0})
            self._write(
                f"snap/{name}.1.json",
                {
                    "t": t1,
                    "cpu_usec": 10_000_000 if name.startswith("hs") else 5_000_000,
                    "rss_kb": 1100 if name == "connect" else 2100,
                    "threads": 20,
                    "fds": 200,
                    "tcp_retrans": 3 if name.startswith("tput-") and not name.startswith("tput1") else 0,
                    "udp_rcvbuf_errors": 7 if name.startswith("udp") else 0,
                },
            )
        self._write("snap/idle.base.json", {"t": 2990.0, "rss_kb": 1000, "threads": 10, "fds": 100})
        self._write("snap/connect.1.json", {"t": 1010.0, "rss_kb": 2048})

        self._write("lg/probe-connect.load1.json", {"cmd": "probe", "ok": True, "t_all_s": 12.5})
        self._write(
            "panel/connect.json",
            {
                "first_hello_unix_ms": 205000,
                "roster": {
                    "full_sent_unix_ms": 204000,
                    "users_full_sent": 9,
                    "acks": {
                        "vless_vision": {"unix_ms": 206000},
                        "hy2": {"unix_ms": 207000},
                    },
                },
                "config_acks": {
                    "vless_vision": {"health_passed_unix_ms": 208000},
                    "hy2": {"health_passed_unix_ms": 209000},
                },
            },
        )
        self._write(
            "panel/delta.json",
            {
                "roster": {
                    "users_full_sent": 12,
                    "acks": {
                        "vless_vision": {"unix_ms": 5100},
                        "hy2": {"unix_ms": 5200},
                    },
                },
            },
        )
        self._write("lg/hold-delta.load1.json", {"dropped": 2})
        self._write(
            "lg/hold-idle.load1.json",
            {
                "opened": 100,
                "open_fail": 1,
                "dropped": 0,
                "connect_ms": {"n": 50, "p50": 1.1, "p99": 2.2, "mean": 1.0, "max": 3.0, "hist": {"0": 50}},
                "rtt_ms": {"n": 50, "p50": 0.5, "p99": 1.5, "mean": 0.5, "max": 2.0, "hist": {"0": 50}},
            },
        )
        self._write(
            "lg/hs-vless_vision.load1.json",
            {
                "ok": 100,
                "fail": 0,
                "rate_per_s": 50.0,
                "connect_ms": {"n": 100, "p50": 3.0, "p99": 6.0, "mean": 3.0, "max": 10.0, "hist": {"0": 100}},
            },
        )
        self._write(
            "lg/tput-hy2.load1.json",
            {"mbps": 500.0, "bytes": 1, "secs": 1},
        )
        self._write("lg/tput1-hy2.load1.json", {"mbps": 800.0})
        self._write(
            "lg/lat-direct.load1.json",
            {"rtt_ms": {"n": 200, "p50": 1.0, "p99": 5.0, "mean": 1.0, "max": 10.0, "hist": {"0": 200}}},
        )
        self._write(
            "lg/lat-vless_vision.load1.json",
            {"rtt_ms": {"n": 200, "p50": 3.0, "p99": 8.0, "mean": 3.0, "max": 12.0, "hist": {"0": 200}}},
        )
        self._write(
            "lg/udp-hy2-100.load1.json",
            {"mbps": 95.0, "loss_pct": 1.25, "sent": 1000, "recv": 988},
        )
        self._write(
            "lg/hold-fo.load1.json",
            {"drops_unix_ms": [7500, 8500, 899000, 950000]},
        )
        self._write("lg/fo-newconn.load1.json", {"ok": 8, "fail": 2})
        self._write("lg/probe-failover.load1.json", {"ok": True, "t_all_s": 4.0})
        self._write("panel/fo-before-up.json", {"configs_sent": 1})
        self._write("panel/fo-after-up.json", {"configs_sent": 3, "last_hello_unix_ms": 90000})

    def test_metrics_formulas(self) -> None:
        self._build_minimal_run()
        rep = report.RunReport(self.tmp)
        out = rep.build()
        m = out["metrics"]
        self.assertAlmostEqual(m["roster.connect_s"], 12.5)
        self.assertAlmostEqual(m["roster.full_ack_s"], (207000 - 204000) / 1000.0)
        self.assertAlmostEqual(m["roster.delta_ack_s"], (5200 - 5000) / 1000.0)
        self.assertAlmostEqual(m["roster.delta_full_resent"], 3.0)
        # idle: rss (2100-1000)/100, cpu 60s window cores*100
        self.assertAlmostEqual(m["idle.rss_kb_per_conn"], 11.0)
        w_idle = rep.window("idle")
        self.assertIsNotNone(w_idle)
        self.assertAlmostEqual(m["idle.cpu_pct"], w_idle["cores"] * 100.0)
        # hs cpu_ms = 10000ms / 100 ok = 100ms
        self.assertAlmostEqual(m["handshake.vless_vision.cpu_ms"], 100.0)
        self.assertEqual(out["n"]["handshake.vless_vision.p50"], 100)
        # tput hy2: 30s window, 5e6 usec -> 5000 cpu_ms, cores = 5000/1000/30, mbps 500 -> cores/(mbps/1000)
        w_tput = rep.window("tput-hy2")
        self.assertAlmostEqual(m["tput.hy2.cores_per_gbps"], w_tput["cores"] / (500.0 / 1000.0))
        self.assertAlmostEqual(m["lat.vless_vision.p50_extra_ms"], 2.0)
        self.assertAlmostEqual(m["udp.hy2.100.loss_pct"], 1.25)
        # failover: [8000, 900000) -> 7500 no, 8500 yes, 899000 yes, 950000 no => 2
        self.assertEqual(m["failover.conn_drops"], 2)

    def test_missing_file_omits_key_and_notes(self) -> None:
        self._build_minimal_run()
        (self.tmp / "lg/udp-hy2-100.load1.json").unlink()
        rep = report.RunReport(self.tmp)
        out = rep.build()
        self.assertNotIn("udp.hy2.100.loss_pct", out["metrics"])
        self.assertTrue(any("udp.hy2.100" in n and "缺" in n for n in out["notes"]))

    def test_two_loadgen_merge(self) -> None:
        self._build_minimal_run()
        self._write(
            "lg/hs-vless_vision.load2.json",
            {
                "ok": 50,
                "fail": 0,
                "rate_per_s": 30.0,
                "connect_ms": {"n": 10, "p50": 99.0, "p99": 99.0, "mean": 5.0, "max": 99.0, "hist": {"0": 10}},
            },
        )
        rep = report.RunReport(self.tmp)
        out = rep.build()
        self.assertAlmostEqual(out["metrics"]["handshake.vless_vision.rate"], 80.0)
        merged = report.merge_summary(
            [
                {"n": 100, "hist": {"0": 100}, "mean": 3.0, "max": 10.0},
                {"n": 10, "hist": {"0": 10}, "mean": 5.0, "max": 99.0},
            ]
        )
        self.assertAlmostEqual(
            out["metrics"]["handshake.vless_vision.p50"],
            report.hist_percentile(merged["hist"], 0.5),
        )

    def test_subprocess_cli(self) -> None:
        self._build_minimal_run()
        r = subprocess.run(
            [sys.executable, "-I", str(_GATE / "report.py"), "--run", str(self.tmp)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(r.returncode, 0, r.stderr)
        data = json.loads(r.stdout)
        self.assertIn("meta", data)
        self.assertIn("metrics", data)
        self.assertIn("n", data)
        self.assertIn("notes", data)


if __name__ == "__main__":
    unittest.main()
