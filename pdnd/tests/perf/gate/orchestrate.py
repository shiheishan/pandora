#!/usr/bin/env python3
"""节点端性能闸门：一次运行的场景编排（契约 §8）。

用法：
  单机：在测试机上后台执行，结果目录 --out 等于机上 $R/runs/<label>。
  多机：在维护者本机执行，经 ssh 驱动 node / load1 / load2，结果汇集到本机 --out。

运行目录产物（节选）：meta.json、events.json、windows.jsonl、snap/*.json、
lg/<name>.<role>.json、panel/*.json、samples.jsonl、logs/*.log、notes.txt、
result.json（report.py 生成）、done 或 failed。
"""

from __future__ import annotations

import argparse
import json
import re
import secrets
import shlex
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional, Tuple

R = "/root/nodegate"

# §8 参数缺省：single / multi
DEFAULT_PARAMS: Dict[str, Dict[str, Any]] = {
    "single": {
        "users": 5000,
        "delta_size": 500,
        "protocols": "all",
        "delta_hold_conns": 200,
        "idle_conns": 10000,
        "idle_secs": 60,
        "hs_concurrency": 32,
        "hs_secs": 60,
        "hs_max_rate": 0,
        "tput_protocols": "all",
        "tput_mbps": 300,
        "tput_streams": 4,
        "tput_secs": 30,
        "tput1_secs": 20,
        "lat_count": 2000,
        "udp_rates": "50 100 300",
        "udp_secs": 20,
        "fo_conns": 200,
        "fo_down_secs": 60,
        "udp_rmem_8m": 0,
        "settle_secs": 10,
        "port_base": 20000,
    },
    "multi": {
        "users": 5000,
        "delta_size": 500,
        "protocols": "all",
        "delta_hold_conns": 1000,
        "idle_conns": 30000,
        "idle_secs": 120,
        "hs_concurrency": 64,
        "hs_secs": 60,
        "hs_max_rate": 400,
        "tput_protocols": "all",
        "tput_mbps": 2000,
        "tput_streams": 8,
        "tput_secs": 30,
        "tput1_secs": 20,
        "lat_count": 2000,
        "udp_rates": "100 300 1000",
        "udp_secs": 20,
        "fo_conns": 1000,
        "fo_down_secs": 60,
        "udp_rmem_8m": 0,
        "settle_secs": 10,
        "port_base": 20000,
    },
}

INT_PARAM_KEYS = frozenset(
    {
        "users",
        "delta_size",
        "delta_hold_conns",
        "idle_conns",
        "idle_secs",
        "hs_concurrency",
        "hs_secs",
        "hs_max_rate",
        "tput_mbps",
        "tput_streams",
        "tput_secs",
        "tput1_secs",
        "lat_count",
        "udp_secs",
        "fo_conns",
        "fo_down_secs",
        "udp_rmem_8m",
        "settle_secs",
        "port_base",
    }
)

