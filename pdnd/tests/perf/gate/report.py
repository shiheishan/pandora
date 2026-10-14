#!/usr/bin/env python3
"""读 nodegate 一次运行目录，按设计契约 §9 汇总指标，向 stdout 输出 result.json。"""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional

# 窗口快照里要算差值的字段
_DELTA_FIELDS = (
    "cpu_usec",
    "tcp_retrans",
    "tcp_out_segs",
    "udp_in",
    "udp_in_errors",
    "udp_rcvbuf_errors",
)


class RunReport:
    """单次运行的汇总上下文。"""

    def __init__(self, run_dir: Path) -> None:
        self.run = run_dir.resolve()
        self.metrics: Dict[str, float] = {}
        self.n: Dict[str, int] = {}
        self._calc_notes: List[str] = []
        self._noted_missing: set[str] = set()
        self.meta: Dict[str, Any] = {}
        self.events: Dict[str, Any] = {}
        self._protocols: Optional[List[str]] = None

    def note(self, msg: str) -> None:
        self._calc_notes.append(msg)

    def note_missing(self, rel: str) -> None:
        if rel not in self._noted_missing:
            self._noted_missing.add(rel)
            self.note(f"缺 {rel}")

    def load_json(self, rel: str) -> Optional[Dict[str, Any]]:
        path = self.run / rel
        try:
            with path.open(encoding="utf-8") as f:
                return json.load(f)
        except (FileNotFoundError, json.JSONDecodeError, OSError):
            return None

    def load_json_for_metric(self, rel: str) -> Optional[Dict[str, Any]]:
        data = self.load_json(rel)
        if data is None:
            self.note_missing(rel)
        return data

    def lg(self, name: str) -> List[Dict[str, Any]]:
        lg_dir = self.run / "lg"
        if not lg_dir.is_dir():
            return []
        prefix = name + "."
        paths = sorted(p for p in lg_dir.iterdir() if p.is_file() and p.name.startswith(prefix) and p.suffix == ".json")
        out: List[Dict[str, Any]] = []
        for p in paths:
            try:
                with p.open(encoding="utf-8") as f:
                    out.append(json.load(f))
            except (json.JSONDecodeError, OSError):
                self.note_missing(str(p.relative_to(self.run)))
        return out

    def protocols(self) -> List[str]:
        if self._protocols is not None:
            return self._protocols
        if self.meta.get("protocols"):
            self._protocols = list(self.meta["protocols"])
            return self._protocols
        found: List[str] = []
        pat = re.compile(r"^hs-([^.]+)\.")
        lg_dir = self.run / "lg"
        if lg_dir.is_dir():
            for p in sorted(lg_dir.iterdir()):
                m = pat.match(p.name)
                if m and m.group(1) not in found:
                    found.append(m.group(1))
        self._protocols = found
        return self._protocols

    def snap(self, name: str) -> Optional[Dict[str, Any]]:
        return self.load_json(f"snap/{name}.json")

    def window(self, name: str) -> Optional[Dict[str, Any]]:
        s0 = self.load_json(f"snap/{name}.0.json")
        s1 = self.load_json(f"snap/{name}.1.json")
        if s0 is None or s1 is None:
            return None
        t0, t1 = s0.get("t", 0), s1.get("t", 0)
        secs = t1 - t0
        if secs <= 0:
            return None
        cpu_ms = (s1.get("cpu_usec", 0) - s0.get("cpu_usec", 0)) / 1000.0
        cores = cpu_ms / 1000.0 / secs if secs > 0 else 0.0
        d: Dict[str, float] = {}
        for field in _DELTA_FIELDS:
            d[field] = float(s1.get(field, 0) - s0.get(field, 0))
        return {"secs": secs, "cpu_ms": cpu_ms, "cores": cores, "d": d}

    def set_metric(self, key: str, value: Optional[float], n_key: Optional[str] = None, n_val: Optional[int] = None) -> None:
        if value is None:
            return
        self.metrics[key] = float(value)
        if n_key is not None and n_val is not None:
            self.n[n_key] = int(n_val)

    def build(self) -> Dict[str, Any]:
        meta = self.load_json("meta.json")
        self.meta = meta if meta is not None else {}
        ev = self.load_json("events.json")
        self.events = ev if ev is not None else {}

        self._roster()
        self._idle()
        self._handshake()
        self._tput()
        self._lat()
        self._udp()
        self._failover()

        notes_path = self.run / "notes.txt"
        file_notes: List[str] = []
        if notes_path.is_file():
            file_notes = [ln.strip() for ln in notes_path.read_text(encoding="utf-8").splitlines() if ln.strip()]
        seen = set()
        notes: List[str] = []
        for ln in file_notes + self._calc_notes:
            if ln not in seen:
                seen.add(ln)
                notes.append(ln)
        return {"meta": self.meta, "metrics": self.metrics, "n": self.n, "notes": notes}

    def _roster(self) -> None:
        e = self.events
        if e.get("enroll_t0") is not None and e.get("enroll_t1") is not None:
            self.set_metric("roster.enroll_s", float(e["enroll_t1"]) - float(e["enroll_t0"]))

        probes = self.lg("probe-connect")
        if probes:
            # 合并多台时 t_all_s 取第一台（probe 通常只 load1）；契约写的是 probe-connect 的 t_all_s
            p = probes[0]
            if p.get("ok"):
                self.set_metric("roster.connect_s", p.get("t_all_s"))
            else:
                self.note("roster.connect_s：probe-connect 未成功")
        else:
            self.note("roster.connect_s：缺 probe-connect 结果")

        panel_conn = self.load_json_for_metric("panel/connect.json")
        pdnd_start = e.get("pdnd_start")
        if panel_conn is not None and pdnd_start is not None:
            fh = panel_conn.get("first_hello_unix_ms")
            if fh:
                self.set_metric("roster.hello_s", float(fh) / 1000.0 - float(pdnd_start))
            else:
                self.note("roster.hello_s：缺 first_hello_unix_ms")
            roster = panel_conn.get("roster") or {}
            full_sent = roster.get("full_sent_unix_ms")
            max_ack = _max_roster_ack_ms(panel_conn)
            if full_sent and max_ack is not None:
                self.set_metric("roster.full_ack_s", (max_ack - float(full_sent)) / 1000.0)
            else:
                self.note("roster.full_ack_s：缺 roster.acks 或 full_sent_unix_ms")
            max_health = _max_health_passed_ms(panel_conn)
            if max_health:
                self.set_metric("roster.health_s", max_health / 1000.0 - float(pdnd_start))
            else:
                self.note("roster.health_s：缺 config_acks.health_passed_unix_ms")
        else:
            if panel_conn is None:
                self.note("roster.hello_s：缺 panel/connect.json")
            if pdnd_start is None:
                self.note("roster.hello_s：缺 events.pdnd_start")

        w_conn = self.window("connect")
        if w_conn is not None:
            self.set_metric("roster.cpu_ms", w_conn["cpu_ms"])
        else:
            self.note("roster.cpu_ms：缺窗口 connect 快照")

        s1 = self.snap("connect.1")
        if s1 is not None:
            self.set_metric("roster.rss_mb", s1.get("rss_kb", 0) / 1024.0)
        else:
            self.note("roster.rss_mb：缺 snap/connect.1.json")

        panel_delta = self.load_json_for_metric("panel/delta.json")
        delta_sent = e.get("delta_sent_unix_ms")
        if panel_delta is not None and delta_sent is not None:
            max_ack = _max_roster_ack_ms(panel_delta)
            if max_ack is not None:
                self.set_metric("roster.delta_ack_s", (max_ack - float(delta_sent)) / 1000.0)
            else:
                self.note("roster.delta_ack_s：缺 roster.acks")
        else:
            if panel_delta is None:
                self.note("roster.delta_ack_s：缺 panel/delta.json")
            if delta_sent is None:
                self.note("roster.delta_ack_s：缺 events.delta_sent_unix_ms")

        w_delta = self.window("delta")
        if w_delta is not None:
            self.set_metric("roster.delta_cpu_ms", w_delta["cpu_ms"])
        else:
            self.note("roster.delta_cpu_ms：缺窗口 delta 快照")

        holds = self.lg("hold-delta")
        if holds:
            self.set_metric("roster.delta_conn_drops", sum(h.get("dropped", 0) for h in holds))
        else:
            self.note("roster.delta_conn_drops：缺 hold-delta 结果")

        if panel_conn is not None and panel_delta is not None:
            uf0 = (panel_conn.get("roster") or {}).get("users_full_sent")
            uf1 = (panel_delta.get("roster") or {}).get("users_full_sent")
            if uf0 is not None and uf1 is not None:
                self.set_metric("roster.delta_full_resent", float(uf1) - float(uf0))
            else:
                self.note("roster.delta_full_resent：缺 users_full_sent")
        else:
            self.note("roster.delta_full_resent：缺 panel/connect 或 panel/delta")

    def _idle(self) -> None:
        holds = self.lg("hold-idle")
        if not holds:
            self.note("idle.*：缺 hold-idle 结果")
            return
        opened = sum(h.get("opened", 0) for h in holds)
        self.set_metric("idle.conns", opened)
        self.set_metric("idle.open_fail", sum(h.get("open_fail", 0) for h in holds))
        self.set_metric("idle.drops", sum(h.get("dropped", 0) for h in holds))

        if opened <= 0:
            self.note("idle.rss_kb_per_conn：opened 为 0")
            return

        base = self.snap("idle.base")
        w_idle = self.window("idle")
        s_idle1 = self.load_json("snap/idle.1.json")
        if base is None or s_idle1 is None:
            self.note("idle.rss_kb_per_conn：缺 idle.base 或 idle.1 快照")
        else:
            self.set_metric(
                "idle.rss_kb_per_conn",
                (s_idle1.get("rss_kb", 0) - base.get("rss_kb", 0)) / opened,
            )
            self.set_metric(
                "idle.threads_per_kconn",
                (s_idle1.get("threads", 0) - base.get("threads", 0)) * 1000.0 / opened,
            )
            self.set_metric(
                "idle.fds_per_conn",
                (s_idle1.get("fds", 0) - base.get("fds", 0)) / opened,
            )

        if w_idle is not None:
            self.set_metric("idle.cpu_pct", w_idle["cores"] * 100.0)
        else:
            self.note("idle.cpu_pct：缺窗口 idle 快照")

        cms = merge_summary([h.get("connect_ms") for h in holds])
        if cms:
            self.set_metric("idle.connect_p50", cms.get("p50"), "idle.connect_p50", cms.get("n"))
            self.set_metric("idle.connect_p99", cms.get("p99"), "idle.connect_p99", cms.get("n"))
        rms = merge_summary([h.get("rtt_ms") for h in holds])
        if rms:
            self.set_metric("idle.rtt_p50", rms.get("p50"), "idle.rtt_p50", rms.get("n"))
            self.set_metric("idle.rtt_p99", rms.get("p99"), "idle.rtt_p99", rms.get("n"))

    def _handshake(self) -> None:
        for proto in self.protocols():
            rows = self.lg(f"hs-{proto}")
            if not rows:
                continue
            rate = sum(r.get("rate_per_s", 0) for r in rows)
            self.set_metric(f"handshake.{proto}.rate", rate)
            ok_sum = sum(r.get("ok", 0) for r in rows)
            fail_sum = sum(r.get("fail", 0) for r in rows)
            if ok_sum + fail_sum > 0:
                self.set_metric(f"handshake.{proto}.fail_pct", fail_sum / (ok_sum + fail_sum) * 100.0)
            w = self.window(f"hs-{proto}")
            if w is not None and ok_sum > 0:
                self.set_metric(f"handshake.{proto}.cpu_ms", w["cpu_ms"] / ok_sum)
            cms = merge_summary([r.get("connect_ms") for r in rows])
            if cms:
                self.set_metric(f"handshake.{proto}.p50", cms.get("p50"), f"handshake.{proto}.p50", cms.get("n"))
                self.set_metric(f"handshake.{proto}.p99", cms.get("p99"), f"handshake.{proto}.p99", cms.get("n"))

    def _tput(self) -> None:
        for proto in self.protocols():
            rows = self.lg(f"tput-{proto}")
            if rows:
                mbps = sum(r.get("mbps", 0) for r in rows)
                self.set_metric(f"tput.{proto}.mbps", mbps)
                w = self.window(f"tput-{proto}")
                if w is not None and mbps > 0:
                    self.set_metric(f"tput.{proto}.cores_per_gbps", w["cores"] / (mbps / 1000.0))
                if w is not None:
                    self.set_metric(f"tput.{proto}.retrans", w["d"].get("tcp_retrans", 0))
            rows1 = self.lg(f"tput1-{proto}")
            if rows1:
                mbps1 = sum(r.get("mbps", 0) for r in rows1)
                self.set_metric(f"tput.{proto}.single_mbps", mbps1)
                w1 = self.window(f"tput1-{proto}")
                if w1 is not None and mbps1 > 0:
                    self.set_metric(f"tput.{proto}.single_cores_per_gbps", w1["cores"] / (mbps1 / 1000.0))

    def _lat(self) -> None:
        direct_rows = self.lg("lat-direct")
        direct_p50 = direct_p99 = None
        direct_n = None
        if direct_rows:
            dms = merge_summary([r.get("rtt_ms") for r in direct_rows])
            if dms:
                direct_p50 = dms.get("p50")
                direct_p99 = dms.get("p99")
                direct_n = dms.get("n")
                self.set_metric("lat.direct.p50", direct_p50, "lat.direct.p50", direct_n)
        for proto in self.protocols():
            rows = self.lg(f"lat-{proto}")
            if not rows:
                continue
            lms = merge_summary([r.get("rtt_ms") for r in rows])
            if not lms:
                continue
            n_val = lms.get("n")
            if direct_p50 is not None:
                self.set_metric(
                    f"lat.{proto}.p50_extra_ms",
                    lms.get("p50") - direct_p50,
                    f"lat.{proto}.p50_extra_ms",
                    n_val,
                )
            else:
                self.note(f"lat.{proto}.p50_extra_ms：缺 lat-direct")
            if direct_p99 is not None:
                self.set_metric(
                    f"lat.{proto}.p99_extra_ms",
                    lms.get("p99") - direct_p99,
                    f"lat.{proto}.p99_extra_ms",
                    n_val,
                )

    def _udp_window_names(self) -> List[str]:
        """从 windows.jsonl 与 lg 文件名收集 udp-<P>-<R> 窗口名。"""
        names: List[str] = []
        seen: set[str] = set()
        wl = self.run / "windows.jsonl"
        if wl.is_file():
            for line in wl.read_text(encoding="utf-8").splitlines():
                line = line.strip()
                if not line:
                    continue
                try:
                    w = json.loads(line)
                except json.JSONDecodeError:
                    continue
                n = w.get("name", "")
                if n.startswith("udp-") and n not in seen:
                    seen.add(n)
                    names.append(n)
        pat = re.compile(r"^(udp-[^.]+)\.")
        lg_dir = self.run / "lg"
        if lg_dir.is_dir():
            for p in sorted(lg_dir.iterdir()):
                m = pat.match(p.name)
                if m and m.group(1) not in seen:
                    seen.add(m.group(1))
                    names.append(m.group(1))
        return names

    def _udp(self) -> None:
        for name in self._udp_window_names():
            # name = udp-<proto>-<rate>
            parts = name.split("-")
            if len(parts) < 3:
                continue
            proto = parts[1]
            rate = parts[2]
            rows = self.lg(name)
            if not rows:
                self.note(f"udp.{proto}.{rate}.mbps：缺 lg/{name}.*.json")
                continue
            mbps = sum(r.get("mbps", 0) for r in rows)
            self.set_metric(f"udp.{proto}.{rate}.mbps", mbps)
            # loss_pct 单台原样；多台用合并 sent/recv 重算
            sent = sum(r.get("sent", 0) for r in rows)
            recv = sum(r.get("recv", 0) for r in rows)
            if len(rows) == 1 and rows[0].get("loss_pct") is not None:
                self.set_metric(f"udp.{proto}.{rate}.loss_pct", rows[0]["loss_pct"])
            elif sent > 0:
                self.set_metric(f"udp.{proto}.{rate}.loss_pct", (sent - recv) / sent * 100.0)
            w = self.window(name)
            if w is not None and mbps > 0:
                self.set_metric(f"udp.{proto}.{rate}.cores_per_gbps", w["cores"] / (mbps / 1000.0))
            if w is not None:
                self.set_metric(f"udp.{proto}.{rate}.rcvbuf_errors", w["d"].get("udp_rcvbuf_errors", 0))

    def _failover(self) -> None:
        e = self.events
        fo_holds = self.lg("hold-fo")
        fault_down = e.get("fault_down_unix_ms")
        pdnd_restart = e.get("pdnd_restart")
        if fo_holds and fault_down is not None and pdnd_restart is not None:
            upper = float(pdnd_restart) * 1000.0
            lo = float(fault_down)
            count = 0
            for h in fo_holds:
                for ts in h.get("drops_unix_ms") or []:
                    if lo <= float(ts) < upper:
                        count += 1
            self.set_metric("failover.conn_drops", count)
        else:
            self.note("failover.conn_drops：缺 hold-fo 或 events.fault_down/pdnd_restart")

        nc = self.lg("fo-newconn")
        if nc:
            ok = sum(r.get("ok", 0) for r in nc)
            fail = sum(r.get("fail", 0) for r in nc)
            if ok + fail > 0:
                self.set_metric("failover.new_conn_fail_pct", fail / (ok + fail) * 100.0)

        pf = self.lg("probe-failover")
        if pf and pf[0].get("ok"):
            self.set_metric("failover.cached_start_s", pf[0].get("t_all_s"))
        elif pf:
            self.note("failover.cached_start_s：probe-failover 未成功")

        fault_up = e.get("fault_up_unix_ms")
        after = self.load_json("panel/fo-after-up.json")
        if after is not None and fault_up is not None:
            lh = after.get("last_hello_unix_ms")
            if lh and float(lh) > float(fault_up):
                self.set_metric("failover.session_recover_s", (float(lh) - float(fault_up)) / 1000.0)
        else:
            self.note("failover.session_recover_s：缺 panel/fo-after-up 或 fault_up")

        before = self.load_json("panel/fo-before-up.json")
        if before is not None and after is not None:
            c0, c1 = before.get("configs_sent"), after.get("configs_sent")
            if c0 is not None and c1 is not None:
                self.set_metric("failover.config_resent", float(c1) - float(c0))


