"""collect.py 的解析单测：python3 -m unittest -v test_collect.py（在本目录下跑）。"""

import importlib.util
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location("nodegate_collect", Path(__file__).with_name("collect.py"))
collect = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(collect)

SNMP = """Ip: Forwarding DefaultTTL InReceives
Ip: 1 64 100
Icmp: InMsgs InErrors
Icmp: 3 0
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 10 20 0 0 3 1000 2000 7 0 0 0
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti MemErrors
Udp: 500 1 9 400 8 0 0 0 0
UdpLite: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti MemErrors
UdpLite: 0 0 0 0 0 0 0 0 0
"""


class ParseSnmpTest(unittest.TestCase):
    def test_columns(self):
        self.assertEqual(
            collect.parse_snmp_text(SNMP),
            {"tcp_retrans": 7, "tcp_out_segs": 2000, "udp_in": 500, "udp_in_errors": 9, "udp_rcvbuf_errors": 8},
        )

    def test_udplite_not_mixed_in(self):
        # UdpLite 组的同名列不能覆盖 Udp 的值
        out = collect.parse_snmp_text(SNMP)
        self.assertEqual(out["udp_in"], 500)

    def test_empty(self):
        self.assertEqual(set(collect.parse_snmp_text("").values()), {0})


class SnapshotNoUnitTest(unittest.TestCase):
    def test_missing_pid_zero(self):
        self.assertEqual(collect.count_fds(0), 0)
        self.assertEqual(collect.parse_snmp(0)["tcp_retrans"], 0)


if __name__ == "__main__":
    unittest.main()