ALL_PROTOCOLS = [
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
UDP_PROTOCOLS = ("hy2", "tuic")


class StepError(Exception):
    """远程步骤失败。"""


class Aborted(Exception):
    """收到 SIGTERM / SIGHUP。故意不继承 StepError：各处轮询会吞掉 StepError，中止信号不能被吞。"""


class Host:
    """一台测试机上的 remote.sh 调用端。"""

    def __init__(self, role: str, alias: Optional[str]) -> None:
        self.role = role
        self.alias = alias

    def remote(
        self,
        *args: str,
        input: Optional[bytes] = None,
        timeout: Optional[float] = None,
    ) -> bytes:
        remote_sh = f"{R}/tools/remote.sh"
        if self.alias is None:
            cmd = ["bash", remote_sh, *args]
        else:
            inner = shlex.join(["bash", remote_sh, *args])
            cmd = [
                "ssh",
                "-o",
                "BatchMode=yes",
                "-o",
                "ServerAliveInterval=15",
                self.alias,
                "--",
                inner,
            ]
        try:
            proc = subprocess.run(
                cmd,
                input=input,
                capture_output=True,
                timeout=timeout,
                check=False,
            )
        except subprocess.TimeoutExpired as e:
            raise StepError(f"角色 {self.role} 超时: {' '.join(args[:4])}…") from e
        if proc.returncode != 0:
            err = proc.stderr.decode("utf-8", errors="replace").strip().splitlines()
            tail = "\n".join(err[-20:]) if err else "(无 stderr)"
            head = " ".join(args[:6])
            raise StepError(f"角色 {self.role} 失败 [{head}…] 退出码 {proc.returncode}:\n{tail}")
        return proc.stdout


_run: Optional["Run"] = None


def log(msg: str) -> None:
    ts = time.strftime("%H:%M:%S", time.localtime())
    print(f"[{ts}] {msg}", flush=True)


def parse_protocols(spec: str) -> List[str]:
    if spec.strip().lower() == "all":
        return list(ALL_PROTOCOLS)
    seen = set()
    out: List[str] = []
    for p in spec.split(","):
        p = p.strip()
        if not p:
            continue
        if p not in ALL_PROTOCOLS:
            raise StepError(f"未知协议: {p}")
        if p not in seen:
            seen.add(p)
            out.append(p)
    order = {x: i for i, x in enumerate(ALL_PROTOCOLS)}
    out.sort(key=lambda x: order[x])
    return out


def merge_params(mode: str, overrides: List[str]) -> Dict[str, Any]:
    params = dict(DEFAULT_PARAMS[mode])
    for item in overrides:
        if "=" not in item:
            raise SystemExit(f"--param 需要 KEY=VALUE: {item}")
        k, v = item.split("=", 1)
        if k in INT_PARAM_KEYS:
            params[k] = int(v)
        else:
            params[k] = v
    return params


class Run:
    """一次闸门运行的状态与步骤。"""

    def __init__(
        self,
        mode: str,
        label: str,
        sha: str,
        tools_id: str,
        out: Path,
        hosts: Dict[str, Host],
        params: Dict[str, Any],
        node_ip: str,
        load1_ip: str,
    ) -> None:
        self.mode = mode
        self.label = label
        self.sha = sha
        self.tools_id = tools_id
        self.out = out
        self.hosts = hosts
        self.params = params
        self.node = hosts["node"]
        self.load1 = hosts["load1"]
        self.load2 = hosts["load2"]
        self.run_dir = f"{R}/runs/{label}"
        self.pdnd = f"{R}/bin/pandora-native-{sha}"
        self.tools = f"{R}/bin/tools-{tools_id}"
        self.events: Dict[str, Any] = {}
        self.loaders = [self.load1] if mode == "single" else [self.load1, self.load2]
        # 实际的机器：单机三个角色是同一台，check / prepare / cleanup 只能各做一次（prepare 拿锁）
        self.machines: Dict[str, Host] = {"node": self.node} if mode == "single" else dict(hosts)
        self.lat_host = self.load1 if mode == "single" else self.load2
        self.protocol_list = parse_protocols(str(params["protocols"]))
        tput_spec = str(params["tput_protocols"])
        tput_set = set(parse_protocols(tput_spec))
        self.tput_protocols = [p for p in self.protocol_list if p in tput_set]
        self.udp_rates = [int(x) for x in str(params["udp_rates"]).split() if x.strip()]
        if mode == "single":
            self.node_addr = "127.0.0.1"
            self.panel_addr = "127.0.0.1:9443"
            self.echo = "127.0.0.1:19000"
            self.udp_echo = "127.0.0.1:19001"
            self.sink = "127.0.0.1:19002"
            self.reality_dest = "127.0.0.1:19443"
        else:
            self.node_addr = node_ip
            self.panel_addr = f"{load1_ip}:9443"
            self.echo = f"{load1_ip}:19000"
            self.udp_echo = f"{load1_ip}:19001"
            self.sink = f"{load1_ip}:19002"
            self.reality_dest = f"{load1_ip}:19443"
        self.started_unix: float = 0.0
        self._cidr: str = ""
        self.load1_ip = load1_ip
        self.out.mkdir(parents=True, exist_ok=True)
        (self.out / "snap").mkdir(exist_ok=True)
        (self.out / "lg").mkdir(exist_ok=True)
        (self.out / "panel").mkdir(exist_ok=True)
        (self.out / "logs").mkdir(exist_ok=True)

    def note(self, text: str) -> None:
        with (self.out / "notes.txt").open("a", encoding="utf-8") as f:
            f.write(text.rstrip() + "\n")

    def save_events(self) -> None:
        with (self.out / "events.json").open("w", encoding="utf-8") as f:
            json.dump(self.events, f, ensure_ascii=False, indent=2)

    def set_event(self, key: str, value: Any) -> None:
        self.events[key] = value
        self.save_events()

    def snap(self, name: str) -> Dict[str, Any]:
        raw = self.node.remote("snap", "--label", self.label)
        line = raw.decode("utf-8", errors="replace").strip().splitlines()[-1]
        data = json.loads(line)
        path = self.out / "snap" / f"{name}.json"
        with path.open("w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False)
        return data

    def window(self, name: str, fn: Callable[[], None]) -> None:
        s0 = self.snap(f"{name}.0")
        fn()
        s1 = self.snap(f"{name}.1")
        rec = {"name": name, "t0": s0["t"], "t1": s1["t"]}
        with (self.out / "windows.jsonl").open("a", encoding="utf-8") as f:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")

    def loadgen(self, host: Host, sub: str, *args: str) -> Dict[str, Any]:
        profile = f"{self.run_dir}/profile/profile.json"
        cmd = self._loadgen_remote_argv(sub, *args)
        raw = host.remote(*cmd, timeout=7200.0)
        lines = [ln for ln in raw.decode("utf-8", errors="replace").splitlines() if ln.strip()]
        if not lines:
            raise StepError(f"loadgen {sub} 无输出（角色 {host.role}）")
        return json.loads(lines[-1])

    def _loadgen_remote_argv(self, sub: str, *args: str) -> Tuple[str, ...]:
        """fg 包装下的 loadgen 参数（含 profile/node/echo）。"""
        profile = f"{self.run_dir}/profile/profile.json"
        return (
            "fg",
            "--label",
            self.label,
            "--",
            f"{self.tools}/loadgen",
            sub,
            "--profile",
            profile,
            "--node",
            self.node_addr,
            "--echo",
            self.echo,
            "--udp-echo",
            self.udp_echo,
            "--sink",
            self.sink,
            *args,
        )

    def loadgen_bin_argv(self, sub: str, *args: str) -> Tuple[str, ...]:
        """bg-start 用的 loadgen 可执行参数（不经 fg）。"""
        profile = f"{self.run_dir}/profile/profile.json"
        return (
            f"{self.tools}/loadgen",
            sub,
            "--profile",
            profile,
            "--node",
            self.node_addr,
            "--echo",
            self.echo,
            "--udp-echo",
            self.udp_echo,
            "--sink",
            self.sink,
            *args,
        )

    def save_lg(self, name: str, role: str, obj: Dict[str, Any]) -> None:
        path = self.out / "lg" / f"{name}.{role}.json"
        with path.open("w", encoding="utf-8") as f:
            json.dump(obj, f, ensure_ascii=False)

    def panel(self, method: str, path: str) -> Dict[str, Any]:
        raw = self.load1.remote(
            "panel-ctl",
            "--label",
            self.label,
            "--method",
            method,
            "--path",
            path,
        )
        return json.loads(raw.decode("utf-8"))

    def save_panel(self, name: str, obj: Dict[str, Any]) -> None:
        with (self.out / "panel" / f"{name}.json").open("w", encoding="utf-8") as f:
            json.dump(obj, f, ensure_ascii=False)

    def wait_until(
        self,
        pred: Callable[[], bool],
        timeout: float,
        interval: float,
        what: str,
    ) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if pred():
                return
            time.sleep(interval)
        raise StepError(f"等待超时: {what}")

    def bg_start(self, host: Host, name: str, *cmd: str) -> None:
        host.remote("bg-start", "--label", self.label, "--name", name, "--", *cmd)

    def bg_stop(self, host: Host, name: str) -> Optional[Dict[str, Any]]:
        host.remote("bg-stop", "--label", self.label, "--name", name, "--wait", "120")
        try:
            raw = host.remote("cat", "--label", self.label, "--path", f"bg/{name}.out")
        except StepError:
            return None
        lines = [ln for ln in raw.decode("utf-8", errors="replace").splitlines() if ln.strip()]
        if not lines:
            return None
        return json.loads(lines[-1])

    def hold_ready(self, host: Host, status_rel: str) -> None:
        """等 loadgen hold 的状态文件（相对运行目录的路径）报 ready。"""
        name = status_rel

        def ready() -> bool:
            try:
                raw = host.remote("cat", "--label", self.label, "--path", status_rel)
                st = json.loads(raw.decode("utf-8"))
                return bool(st.get("ready"))
            except (StepError, json.JSONDecodeError, KeyError):
                return False

        self.wait_until(ready, 300.0, 0.5, f"{host.role} hold {name} ready")

    def parallel(self, fns: List[Callable[[], Any]]) -> List[Any]:
        results: List[Any] = [None] * len(fns)
        errors: List[Optional[BaseException]] = [None] * len(fns)

        def worker(i: int, fn: Callable[[], Any]) -> None:
            try:
                results[i] = fn()
            except BaseException as e:
                errors[i] = e

        threads = [threading.Thread(target=worker, args=(i, fn)) for i, fn in enumerate(fns)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        for e in errors:
            if e is not None:
                raise e
        return results

    def write_meta(self, finished_unix: Optional[float]) -> None:
        meta = {
            "label": self.label,
            "sha": self.sha,
            "tools_id": self.tools_id,
            "mode": self.mode,
            "started_unix": self.started_unix,
            "finished_unix": finished_unix,
            "params": self.params,
            "protocols": self.protocol_list,
        }
        with (self.out / "meta.json").open("w", encoding="utf-8") as f:
            json.dump(meta, f, ensure_ascii=False, indent=2)

    def idle_conns_per_loader(self) -> int:
        total = int(self.params["idle_conns"])
        n = len(self.loaders)
        return total // n

    def tput_mbps_per_loader(self) -> int:
        total = int(self.params["tput_mbps"])
        n = len(self.loaders)
        return total // n

    def wait_connect_status(self, timeout: float = 120.0) -> Dict[str, Any]:
        protos = self.protocol_list

        def ok() -> bool:
            try:
                st = self.panel("GET", "/status")
            except (StepError, json.JSONDecodeError):
                return False
            acks = st.get("roster", {}).get("acks", {})
            cacks = st.get("config_acks", {})
            for p in protos:
                a = acks.get(p, {})
                if a.get("version") != "ng-g0" or not a.get("ok"):
                    return False
                ca = cacks.get(p, {})
                if int(ca.get("health_passed_unix_ms") or 0) <= 0:
                    return False
            return True

        self.wait_until(ok, timeout, 0.5, "各协议 ng-g0 与健康检查")
        return self.panel("GET", "/status")

    def wait_delta_status(self, timeout: float = 60.0) -> Dict[str, Any]:
        protos = self.protocol_list

        def ok() -> bool:
            try:
                st = self.panel("GET", "/status")
            except (StepError, json.JSONDecodeError):
                return False
            acks = st.get("roster", {}).get("acks", {})
            for p in protos:
                a = acks.get(p, {})
                if a.get("version") != "ng-g1" or not a.get("ok"):
                    return False
            return True

        self.wait_until(ok, timeout, 0.5, "各协议 ng-g1")
        return self.panel("GET", "/status")

    def prepare(self) -> None:
        log("场景 0：准备")
        p = self.params
        protos_arg = ",".join(self.protocol_list)
        for role, host in self.machines.items():
            log(f"  check/prepare 角色 {role}")
            host.remote("check", "--mode", self.mode, "--role", role)
            host.remote("prepare", "--label", self.label, "--mode", self.mode, "--role", role)

        log("  node: scalepanel init")
        self.node.remote(
            "fg",
            "--label",
            self.label,
            "--",
            f"{self.tools}/scalepanel",
            "init",
            "--out",
            f"{self.run_dir}/profile",
            "--reality-dest",
            self.reality_dest,
            "--users",
            str(p["users"]),
            "--delta-size",
            str(p["delta_size"]),
            "--protocols",
            protos_arg,
            "--node-cert-dir",
            f"{self.run_dir}/profile/certs",
            timeout=600.0,
        )

        if self.mode == "multi":
            log("  复制 profile 到压测机")
            tarball = self.node.remote("tar", "--label", self.label, "--dir", "profile")
            self.load1.remote(
                "untar",
                "--label",
                self.label,
                "--dir",
                "profile",
                input=tarball,
                timeout=300.0,
            )
            self.load2.remote(
                "untar",
                "--label",
                self.label,
                "--dir",
                "profile",
                input=tarball,
                timeout=300.0,
            )

        token = secrets.token_hex(32)
        token_path = f"{self.run_dir}/token"
        self.load1.remote(
            "fg",
            "--label",
            self.label,
            "--",
            "sh",
            "-c",
            'umask 077; cat > "$1"',
            "_",
            token_path,
            input=token.encode("utf-8"),
        )
        if self.mode == "multi":
            self.node.remote(
                "fg",
                "--label",
                self.label,
                "--",
                "sh",
                "-c",
                'umask 077; cat > "$1"',
                "_",
                token_path,
                input=token.encode("utf-8"),
            )

        log("  load1: target 与 scalepanel")
        # 测试端口只听回环（单机，且在 nodegate 命名空间里）或 load1 的内网地址（多机），不听 0.0.0.0
        listen_host = "127.0.0.1" if self.mode == "single" else self.load1_ip
        self.bg_start(
            self.load1,
            "target",
            f"{self.tools}/target",
            "--tcp-echo",
            f"{listen_host}:19000",
            "--udp-echo",
            f"{listen_host}:19001",
            "--sink",
            f"{listen_host}:19002",
            "--tls-dest",
            f"{listen_host}:19443",
        )
        ca_fp_out = f"{self.run_dir}/ca.fp"
        grpc_listen = f"{listen_host}:9443"
        server_name = listen_host
        self.bg_start(
            self.load1,
            "scalepanel",
            f"{self.tools}/scalepanel",
            "serve",
            "--profile",
            f"{self.run_dir}/profile/profile.json",
            "--grpc-listen",
            grpc_listen,
            "--server-name",
            server_name,
            "--control-listen",
            "127.0.0.1:9180",
            "--token-file",
            token_path,
            "--ca-fingerprint-out",
            ca_fp_out,
        )

        def ca_ready() -> bool:
            try:
                raw = self.load1.remote("cat", "--label", self.label, "--path", "ca.fp")
                return bool(raw.strip())
            except StepError:
                return False

        self.wait_until(ca_ready, 10.0, 0.2, "CA 指纹文件")

        log("  node: pdnd-config 与采样")
        self.node.remote("pdnd-config", "--label", self.label, "--panel", self.panel_addr)
        if self.mode == "multi":
            pb = int(p["port_base"])
            if not self._cidr:
                raise StepError("multi 缺少 cidr")
            self.node.remote(
                "fw-guard",
                "--label",
                self.label,
                "--cidr",
                self._cidr,
                "--ports",
                f"{pb}:{pb + 8}",
                "--ttl",
                "14400",
            )
        if int(p["udp_rmem_8m"]) == 1:
            self.node.remote("rmem-set")
        self.node.remote("sample-start", "--label", self.label)

    def s1_connect(self) -> None:
        log("场景 1：接入")
        protos_csv = ",".join(self.protocol_list)

        def body() -> None:
            log("  enroll")
            self.set_event("enroll_t0", time.time())
            fp_raw = self.load1.remote("cat", "--label", self.label, "--path", "ca.fp")
            fp = fp_raw.decode("utf-8", errors="replace").strip()
            self.node.remote(
                "enroll",
                "--label",
                self.label,
                "--bin",
                self.pdnd,
                "--panel",
                self.panel_addr,
                "--ca-sha256",
                fp,
                "--token-file",
                f"{self.run_dir}/token",
            )
            self.set_event("enroll_t1", time.time())
            log("  pdnd-start")
            t_raw = self.node.remote("pdnd-start", "--label", self.label, "--bin", self.pdnd)
            pdnd_start = float(t_raw.decode("utf-8").strip())
            self.set_event("pdnd_start", pdnd_start)
            since_ms = str(int(pdnd_start * 1000))
            log("  probe")
            probe = self.loadgen(
                self.load1,
                "probe",
                "--protos",
                protos_csv,
                "--since-unix-ms",
                since_ms,
                "--timeout",
                "120s",
                "--name",
                "probe-connect",
            )
            self.save_lg("probe-connect", "load1", probe)
            log("  等名单与健康")
            st = self.wait_connect_status(120.0)
            self.save_panel("connect", st)

        self.window("connect", body)

    def s2_delta(self) -> None:
        log("场景 2：名单增量")
        hold = "hold-delta"
        status_path = f"{self.run_dir}/hold-delta.status"
        conns = str(self.params["delta_hold_conns"])
        self.bg_start(
            self.load1,
            hold,
            *self.loadgen_bin_argv(
                "hold",
                "--proto",
                "vless_vision",
                "--conns",
                conns,
                "--duration",
                "3600s",
                "--status-file",
                status_path,
                "--name",
                "hold-delta",
            ),
        )
        self.hold_ready(self.load1, "hold-delta.status")

        def body() -> None:
            log("  roster/swap")
            swap = self.panel("POST", "/roster/swap")
            self.set_event("delta_sent_unix_ms", int(swap["sent_unix_ms"]))
            st = self.wait_delta_status(60.0)
            self.save_panel("delta", st)

        self.window("delta", body)
        time.sleep(10)
        obj = self.bg_stop(self.load1, hold)
        if obj:
            self.save_lg("hold-delta", "load1", obj)

    def s3_idle(self) -> None:
        log("场景 3：空闲长连接")
        settle = int(self.params["settle_secs"])
        time.sleep(settle)
        base = self.snap("idle.base")
        with (self.out / "snap" / "idle.base.json").open("w", encoding="utf-8") as f:
            json.dump(base, f, ensure_ascii=False)
        per = self.idle_conns_per_loader()
        hold_name = "hold-idle"
        for host in self.loaders:
            status_path = f"{self.run_dir}/{hold_name}.{host.role}.status"
            self.bg_start(
                host,
                f"{hold_name}-{host.role}",
                *self.loadgen_bin_argv(
                    "hold",
                    "--proto",
                    "vless_vision",
                    "--conns",
                    str(per),
                    "--ramp",
                    "1000",
                    "--duration",
                    "3600s",
                    "--status-file",
                    status_path,
                    "--name",
                    f"hold-idle-{host.role}",
                ),
            )
        for host in self.loaders:
            self.hold_ready(host, f"{hold_name}.{host.role}.status")

        def body() -> None:
            time.sleep(int(self.params["idle_secs"]))

        self.window("idle", body)
        for host in self.loaders:
            name = f"{hold_name}-{host.role}"
            obj = self.bg_stop(host, name)
            if obj:
                self.save_lg("hold-idle", host.role, obj)

    def s4_handshake(self) -> None:
        log("场景 4：握手")
        conc = str(self.params["hs_concurrency"])
        dur = f"{int(self.params['hs_secs'])}s"
        max_rate = int(self.params["hs_max_rate"])
        for proto in self.protocol_list:
            log(f"  协议 {proto}")
            win = f"hs-{proto}"

            def run_hs() -> None:
                def one(host: Host) -> Dict[str, Any]:
                    args = [
                        "connrate",
                        "--proto",
                        proto,
                        "--concurrency",
                        conc,
                        "--duration",
                        dur,
                        "--name",
                        f"hs-{proto}-{host.role}",
                    ]
                    if max_rate > 0:
                        args.extend(["--max-rate", str(max_rate)])
                    return self.loadgen(host, *args)

                results = self.parallel([lambda h=host: one(h) for host in self.loaders])
                for host, obj in zip(self.loaders, results):
                    self.save_lg(win, host.role, obj)

            self.window(win, run_hs)
            time.sleep(5)

    def s5_tput(self) -> None:
        log("场景 5：吞吐")
        mbps_each = self.tput_mbps_per_loader()
        streams = str(self.params["tput_streams"])
        dur = f"{int(self.params['tput_secs'])}s"
        dur1 = f"{int(self.params['tput1_secs'])}s"
        for proto in self.tput_protocols:
            log(f"  协议 {proto}")
            win = f"tput-{proto}"

            def run_tput() -> None:
                def one(host: Host) -> Dict[str, Any]:
                    return self.loadgen(
                        host,
                        "tput",
                        "--proto",
                        proto,
                        "--dir",
                        "down",
                        "--rate-mbps",
                        str(mbps_each),
                        "--streams",
                        streams,
                        "--duration",
                        dur,
                        "--name",
                        f"tput-{proto}-{host.role}",
                    )

                results = self.parallel([lambda h=host: one(h) for host in self.loaders])
                for host, obj in zip(self.loaders, results):
                    self.save_lg(win, host.role, obj)

            self.window(win, run_tput)
            time.sleep(3)
            win1 = f"tput1-{proto}"

            def run_tput1() -> None:
                obj = self.loadgen(
                    self.load1,
                    "tput",
                    "--proto",
                    proto,
                    "--dir",
                    "down",
                    "--rate-mbps",
                    "0",
                    "--streams",
                    "1",
                    "--duration",
                    dur1,
                    "--name",
                    f"tput1-{proto}-load1",
                )
                self.save_lg(win1, "load1", obj)

            self.window(win1, run_tput1)
            time.sleep(3)

    def s6_latency(self) -> None:
        log("场景 6：附加延迟")
        count = str(self.params["lat_count"])
        host = self.lat_host
        direct = self.loadgen(
            host,
            "lat",
            "--proto",
            "direct",
            "--count",
            count,
            "--interval",
            "5ms",
            "--name",
            f"lat-direct-{host.role}",
        )
        self.save_lg("lat-direct", host.role, direct)
        for proto in self.protocol_list:
            obj = self.loadgen(
                host,
                "lat",
                "--proto",
                proto,
                "--count",
                count,
                "--interval",
                "5ms",
                "--name",
                f"lat-{proto}-{host.role}",
            )
            self.save_lg(f"lat-{proto}", host.role, obj)

    def s7_udp(self) -> None:
        log("场景 7：UDP")
        udp_protos = [p for p in self.protocol_list if p in UDP_PROTOCOLS]
        dur = f"{int(self.params['udp_secs'])}s"
        for proto in udp_protos:
            for rate in self.udp_rates:
                log(f"  {proto} @ {rate} Mbps")
                win = f"udp-{proto}-{rate}"

                def run_udp() -> None:
                    obj = self.loadgen(
                        self.load1,
                        "udp",
                        "--proto",
                        proto,
                        "--rate-mbps",
                        str(rate),
                        "--duration",
                        dur,
                        "--name",
                        f"udp-{proto}-{rate}-load1",
                    )
                    self.save_lg(win, "load1", obj)

                self.window(win, run_udp)
                time.sleep(3)

    def s8_failover(self) -> None:
        log("场景 8：面板宕机与重启")
        hold = "hold-fo"
        status_path = f"{self.run_dir}/hold-fo.status"
        conns = str(self.params["fo_conns"])
        down_secs = int(self.params["fo_down_secs"])
        protos_csv = ",".join(self.protocol_list)

        def body() -> None:
            self.bg_start(
                self.load1,
                hold,
                *self.loadgen_bin_argv(
                    "hold",
                    "--proto",
                    "vless_vision",
                    "--conns",
                    conns,
                    "--duration",
                    "3600s",
                    "--status-file",
                    status_path,
                    "--name",
                    "hold-fo",
                ),
            )
            self.hold_ready(self.load1, "hold-fo.status")
            down = self.panel("POST", "/fault/down")
            self.set_event("fault_down_unix_ms", int(down["down_unix_ms"]))
            time.sleep(down_secs / 2)
            nc = self.loadgen(
                self.load1,
                "connrate",
                "--proto",
                "vless_vision",
                "--concurrency",
                "4",
                "--duration",
                "10s",
                "--name",
                "fo-newconn-load1",
            )
            self.save_lg("fo-newconn", "load1", nc)
            time.sleep(down_secs / 2)
            self.node.remote("pdnd-stop", "--label", self.label)
            t_raw = self.node.remote("pdnd-start", "--label", self.label, "--bin", self.pdnd)
            restart = float(t_raw.decode("utf-8").strip())
            self.set_event("pdnd_restart", restart)
            since_ms = str(int(restart * 1000))
            probe = self.loadgen(
                self.load1,
                "probe",
                "--protos",
                protos_csv,
                "--since-unix-ms",
                since_ms,
                "--timeout",
                "60s",
                "--name",
                "probe-failover",
            )
            self.save_lg("probe-failover", "load1", probe)
            fo_hold = self.bg_stop(self.load1, hold)
            if fo_hold:
                self.save_lg("hold-fo", "load1", fo_hold)
            before = self.panel("GET", "/status")
            self.save_panel("fo-before-up", before)
            up = self.panel("POST", "/fault/up")
            fault_up_ms = int(up["up_unix_ms"])
            self.set_event("fault_up_unix_ms", fault_up_ms)

            def reconnected() -> bool:
                try:
                    st = self.panel("GET", "/status")
                except (StepError, json.JSONDecodeError):
                    return False
                lh = int(st.get("last_hello_unix_ms") or 0)
                return lh > fault_up_ms

            self.wait_until(reconnected, 120.0, 0.5, "故障恢复后 Hello")
            after = self.panel("GET", "/status")
            self.save_panel("fo-after-up", after)

        self.window("failover", body)

    def fetch_artifacts(self) -> None:
        """§8 第 9 条：取回日志与采样。"""
        label = self.label
        if self.mode == "single" and str(self.out) == f"{R}/runs/{label}":
            for src, dst in (
                ("bg/scalepanel.err", "logs/scalepanel.log"),
                ("bg/target.err", "logs/target.log"),
            ):
                try:
                    raw = self.load1.remote("cat", "--label", label, "--path", src)
                    (self.out / dst).write_bytes(raw)
                except StepError as e:
                    self.note(f"复制 {src} 失败: {e}")
            return
        try:
            raw = self.node.remote("cat", "--label", label, "--path", "samples.jsonl")
            (self.out / "samples.jsonl").write_bytes(raw)
        except StepError as e:
            self.note(f"取回 samples.jsonl 失败: {e}")
        try:
            raw = self.node.remote("cat", "--label", label, "--path", "logs/pdnd.log")
            (self.out / "logs" / "pdnd.log").write_bytes(raw)
        except StepError as e:
            self.note(f"取回 pdnd.log 失败: {e}")
        for src, dst in (
            ("bg/scalepanel.err", "logs/scalepanel.log"),
            ("bg/target.err", "logs/target.log"),
        ):
            try:
                raw = self.load1.remote("cat", "--label", label, "--path", src)
                (self.out / dst).write_bytes(raw)
            except StepError as e:
                self.note(f"复制 {src} 失败: {e}")

    def finish(self) -> None:
        log("场景 9：收尾")
        try:
            self.node.remote("sample-stop", "--label", self.label)
        except StepError as e:
            self.note(f"sample-stop: {e}")
        self.fetch_artifacts()
        self.invoke_report()

    def invoke_report(self) -> None:
        report_py = Path(__file__).with_name("report.py")
        result_path = self.out / "result.json"
        try:
            with result_path.open("w", encoding="utf-8") as outf:
                proc = subprocess.run(
                    [sys.executable, "-I", str(report_py), "--run", str(self.out)],
                    stdout=outf,
                    stderr=subprocess.PIPE,
                    check=False,
                )
            if proc.returncode != 0:
                err = proc.stderr.decode("utf-8", errors="replace").strip()
                self.note(f"report.py 退出码 {proc.returncode}: {err[-500:]}")
        except OSError as e:
            self.note(f"report.py 调用失败: {e}")

    def run_scenarios(self) -> None:
        self.prepare()
        self.s1_connect()
        self.s2_delta()
        self.s3_idle()
        self.s4_handshake()
        self.s5_tput()
        self.s6_latency()
        self.s7_udp()
        self.s8_failover()
        self.finish()


def cleanup_all(run: Run) -> None:
    for role, host in run.machines.items():
        try:
            host.remote("cleanup", "--label", run.label, timeout=300.0)
        except StepError as e:
            run.note(f"cleanup 角色 {role}: {e}")


def _on_sigterm(signum: int, _frame: Any) -> None:
    # 转成异常：走 cmd_run 的失败路径与 finally 里的 cleanup（缺省处理会直接退出、不清理）
    raise Aborted(f"收到信号 {signum}，中止")


def cmd_run(args: argparse.Namespace) -> int:
    global _run
    signal.signal(signal.SIGTERM, _on_sigterm)
    signal.signal(signal.SIGHUP, _on_sigterm)
    label = args.label
    if not re.match(r"^[A-Za-z0-9._-]+$", label):
        print("label 非法", file=sys.stderr)
        return 2
    if not re.match(r"^[0-9a-f]{12}$", args.sha):
        print("sha 须 12 位十六进制", file=sys.stderr)
        return 2
    mode = args.mode
    if mode == "multi":
        missing = [
            n
            for n, v in (
                ("node", args.node),
                ("load1", args.load1),
                ("load2", args.load2),
                ("node-ip", args.node_ip),
                ("load1-ip", args.load1_ip),
                ("cidr", args.cidr),
            )
            if not v
        ]
        if missing:
            print(f"multi 缺少: {', '.join(missing)}", file=sys.stderr)
            return 2
        hosts = {
            "node": Host("node", args.node),
            "load1": Host("load1", args.load1),
            "load2": Host("load2", args.load2),
        }
    else:
        hosts = {
            "node": Host("node", None),
            "load1": Host("load1", None),
            "load2": Host("load2", None),
        }
    params = merge_params(mode, args.param or [])
    out = Path(args.out).resolve()
    run = Run(
        mode,
        label,
        args.sha,
        args.tools_id,
        out,
        hosts,
        params,
        args.node_ip or "",
        args.load1_ip or "",
    )
    run._cidr = args.cidr or ""
    _run = run
    run.started_unix = time.time()
    run.write_meta(None)
    failed_path = out / "failed"
    done_path = out / "done"
    if failed_path.exists():
        failed_path.unlink()
    if done_path.exists():
        done_path.unlink()
    ok = False
    try:
        run.run_scenarios()
        ok = True
        run.write_meta(time.time())
        done_path.touch()
        return 0
    except Exception as e:  # noqa: BLE001 —— 任何异常都要写 failed 并清理
        log(f"失败: {type(e).__name__}: {e}")
        run.note(str(e))
        run.write_meta(time.time())
        failed_path.touch()
        return 1
    finally:
        if not ok:
            try:
                run.node.remote("sample-stop", "--label", run.label)
            except StepError as e:
                run.note(f"sample-stop: {e}")
            try:
                run.fetch_artifacts()
            except Exception as e:
                run.note(f"取回产物: {e}")
            try:
                run.invoke_report()
            except Exception as e:
                run.note(f"report: {e}")
        cleanup_all(run)


def main() -> None:
    parser = argparse.ArgumentParser(description="nodegate 性能闸门编排")
    sub = parser.add_subparsers(dest="command", required=True)
    run_p = sub.add_parser("run", help="执行一次完整运行")
    run_p.add_argument("--mode", choices=("single", "multi"), required=True)
    run_p.add_argument("--label", required=True)
    run_p.add_argument("--sha", required=True)
    run_p.add_argument("--tools-id", required=True)
    run_p.add_argument("--out", required=True)
    run_p.add_argument("--node")
    run_p.add_argument("--load1")
    run_p.add_argument("--load2")
    run_p.add_argument("--node-ip")
    run_p.add_argument("--load1-ip")
    run_p.add_argument("--cidr")
    run_p.add_argument("--param", action="append", default=[])
    run_p.set_defaults(func=cmd_run)
    args = parser.parse_args()
    sys.exit(args.func(args))


if __name__ == "__main__":
    main()