def _max_roster_ack_ms(panel: Dict[str, Any]) -> Optional[float]:
    acks = (panel.get("roster") or {}).get("acks") or {}
    if not acks:
        return None
    return max(float(v.get("unix_ms", 0)) for v in acks.values())


def _max_health_passed_ms(panel: Dict[str, Any]) -> Optional[float]:
    cacks = panel.get("config_acks") or {}
    vals = [float(v.get("health_passed_unix_ms", 0)) for v in cacks.values() if v.get("health_passed_unix_ms")]
    return max(vals) if vals else None


def hist_percentile(hist: Dict[str, int], q: float) -> float:
    """与 §5.2 Go HistPercentile 同一公式。"""
    if not hist:
        return 0.0
    total = sum(hist.values())
    if total == 0:
        return 0.0
    target = math.ceil(q * total)
    keys: List[int] = []
    for k in hist:
        try:
            keys.append(int(k))
        except ValueError:
            continue
    keys.sort()
    cum = 0
    for b in keys:
        cum += hist[str(b)]
        if cum >= target:
            return math.pow(1.01, b + 1) / 1000.0
    return 0.0


def merge_summary(sums: List[Optional[Dict[str, Any]]]) -> Optional[Dict[str, Any]]:
    valid = [s for s in sums if s]
    if not valid:
        return None
    if len(valid) == 1:
        return valid[0]
    n = sum(int(s.get("n", 0)) for s in valid)
    hist: Dict[str, int] = {}
    for s in valid:
        for k, v in (s.get("hist") or {}).items():
            hist[k] = hist.get(k, 0) + int(v)
    max_v = max(float(s.get("max", 0)) for s in valid)
    mean = sum(float(s.get("mean", 0)) * int(s.get("n", 0)) for s in valid) / n if n else 0.0
    return {
        "n": n,
        "p50": hist_percentile(hist, 0.5),
        "p99": hist_percentile(hist, 0.99),
        "mean": mean,
        "max": max_v,
        "hist": hist,
    }


def load_json(path: Path) -> Optional[Dict[str, Any]]:
    """模块级工具：读 JSON 文件，失败返回 None（不记 note）。"""
    try:
        with Path(path).open(encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return None


def main() -> None:
    ap = argparse.ArgumentParser(description="汇总 nodegate 运行目录为 result.json")
    ap.add_argument("--run", required=True, help="运行目录")
    args = ap.parse_args()
    rep = RunReport(Path(args.run))
    out = rep.build()
    json.dump(out, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
