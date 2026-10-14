#!/usr/bin/env bash
# 评估「内鬼检测能不能正常运行」（nodesim 节点夹具）：滥用指订阅分享、批量刷号、
# 一条订阅多人同时在线。审计无哈希链，不在本脚本范围。
#
# 做法：后台 API 建池、服务器、节点，签 bootstrap-token 后 nodesim 真接入再 activate；strict 集合由 worker
# devices:strict 推进，脚本轮询 nodesim status 里的 proxy_uuid 名单；在线设备按 SQL 去重后的设备键行数核对
# （HMAC 与 IPv6 /64 归一由 Go 单测覆盖）。
#
# 仍直接写库：subscription_fetch_log 的 fetched_at 回拨（append-only + session_replication_role）；
# node_alive_ips 造在线（脚本拿不到 HMAC 密钥）；strict 前刷新 DEVICE 用例的 last_seen_at（两轮间隔防窗口过期）。
#
# 两类结论：机制断言 [ OK ]/[FAIL]（错了退出 1）；检测效果 [EVAL]（误判不判红）。
# 来源用 X-Real-IP（gin TrustedPlatform=X-Real-IP）。地址用文档保留段，每轮每用例互不相撞。
#
# 环境变量：ADM、PUB、PSQL、ADMIN_EMAIL、ADMIN_PASS（必填）；
#   RISK_E2E_DISPOSABLE=YES_DELETE_FIXTURES（必填）；NODESIM（必填，可执行 nodesim）；
#   RISK_ADMIN_BUDGET（可选，后台每分钟调用预算，缺省 100）。
# 注册、登录、停用留审计，只许一次性库；收尾恢复设备策略、归档套餐、停 nodesim、节点 retire。
set -euo pipefail

export ADM=${ADM:-http://127.0.0.1:9001}
export PUB=${PUB:-http://127.0.0.1:9000}
export PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
export ADMIN_EMAIL=${ADMIN_EMAIL:-}
export ADMIN_PASS=${ADMIN_PASS:-}
export RISK_E2E_DISPOSABLE=${RISK_E2E_DISPOSABLE:-}
export NODESIM=${NODESIM:-}
export RISK_ADMIN_BUDGET=${RISK_ADMIN_BUDGET:-100}

command -v python3 >/dev/null 2>&1 || { echo "  [FATAL] 需要 python3" >&2; exit 1; }

exec python3 - <<'PY'
import csv, hashlib, io, ipaddress, json, os, re, secrets, signal, subprocess, sys, time
import urllib.error, urllib.parse, urllib.request
from datetime import datetime, timedelta, timezone

# ============================================================================
#  输出与计数：[ OK ] / [FAIL] 进 run-smoke-e2e.sh 的统计，[EVAL] 只是评估结论
# ============================================================================
PASS = FAIL = 0

def ok(msg):
    global PASS
    PASS += 1
    print(f"  [ OK ] {msg}", flush=True)

def bad(msg, detail=""):
    global FAIL
    FAIL += 1
    print(f"  [FAIL] {msg}", flush=True)
    if detail:
        print(f"         {detail}", flush=True)

def check(cond, msg, detail=""):
    (ok if cond else (lambda m: bad(m, detail)))(msg)
    return bool(cond)

def sec(title):
    print(f"\n=== {title} ===", flush=True)

def ev(msg):
    print(f"  [EVAL] {msg}", flush=True)

class Fatal(Exception):
    def __init__(self, msg, detail=""):
        super().__init__(msg)
        self.msg, self.detail = msg, detail

def _on_term(*_):
    raise SystemExit(124)  # timeout 发的 SIGTERM 也要走 finally 恢复设置

signal.signal(signal.SIGTERM, _on_term)

# ============================================================================
#  环境与一次性库守卫
# ============================================================================
ADM = os.environ["ADM"].rstrip("/")
PUB = os.environ["PUB"].rstrip("/")
PSQL = os.environ["PSQL"]
NODESIM = os.environ["NODESIM"]
ADMIN_EMAIL = os.environ["ADMIN_EMAIL"]
ADMIN_PASS = os.environ["ADMIN_PASS"]
SIM_PROC = None
SIM_TMP = None
UUID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
STAMP = f"{int(time.time()) % 100000000:08d}"
UA_BROWSER = "Mozilla/5.0 (risk-e2e)"
UA = {
    "A": "ClashMetaForAndroid/2.10.1.Meta",
    "M": "clash-verge/v1.7.7",
    "I": "Shadowrocket/2070 CFNetwork/1494.0.7 Darwin/23.4.0",
    "S": "sing-box 1.10.1",
    "W": "v2rayN/6.42",
}

def is_loopback_origin(url):
    u = urllib.parse.urlsplit(url)
    return (u.scheme in ("http", "https") and u.hostname in ("127.0.0.1", "::1")
            and u.port is not None and not u.username and u.path in ("", "/")
            and not u.query and not u.fragment)

# ============================================================================
#  HTTP 与 SQL
# ============================================================================
_opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
_admin_calls = []
ADMIN_BUDGET = int(os.environ.get("RISK_ADMIN_BUDGET", "100"))  # 后台按 IP 每分钟缺省 120，CI 会放宽并调大预算

def _throttle_admin():
    while True:
        now = time.monotonic()
        while _admin_calls and now - _admin_calls[0] > 61:
            _admin_calls.pop(0)
        if len(_admin_calls) < ADMIN_BUDGET:
            _admin_calls.append(now)
            return
        time.sleep(61 - (now - _admin_calls[0]) + 0.2)

def http(method, url, body=None, headers=None, token=None):
    h = {"Accept": "application/json"}
    h.update(headers or {})
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        h["Content-Type"] = "application/json"
    if token:
        h["Authorization"] = "Bearer " + token
    if url.startswith(ADM):
        _throttle_admin()
    req = urllib.request.Request(url, data=data, method=method, headers=h)
    try:
        with _opener.open(req, timeout=30) as r:
            code, raw = r.status, r.read()
    except urllib.error.HTTPError as e:
        code, raw = e.code, e.read()
    except (urllib.error.URLError, OSError) as e:
        raise Fatal(f"{method} {url.split('?')[0]} 连不上", str(e))
    text = raw.decode("utf-8", "replace")
    try:
        js = json.loads(text) if text else None
    except ValueError:
        js = None
    return code, js, text

def sql(q):
    r = subprocess.run([PSQL, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-tA", "-F", "|", "-c", q],
                       stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
    if r.returncode != 0:
        raise Fatal("SQL 执行失败", (r.stderr or "").strip()[:400])
    return [l for l in r.stdout.splitlines() if l.strip()]

def need_uuid(v, what):
    if not isinstance(v, str) or not UUID_RE.match(v):
        raise Fatal(f"{what} 不是 UUID", repr(v)[:80])
    return v

class Admin:
    def __init__(self):
        self.token, self.at = None, 0.0

    def login(self):
        code, js, _ = http("POST", ADM + "/v1/auth/login",
                           {"email": ADMIN_EMAIL, "password": ADMIN_PASS})
        if code != 200 or not js or not js.get("access_token"):
            raise Fatal(f"后台管理员登录失败（HTTP {code}）")
        self.token, self.at = js["access_token"], time.monotonic()

    def call(self, method, path, body=None, idem=None, expect=(200, 201)):
        # 登录签发的令牌自带 15 分钟重认证窗口；过了 10 分钟就重登，免得高风险接口回 403
        if time.monotonic() - self.at > 600:
            self.login()
        headers = {"Idempotency-Key": idem} if idem else {}
        code, js, text = http(method, ADM + path, body, headers, self.token)
        if expect and code not in expect:
            raise Fatal(f"{method} {path.split('?')[0]} 返回 {code}", text[:300])
        return code, js

ADMIN = Admin()

# 批量生成是后台任务（w5account）：提交回 202 与任务，aegis-admin 的 worker 逐个生成；
# 轮询进度到 succeeded（有上限），再下载结果 CSV（邮箱,初始密码；要近期重认证，Admin.call
# 每 10 分钟重登一次，登录令牌自带 15 分钟重认证窗口）
GEN_TIMEOUT = 300

def generate_users(count, prefix, idem):
    _, job = ADMIN.call("POST", "/v1/users/bulk/generate",
                        {"count": count, "email_prefix": prefix, "email_domain": "example.test",
                         "reason": "风控评估脚本预制的一次性账号"},
                        idem=idem, expect=(202,))
    job_id = need_uuid((job or {}).get("id"), "批量生成任务 id")
    deadline = time.monotonic() + GEN_TIMEOUT
    while True:
        _, job = ADMIN.call("GET", f"/v1/users/bulk/generate/jobs/{job_id}", expect=(200,))
        status = (job or {}).get("status")
        if status == "succeeded":
            break
        if status == "failed":
            raise Fatal("批量生成任务失败", str((job or {}).get("error"))[:200])
        if time.monotonic() > deadline:
            raise Fatal(f"批量生成任务 {GEN_TIMEOUT} 秒内没完成",
                        f"status={status} completed={(job or {}).get('completed')}/{count}")
        time.sleep(1)
    ADMIN.call("GET", "/v1/me", expect=(200,))  # 过了 10 分钟就先重登，下载要近期重认证
    code, _, text = http("GET", ADM + f"/v1/users/bulk/generate/jobs/{job_id}/result",
                         headers={"Accept": "text/csv"}, token=ADMIN.token)
    if code != 200:
        raise Fatal(f"下载批量生成结果返回 {code}", text[:300])
    rows = list(csv.reader(io.StringIO(text.lstrip("\ufeff"))))
    if not rows or rows[0] != ["邮箱", "初始密码"]:
        raise Fatal("批量生成结果的表头不对", repr(rows[:1])[:200])
    return [{"email": r[0], "password": r[1]} for r in rows[1:] if len(r) == 2]

# ============================================================================
#  用例集。每条写明标签与理由；ip 键按轮次与用例序号展开成互不相撞的地址：
#    v4:h → 198.(18+轮).序号.h   cg:h → 100.(64+轮).序号.h（运营商 CGNAT 段）
#    v6:s:h → 2001:db8:轮:(序号<<8|s)::/64 里的第 h 个临时地址
#  拉取写成 (第几天前, ip 键, 客户端)；0 = 今天
# ============================================================================
def share(n, limit=3):
    """主人在家拉一次，再分给 n-1 个人各自在自己的网络拉一次。"""
    uas = "AMISW"
    return [(0, "v4:1", "M")] + [(0, f"v4:{i + 1}", uas[i % 5]) for i in range(1, n)]

def slow_share(friends):
    """主人每天在家拉；每天一个新朋友拉一次，摊在 friends 天里。"""
    days = friends + 1
    out = [(d, "v4:1", "M") for d in range(days - 1, -1, -1)]
    out += [(k - 1, f"v4:{10 + k}", "AISW"[k % 4]) for k in range(1, friends + 1)]
    return sorted(out, key=lambda x: -x[0])

FETCH_CASES = [
    dict(id="F01", grp="T", label=0, limit=3, title="单人三设备·同一家宽",
         why="手机、电脑、平板都在家，出口只有一个",
         fetch=[(0, "v4:1", "A"), (0, "v4:1", "M"), (0, "v4:1", "I")], probe=True),
    dict(id="F02", grp="H", label=0, limit=3, title="单人三设备·家/公司/移动网各一",
         why="三台设备分处三个网络，来源数正好等于设备上限",
         fetch=[(0, "v4:1", "I"), (0, "v4:2", "M"), (0, "cg:1", "A")]),
    dict(id="F03", grp="T", label=0, limit=3, title="通勤日·手机换了两个移动出口",
         why="手机在家→地铁（移动网换出口两次）→公司，电脑在公司，平板在家；一个人三台设备",
         fetch=[(0, "v4:1", "A"), (0, "cg:1", "A"), (0, "cg:2", "A"), (0, "v4:2", "A"),
                (0, "v4:2", "M"), (0, "v4:1", "I")]),
    dict(id="F04", grp="H", label=0, limit=3, title="出差一周",
         why="同一个人一周里在家、机场、两家酒店、客户公司和移动网之间换，任一天不超过两个来源",
         fetch=[(6, "v4:1", "A"), (5, "v4:2", "A"), (5, "cg:1", "A"), (4, "v4:3", "M"),
                (3, "v4:3", "A"), (3, "cg:2", "A"), (2, "v4:4", "M"), (1, "v4:5", "A"),
                (0, "v4:5", "M"), (0, "cg:3", "A")]),
    dict(id="F05", grp="T", label=0, limit=3, title="IPv6 临时地址轮换·三台设备",
         why="家宽 IPv6 下三台设备各拉两次，每次用新的临时地址（同一 /64）",
         fetch=[(0, "v6:1:1", "A"), (0, "v6:1:2", "A"), (0, "v6:1:3", "M"),
                (0, "v6:1:4", "M"), (0, "v6:1:5", "I"), (0, "v6:1:6", "I")]),
    dict(id="F06", grp="H", label=0, limit=3, title="IPv6 轮换·两台设备加一台 IPv4",
         why="手机和电脑走 IPv6 各换两次地址，平板只有 IPv4",
         fetch=[(0, "v6:1:1", "A"), (0, "v6:1:2", "A"), (0, "v6:1:3", "M"),
                (0, "v6:1:4", "M"), (0, "v4:1", "I")]),
    dict(id="F07", grp="T", label=0, limit=10, title="家庭套餐·设备分在多处",
         why="上限 10 的家庭套餐，家人分别在家、学校、两处公司和移动网",
         fetch=[(0, "v4:1", "A"), (0, "v4:1", "M"), (0, "v4:1", "I"), (0, "v4:2", "A"),
                (0, "v4:3", "W"), (0, "v4:4", "M"), (0, "cg:1", "A"), (0, "cg:2", "I"),
                (0, "cg:3", "A")]),
    dict(id="F08", grp="H", label=0, limit=1, title="单设备套餐·手机整天在移动网",
         why="上限 1，只有一部手机，两次拉取落在不同的 CGNAT 出口",
         fetch=[(0, "cg:1", "A"), (0, "cg:2", "A")]),
    dict(id="F09", grp="H", label=0, limit=8, title="家庭套餐·六处来源",
         why="上限 8 的家庭套餐，六个家人各在一处",
         fetch=[(0, "v4:1", "A"), (0, "v4:2", "I"), (0, "v4:3", "M"), (0, "v4:4", "W"),
                (0, "cg:1", "A"), (0, "cg:2", "I")]),
    dict(id="F10", grp="T", label=0, limit=3, title="一周通勤",
         why="工作日家和公司来回，手机在移动网每天落在不同出口；一个人三台设备",
         fetch=[(4, "v4:1", "M"), (4, "v4:2", "M"), (4, "cg:1", "A"), (3, "v4:2", "M"),
                (3, "cg:2", "A"), (2, "v4:1", "I"), (2, "cg:3", "A"), (1, "v4:2", "M"),
                (1, "cg:4", "A"), (0, "v4:1", "I"), (0, "cg:5", "A")]),
    dict(id="S02", grp="H", label=0, limit=3, title="分享给 1 人（共 2 个来源）",
         why="来源数在设备上限之内，按产品规则不算超用", fetch=share(2)),
    dict(id="S03", grp="T", label=0, limit=3, title="分享给 2 人（共 3 个来源）",
         why="来源数正好等于设备上限，不算超用", fetch=share(3)),
    dict(id="S04", grp="T", label=1, limit=3, title="分享给 3 人（共 4 个来源）",
         why="来源数超过上限 1 个，刚过线", fetch=share(4)),
    dict(id="S05", grp="H", label=1, limit=3, title="分享给 4 人（共 5 个来源）",
         why="来源数超过上限 2 个", fetch=share(5)),
    dict(id="S08", grp="T", label=1, limit=3, title="分享给 7 人（共 8 个来源）",
         why="小群分享", fetch=share(8)),
    dict(id="S15", grp="H", label=1, limit=3, title="链接挂进群里（共 15 个来源）",
         why="大群分享，一天内冒出一串互不相干的地址", fetch=share(15)),
    dict(id="SL6", grp="T", label=1, limit=3, title="慢速分享·6 人摊在 6 天",
         why="每天只多一个朋友拉一次，一周内共 7 个来源，远超上限 3", fetch=slow_share(6)),
    dict(id="SL4", grp="H", label=1, limit=3, title="慢速分享·4 人摊在 4 天",
         why="一周内共 5 个来源，超过上限 3", fetch=slow_share(4)),
    dict(id="SU1", grp="H", label=1, limit=1, title="单设备套餐分给 2 人",
         why="上限 1，一天里 3 个来源", fetch=share(3)),
    dict(id="SU5", grp="T", label=1, limit=5, title="五设备套餐分给 6 人",
         why="上限 5，一天里 7 个来源", fetch=share(7)),
]

DEVICE_CASES = [
    dict(id="V01", grp="T", label=0, limit=3, title="三台设备同时在线·三处网络",
         why="正常多设备", ips=["v4:1", "v4:2", "cg:1"]),
    dict(id="V02", grp="H", label=0, limit=3, title="三台设备·手机刚换网旧地址未过期",
         why="IP 抖动：同一部手机在窗口里占两个地址", ips=["v4:1", "v4:2", "v4:3", "cg:1"]),
    dict(id="V03", grp="T", label=1, limit=3, title="自己三台加分享 1 人同时在线",
         why="同时 4 个来源，超过上限 1 个", ips=["v4:1", "v4:2", "v4:3", "v4:4"]),
    dict(id="V04", grp="H", label=1, limit=3, title="自己两台加分享 4 人",
         why="同时 6 个来源", ips=["v4:1", "v4:2", "v4:3", "v4:4", "v4:5", "v4:6"]),
    dict(id="V05", grp="T", label=1, limit=3, title="自己三台加分享 5 人",
         why="同时 8 个来源", ips=[f"v4:{i}" for i in range(1, 9)]),
    dict(id="V06", grp="H", label=1, limit=1, title="单设备套餐同时三处在线",
         why="上限 1，同时 3 个来源", ips=["v4:1", "v4:2", "cg:1"]),
    dict(id="V07", grp="T", label=0, limit=3, title="三台设备都在一个家宽 NAT 后",
         why="NAT 后多台设备只露出一个地址，正常", ips=["v4:1"]),
    dict(id="V08", grp="H", label=1, limit=3, title="分给室友·同一 NAT 下 5 台设备",
         why="共享发生在同一出口后面，同时 5 台设备只露出一个地址", ips=["v4:1"]),
    dict(id="V09", grp="T", label=0, limit=3, title="IPv6 轮换时刻·三台设备",
         why="手机和电脑各有新旧两个临时地址同时在窗口里，平板走 IPv4",
         ips=["v6:1:1", "v6:1:2", "v6:1:3", "v6:1:4", "v4:1"]),
    dict(id="V10", grp="H", label=0, limit=3, title="手机换网又换 IPv6 地址",
         why="手机两个临时地址加一个移动网地址，电脑与平板各一个",
         ips=["v6:1:1", "v6:1:2", "cg:1", "v6:2:1", "v4:1"]),
]

# 成员写成 (方式, ip 键)：reg = 真实注册流程（user.registered 带来源），login = 后台预制账号在此登录
def members(spec):
    out = []
    for how, key, n in spec:
        out += [(how, key)] * n
    return out

CLUSTER_CASES = [
    dict(id="C01", grp="T", label=0, title="家庭两人共用家宽",
         why="夫妻各一个账号", members=members([("login", "v4:1", 2)])),
    dict(id="C02", grp="H", label=0, title="宿舍三人",
         why="室友在宿舍注册并使用", members=members([("reg", "v4:1", 3)])),
    dict(id="C03", grp="T", label=0, title="公司出口·八名同事",
         why="两人在公司注册，六人在家注册、在公司登录",
         members=members([("reg", "v4:1", 2), ("login", "v4:1", 6)])),
    dict(id="C04", grp="H", label=0, title="网吧出口·五位顾客",
         why="一人在网吧注册，四人在网吧登录", members=members([("reg", "v4:1", 1), ("login", "v4:1", 4)])),
    dict(id="C05", grp="T", label=0, title="运营商 CGNAT·四个陌生人",
         why="移动网 CGNAT 出口被互不相识的用户共用", members=members([("reg", "cg:1", 2), ("login", "cg:1", 2)])),
    dict(id="C06", grp="H", label=0, title="运营商 CGNAT·六个陌生人",
         why="同上，人更多", members=members([("reg", "cg:1", 1), ("login", "cg:1", 5)])),
    dict(id="R01", grp="T", label=1, title="同一 IP 批量注册 6 个",
         why="几分钟内同一出口注册一串账号", members=members([("reg", "v4:1", 6)])),
    dict(id="R02", grp="H", label=1, title="同一 IP 批量注册 10 个",
         why="同上，规模更大", members=members([("reg", "v4:1", 10)])),
    dict(id="R03", grp="T", label=1, title="同一 IP 小批量注册 3 个",
         why="小批量刷号（例如薅新人优惠）", members=members([("reg", "v4:1", 3)])),
    dict(id="R04", grp="H", label=1, title="同一 IP 小批量注册 4 个",
         why="同上", members=members([("reg", "v4:1", 4)])),
    dict(id="R05", grp="T", label=1, title="同一 /24 每号换一个 IP 注册 6 个",
         why="刷号者轮换同一网段内的地址", members=[("reg", f"v4:{i}") for i in range(1, 7)]),
    dict(id="R06", grp="H", label=1, title="同一 IPv6 /64 每号换地址注册 5 个",
         why="刷号者在自己的 /64 里随便换地址", members=[("reg", f"v6:1:{i}") for i in range(1, 6)]),
]

ALL_CASES = FETCH_CASES + DEVICE_CASES + CLUSTER_CASES
for i, c in enumerate(ALL_CASES, start=1):
    c["idx"] = i  # 用例序号决定地址段；三族合计不超过 255
assert len(ALL_CASES) < 255

def ip_of(key, rnd, idx):
    kind, *rest = key.split(":")
    if kind == "v4":
        return f"198.{18 + rnd}.{idx}.{int(rest[0])}"
    if kind == "cg":
        return f"100.{64 + rnd}.{idx}.{int(rest[0])}"
    if kind == "v6":
        s, h = int(rest[0]), int(rest[1])
        return str(ipaddress.IPv6Address(
            f"2001:db8:{rnd:x}:{(idx << 8) | s:x}:{0x5e00 + h:x}:{(h * 40503) & 0xffff:x}:"
            f"{(h * 9973 + 7) & 0xffff:x}:{(h * 31337 + 11) & 0xffff:x}"))
    raise ValueError(key)

def source_key(ip):
    """派生特征：IPv6 按 /64 合并成一个来源，IPv4 原样。"""
    a = ipaddress.ip_address(ip)
    if a.version == 6:
        return str(ipaddress.IPv6Network(f"{ip}/64", strict=False))
    return ip

def parse_ts(s):
    s = s.replace("Z", "+00:00")
    m = re.match(r"^(.*T\d\d:\d\d:\d\d)(\.\d+)?(.*)$", s)
    if m:
        frac = (m.group(2) or "")[:7]
        s = m.group(1) + frac + m.group(3)
    return datetime.fromisoformat(s)

# ============================================================================
#  造数助手
# ============================================================================
def user_login(email, password, ip):
    code, js, _ = http("POST", PUB + "/v1/auth/login", {"email": email, "password": password},
                       {"X-Real-IP": ip, "User-Agent": UA_BROWSER})
    if code != 200 or not js or not js.get("access_token"):
        raise Fatal(f"用户 {email} 从 {ip} 登录失败（HTTP {code}）")
    return js["access_token"]

def register(email, ip):
    hdr = {"X-Real-IP": ip, "User-Agent": UA_BROWSER}
    code, js, text = http("POST", PUB + "/v1/auth/register/start", {"email": email}, hdr)
    if code != 200 or not js or not js.get("registration_token"):
        raise Fatal(f"注册开始失败（HTTP {code}）", text[:200])
    vcode = js.get("dev_code", "") if js.get("verification_required") else ""
    password = "Risk-" + secrets.token_urlsafe(12) + "-2026"
    code, js, text = http("POST", PUB + "/v1/auth/register/complete",
                          {"registration_token": js["registration_token"], "code": vcode,
                           "password": password}, hdr)
    if code != 201 or not js:
        raise Fatal(f"注册完成失败（HTTP {code}）", text[:200])
    return need_uuid(js.get("user_id"), "注册返回的 user_id"), password

def fetch(url, ip, ua):
    path = urllib.parse.urlsplit(url).path
    code, _, _ = http("GET", PUB + path, None, {"X-Real-IP": ip, "User-Agent": UA[ua]})
    return code

def backdate(sub_id, days):
    rows = sql(f"""SET session_replication_role = replica;
        WITH u AS (UPDATE subscription_fetch_log
                      SET fetched_at = fetched_at - make_interval(days => {int(days)})
                    WHERE subscription_id = '{sub_id}'
                      AND fetched_at > now() - interval '15 minutes' RETURNING 1)
        SELECT count(*) FROM u""")
    return int(rows[-1])

def device_key_ip_hash(ip):
    """脚本侧造数：sha256(设备键)；产品存 HMAC(设备键)，后台只按 DISTINCT ip_hash 计数。"""
    return hashlib.sha256(source_key(ip).encode()).digest()

def ip_hash_sql(ip):
    return "decode('" + device_key_ip_hash(ip).hex() + "', 'hex')"

def insert_alive_ips(node_id, sub_id, ips):
    for ip in ips:
        sql(f"""INSERT INTO node_alive_ips (node_id, subscription_id, ip_hash, last_seen_at)
            VALUES ('{node_id}', '{sub_id}', {ip_hash_sql(ip)}, now())
            ON CONFLICT (node_id, subscription_id, ip_hash)
            DO UPDATE SET last_seen_at = now()""")

def roster_users_for_node(status_path, node_id):
    with open(status_path, encoding="utf-8") as f:
        snap = json.load(f)
    for srv in (snap.get("servers") or {}).values():
        node = (srv.get("nodes") or {}).get(str(node_id)) or (srv.get("nodes") or {}).get(node_id)
        if node is not None:
            return set(node.get("users") or [])
    return set()

def wait_roster_proxy(status_path, node_id, proxy_uuid, present, timeout=180, explain=""):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if SIM_PROC is not None and SIM_PROC.poll() is not None:
            raise Fatal("nodesim 已退出", explain)
        users = roster_users_for_node(status_path, node_id)
        if present and proxy_uuid in users:
            ok(explain or f"名单含 {proxy_uuid}")
            return
        if not present and proxy_uuid not in users:
            ok(explain or f"名单不含 {proxy_uuid}")
            return
        time.sleep(1)
    raise Fatal(f"轮询名单超时（{timeout}s）", explain or proxy_uuid)

def wait_nodesim_server_ready(status_path, name, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if SIM_PROC is not None and SIM_PROC.poll() is not None:
            raise Fatal("nodesim 已退出")
        try:
            with open(status_path, encoding="utf-8") as f:
                d = json.load(f)
        except (OSError, json.JSONDecodeError):
            time.sleep(1)
            continue
        s = (d.get("servers") or {}).get(name) or {}
        if s.get("enrolled") and s.get("connected"):
            ok(f"nodesim {name} enrolled and connected")
            return
        time.sleep(1)
    raise Fatal(f"nodesim {name} 就绪超时（{timeout}s）")

def wait_nodesim_node_config(status_path, name, node_id, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if SIM_PROC is not None and SIM_PROC.poll() is not None:
            raise Fatal("nodesim 已退出")
        try:
            with open(status_path, encoding="utf-8") as f:
                d = json.load(f)
        except (OSError, json.JSONDecodeError):
            time.sleep(1)
            continue
        s = (d.get("servers") or {}).get(name) or {}
        node = (s.get("nodes") or {}).get(str(node_id)) or (s.get("nodes") or {}).get(node_id)
        if node and (node.get("config_sha256") or ""):
            ok(f"nodesim {name} 节点 config_sha256 非空")
            return
        time.sleep(1)
    raise Fatal(f"nodesim 节点配置下发超时（{timeout}s）")

def stop_nodesim():
    global SIM_PROC
    if SIM_PROC is None:
        return
    try:
        SIM_PROC.terminate()
        SIM_PROC.wait(timeout=30)
    except Exception:
        try:
            SIM_PROC.kill()
        except Exception:
            pass
    SIM_PROC = None

# ============================================================================
#  一轮：造账号与订阅 → 按用例造行为 → 读系统输出 → 机制断言 → 处置
# ============================================================================
def run_round(rnd, ctx):
    tag = "AB"[rnd]
    sig = {}
    sec(f"第 {rnd + 1} 轮（{tag}）· 预制账号与赠送订阅")
    owners = [c for c in FETCH_CASES + DEVICE_CASES]
    logins = [(c, i) for c in CLUSTER_CASES for i, (how, _) in enumerate(c["members"]) if how == "login"]
    n_gen = len(owners) + len(logins)
    prefix = f"rk{rnd}-{STAMP}"
    gen = generate_users(n_gen, prefix, f"risk-gen-{STAMP}-{rnd}")
    if not check(len(gen) == n_gen, f"后台批量生成 {n_gen} 个账号"):
        raise Fatal("批量生成的账号数不对")
    ids = dict(r.split("|") for r in sql(
        f"SELECT email::text, id::text FROM users WHERE email LIKE '{prefix}-%'"))
    pool = [(u["email"], u["password"], need_uuid(ids.get(u["email"]), "预制账号 id")) for u in gen]
    acct = {}
    for c in owners:
        acct[c["id"]] = pool.pop(0)
    for c, i in logins:
        acct[(c["id"], i)] = pool.pop(0)

    for c in owners:
        ADMIN.call("POST", "/v1/orders/manual",
                   {"user_id": acct[c["id"]][2], "plan_id": ctx["plan_id"], "price_id": ctx["price_id"],
                    "reason": "风控评估：赠送一次性订阅", "settlement": "grant"},
                   idem=f"risk-order-{STAMP}-{rnd}-{c['id']}", expect=(201,))
    subs = {}
    for r in sql(f"""SELECT s.user_id::text, s.id::text, s.node_uid::text, s.proxy_uuid::text
                      FROM subscriptions s
                      JOIN users u ON u.id = s.user_id
                     WHERE u.email LIKE '{prefix}-%'"""):
        uid, sid, nuid, puuid = r.split("|")
        subs.setdefault(uid, []).append((sid, int(nuid), puuid))
    good = all(len(subs.get(acct[c["id"]][2], [])) == 1 for c in owners)
    if not check(good, f"{len(owners)} 张赠送单各开出一条订阅"):
        raise Fatal("赠送单没有开出订阅")
    for c in owners:
        c.setdefault("sub", {})[rnd] = subs[acct[c["id"]][2]][0]
        if c["limit"] != 3:
            ADMIN.call("POST", f"/v1/subscriptions/{c['sub'][rnd][0]}/device-limit", {"limit": c["limit"]})
    ok("设备上限不是 3 的用例已在订阅上覆盖")

    # ------------------------------------------------------------------ 拉取
    sec(f"第 {rnd + 1} 轮（{tag}）· 订阅拉取（门户 24 小时 / 后台 7 天来源）")
    for c in FETCH_CASES:
        email, password, uid = acct[c["id"]]
        sid = c["sub"][rnd][0]
        ips = [(d, ip_of(k, rnd, c["idx"]), ua) for d, k, ua in c["fetch"]]
        home = ips[0][1] if ips[0][0] == 0 else [x for x in ips if x[0] == 0][0][1]
        tok = user_login(email, password, home)
        code, js, _ = http("GET", PUB + "/v1/me/subscription-links", None,
                           {"X-Real-IP": home, "User-Agent": UA_BROWSER}, tok)
        links = (js or {}).get("links") or []
        if code != 200 or len(links) != 1:
            raise Fatal(f"{c['id']} 门户订阅链接不是一条（HTTP {code}）")
        url = links[0]["url"]
        failed = []
        for day in sorted({d for d, _, _ in ips}, reverse=True):
            todays = [(ip, ua) for d, ip, ua in ips if d == day]
            for ip, ua in todays:
                code = fetch(url, ip, ua)
                if code != 200:
                    failed.append(f"{ip}→{code}")
            if day > 0:
                moved = backdate(sid, day)
                if moved != len(todays):
                    failed.append(f"第 {day} 天挪了 {moved} 行，应为 {len(todays)}")
        if c.get("probe"):
            # 错误令牌从一个新地址来：回诱饵 404，且不能算进来源数
            path = urllib.parse.urlsplit(url).path
            bad_path = path[:-1] + ("A" if path[-1] != "A" else "B")
            code, _, _ = http("GET", PUB + bad_path, None,
                              {"X-Real-IP": ip_of("v4:200", rnd, c["idx"]), "User-Agent": UA["A"]})
            check(code == 404, f"{c['id']} 错误令牌回诱饵 404")
        check(not failed, f"{c['id']} {len(ips)} 次拉取按造数完成", "; ".join(failed))
        c.setdefault("tok", {})[rnd] = (tok, home, uid)

    for c in FETCH_CASES:
        tok, home, uid = c["tok"][rnd]
        code, js, _ = http("GET", PUB + "/v1/me/subscription-links", None,
                           {"X-Real-IP": home, "User-Agent": UA_BROWSER}, tok)
        d24 = js["links"][0]["distinct_sources_24h"]
        code, js, _ = http("GET", PUB + "/v1/me/subscriptions", None,
                           {"X-Real-IP": home, "User-Agent": UA_BROWSER}, tok)
        mine = (js or {}).get("subscriptions") or [{}]
        limit = mine[0].get("device_limit")
        _, prof = ADMIN.call("GET", f"/v1/users/{uid}/profile")
        s7 = prof["fetch_sources_7d"]
        now = datetime.now(timezone.utc)
        okf = [f for f in prof["fetches"] if f["result"] == "ok"]
        in24 = [f for f in okf if parse_ts(f["at"]) > now - timedelta(hours=24)]
        in7 = [f for f in okf if parse_ts(f["at"]) > now - timedelta(days=7)]
        exp24 = len({ip_of(k, rnd, c["idx"]) for d, k, _ in c["fetch"] if d == 0})
        exp7 = len({ip_of(k, rnd, c["idx"]) for d, k, _ in c["fetch"]})
        check(d24 == exp24, f"{c['id']} 门户 distinct_sources_24h = {exp24}", f"实际 {d24}")
        check(s7 == exp7, f"{c['id']} 后台 fetch_sources_7d = {exp7}", f"实际 {s7}")
        check(limit == c["limit"], f"{c['id']} 门户生效设备上限 = {c['limit']}", f"实际 {limit}")
        check(len({f["ip"] for f in in7}) == s7,
              f"{c['id']} 后台拉取明细解密出的 7 天来源与计数一致")
        sig[c["id"]] = dict(d24=d24, s7=s7, limit=limit,
                            d24n=len({source_key(f["ip"]) for f in in24}),
                            s7n=len({source_key(f["ip"]) for f in in7}))

    # ------------------------------------------------------------------ 同 IP 多账号
    sec(f"第 {rnd + 1} 轮（{tag}）· 同 IP 多账号（注册与登录）")
    member_ids = {}
    for c in CLUSTER_CASES:
        member_ids[c["id"]] = []
        for i, (how, key) in enumerate(c["members"]):
            ip = ip_of(key, rnd, c["idx"])
            if how == "reg":
                # 与预制账号的前缀 rk 分开，免得按前缀反查预制账号时混进来
                email = f"rr{rnd}-{STAMP}-{c['id'].lower()}-{i}@example.test"
                uid, password = register(email, ip)
            else:
                email, password, uid = acct[(c["id"], i)]
                user_login(email, password, ip)
            member_ids[c["id"]].append((uid, email, ip, password))
        ok(f"{c['id']} {len(c['members'])} 个账号按造数注册 / 登录")

    _, js = ADMIN.call("GET", "/v1/ip-clusters")
    listed = {cl["ip"]: cl for cl in js["clusters"]}
    for c in CLUSTER_CASES:
        by_ip = {}
        for uid, email, ip, _ in member_ids[c["id"]]:
            by_ip.setdefault(ip, []).append((uid, email))
        per_ip = {}
        for ip, ms in by_ip.items():
            cl = listed.get(ip)
            if len(ms) >= 2:
                good = (cl is not None and cl["accounts"] == len(ms)
                        and sorted(cl["emails"]) == sorted(e for _, e in ms))
                check(good, f"{c['id']} 聚类列出 {ip} 下的 {len(ms)} 个账号",
                      f"实际 {None if cl is None else (cl['accounts'], cl['risk'])}")
            else:
                check(cl is None, f"{c['id']} 单账号地址 {ip} 不成聚类")
            reg_here = 0
            for uid, _ in ms:
                _, prof = ADMIN.call("GET", f"/v1/users/{uid}/profile")
                if prof.get("registered_ip") == ip:
                    reg_here += 1
            if len(ms) >= 2:
                mine = [x for x in prof["ips"] if x["ip"] == ip]
                check(bool(mine) and mine[0]["accounts"] == len(ms),
                      f"{c['id']} 单用户画像里 {ip} 的同 IP 账号数 = {len(ms)}")
            per_ip[ip] = dict(accounts=len(ms) if cl else 1, risk=cl["risk"] if cl else None,
                              listed=cl is not None, reg_here=reg_here,
                              key=cl["key"] if cl else None)
        top = max(per_ip.values(), key=lambda v: (v["accounts"], v["reg_here"]))
        sig[c["id"]] = dict(accounts=top["accounts"], risk=top["risk"], listed=top["listed"],
                            reg_here=top["reg_here"], listed_ips=sum(v["listed"] for v in per_ip.values()))
        c.setdefault("key", {})[rnd] = top["key"]
        c.setdefault("members_r", {})[rnd] = member_ids[c["id"]]

    # ------------------------------------------------------------------ 在线设备
    sec(f"第 {rnd + 1} 轮（{tag}）· 在线设备与设备上限")
    sql(f"DELETE FROM node_alive_ips WHERE node_id = '{ctx['node_id']}'")
    expect_keys = 0
    for c in DEVICE_CASES:
        ips = sorted({ip_of(k, rnd, c["idx"]) for k in c["ips"]})
        insert_alive_ips(ctx["node_id"], c["sub"][rnd][0], ips)
        expect_keys += len({source_key(ip) for ip in ips})
    n_rows = int(sql(
        f"SELECT count(DISTINCT ip_hash) FROM node_alive_ips WHERE node_id = '{ctx['node_id']}'")[0])
    check(n_rows == expect_keys,
          f"写入 node_alive_ips 的去重设备键行数 = {expect_keys}", f"实际 {n_rows}")
    _, js = ADMIN.call("GET", "/v1/devices")
    grace = js["grace"]
    ctx["grace"], ctx["window"] = grace, js["window_minutes"]
    devs = {d["subscription_id"]: d for d in js["devices"]}
    for c in DEVICE_CASES:
        d = devs.get(c["sub"][rnd][0])
        n = len({source_key(ip_of(k, rnd, c["idx"])) for k in c["ips"]})
        if not check(d is not None, f"{c['id']} 出现在后台在线设备概览"):
            sig[c["id"]] = dict(online=None, limit=None, exceeded=None, grace=grace)
            continue
        check(d["online"] == n and d["limit"] == c["limit"],
              f"{c['id']} 在线 {n} 台、上限 {c['limit']}", f"实际在线 {d['online']}、上限 {d['limit']}")
        check(d["exceeded"] == (d["online"] > d["limit"] + grace),
              f"{c['id']} 超限标志与「在线 > 上限 + grace({grace})」一致")
        sig[c["id"]] = dict(online=d["online"], limit=d["limit"], exceeded=d["exceeded"], grace=grace)

    # ------------------------------------------------------------------ 处置
    sec(f"第 {rnd + 1} 轮（{tag}）· 聚类处置：标记正常与批量停用")
    c03 = next(c for c in CLUSTER_CASES if c["id"] == "C03")
    key = c03["key"][rnd]
    if key:
        ADMIN.call("POST", f"/v1/ip-clusters/{key}/review", {"note": "公司出口，已核实"})
        _, d1 = ADMIN.call("GET", "/v1/ip-clusters")
        _, d2 = ADMIN.call("GET", "/v1/ip-clusters?include_reviewed=1")
        check(all(cl["key"] != key for cl in d1["clusters"]), "C03 标记正常后不再出现在默认列表")
        rv = [cl for cl in d2["clusters"] if cl["key"] == key]
        check(bool(rv) and (rv[0].get("review") or {}).get("decision") == "normal",
              "C03 在 include_reviewed=1 里带着「正常」结论")
    else:
        bad("C03 没有聚类 key，跳过标记正常")
    r01 = next(c for c in CLUSTER_CASES if c["id"] == "R01")
    key = r01["key"][rnd]
    if key:
        uids = [m[0] for m in r01["members_r"][rnd]]
        _, js = ADMIN.call("POST", f"/v1/ip-clusters/{key}/disable-accounts",
                           {"user_ids": uids, "reason": "同一出口几分钟内批量注册"},
                           idem=f"risk-disable-{STAMP}-{rnd}")
        check(js.get("disabled") == len(uids) and not js.get("skipped"),
              f"R01 批量停用 {len(uids)} 个账号、无跳过", json.dumps(js, ensure_ascii=False)[:200])
        n = int(sql(f"""SELECT count(*) FROM users WHERE status = 'suspended'
                        AND id IN ({",".join("'" + u + "'" for u in uids)})""")[0])
        check(n == len(uids), "R01 账号在库里都是 suspended")
        _, email, ip, password = r01["members_r"][rnd][0]
        code, _, _ = http("POST", PUB + "/v1/auth/login", {"email": email, "password": password},
                          {"X-Real-IP": ip, "User-Agent": UA_BROWSER})
        check(code != 200, f"R01 停用后的账号登录被拒（HTTP {code}）")
        _, d2 = ADMIN.call("GET", "/v1/ip-clusters?include_reviewed=1")
        rv = [cl for cl in d2["clusters"] if cl["key"] == key]
        check(bool(rv) and (rv[0].get("review") or {}).get("decision") == "disabled",
              "R01 聚类照常列出并带着「已停用」结论")
    else:
        bad("R01 没有聚类 key，跳过批量停用")
    return sig

# ============================================================================
#  判定：现行阈值（与前端、后端代码一一对应）与阈值族
# ============================================================================
def d1_now(s):  # labels.ts fetchStats：distinct_sources_24h > device_limit
    return s["limit"] is not None and s["d24"] > s["limit"]

def d2_level(s):  # RiskTab.tsx sharingHint：≥6 疑似分享，≥3 留意
    return "疑似分享" if s["s7"] >= 6 else ("留意" if s["s7"] >= 3 else "—")

def d3_level(s):  # api/admin risk.go clusterRisk：≥5 或机房 high，≥3 mid，其余 low
    return s["risk"] or "未入列"

DETECTORS = {
    "D1": dict(name="门户分享提示（24h 来源 > 设备上限）", cases=FETCH_CASES,
               fire=d1_now, show=lambda s: "提示泄露" if d1_now(s) else "—",
               signal=lambda s: f"24h={s['d24']} 上限={s['limit']}"),
    "D2": dict(name="后台画像（7 天来源 ≥ 6 疑似分享）", cases=FETCH_CASES,
               fire=lambda s: s["s7"] >= 6, show=d2_level,
               signal=lambda s: f"7d={s['s7']} 上限={s['limit']}"),
    "D1|D2": dict(name="分享检测合并（门户或后台任一报）", cases=FETCH_CASES,
                  fire=lambda s: d1_now(s) or s["s7"] >= 6,
                  show=lambda s: "报" if (d1_now(s) or s["s7"] >= 6) else "—",
                  signal=lambda s: f"24h={s['d24']} 7d={s['s7']} 上限={s['limit']}"),
    "D3": dict(name="共享 IP 聚类（风险 high）", cases=CLUSTER_CASES,
               fire=lambda s: s["risk"] == "high", show=d3_level,
               signal=lambda s: f"账号={s['accounts']} 此处注册={s['reg_here']}"),
    "D4": dict(name="设备超限（在线 > 上限 + grace）", cases=DEVICE_CASES,
               fire=lambda s: bool(s["exceeded"]),
               show=lambda s: "超限" if s["exceeded"] else "—",
               signal=lambda s: f"在线={s['online']} 上限={s['limit']} grace={s['grace']}"),
}

def lim(s):
    return s["limit"] if s["limit"] is not None else 10 ** 9

FAMILIES = {
    "D1": [("24h 来源 > 上限 + m", lambda s, m: s["d24"] > lim(s) + m, range(0, 5), 0),
           ("24h 来源（IPv6 按 /64 合并）> 上限 + m", lambda s, m: s["d24n"] > lim(s) + m, range(0, 5), None)],
    "D2": [("7 天来源 ≥ T", lambda s, t: s["s7"] >= t, range(2, 13), 6),
           ("7 天来源 > 上限 + m", lambda s, m: s["s7"] > lim(s) + m, range(0, 6), None),
           ("7 天来源（IPv6 按 /64 合并）> 上限 + m", lambda s, m: s["s7n"] > lim(s) + m, range(0, 6), None)],
    "D3": [("同 IP 账号数 ≥ T", lambda s, t: s["accounts"] >= t, range(2, 11), 5),
           ("在该 IP 注册的账号数 ≥ T", lambda s, t: s["reg_here"] >= t, range(2, 11), None)],
    "D4": [("在线 > 上限 + g", lambda s, g: s["online"] is not None and s["online"] > s["limit"] + g,
            range(0, 5), "grace")],
}

def confusion(cases, sig, fire):
    tp = fn = fp = tn = 0
    for c in cases:
        f = bool(fire(sig[c["id"]]))
        if c["label"]:
            tp, fn = tp + f, fn + (not f)
        else:
            fp, tn = fp + f, tn + (not f)
    return dict(tp=tp, fn=fn, fp=fp, tn=tn,
                recall=None if tp + fn == 0 else round(tp / (tp + fn), 3),
                fpr=None if fp + tn == 0 else round(fp / (fp + tn), 3))

def fmt_cm(m):
    r = "—" if m["recall"] is None else f"{m['recall']:.0%}"
    f = "—" if m["fpr"] is None else f"{m['fpr']:.0%}"
    return f"召回 {r}（{m['tp']}/{m['tp'] + m['fn']}）· 误报率 {f}（{m['fp']}/{m['fp'] + m['tn']}）"

def evaluate(sig, grace):
    out = {}
    for did, d in DETECTORS.items():
        sec(f"评估 · {did} {d['name']}")
        rows = []
        for c in d["cases"]:
            s = sig[c["id"]]
            f = bool(d["fire"](s))
            right = f == bool(c["label"])
            rows.append(dict(id=c["id"], grp=c["grp"], label=c["label"], title=c["title"],
                             signal=d["signal"](s), output=d["show"](s), fired=f, correct=right))
            ev(f"| {c['id']} | {c['grp']} | {'应报' if c['label'] else '不应报'} | {c['title']} | "
               f"{d['signal'](s)} | {d['show'](s)} | {'对' if right else '错'} |")
        res = {g: confusion([c for c in d["cases"] if g == "全部" or c["grp"] == g], sig, d["fire"])
               for g in ("T", "H", "全部")}
        for g in ("T", "H", "全部"):
            ev(f"{did} {g}：{fmt_cm(res[g])}")
        out[did] = dict(rows=rows, metrics=res)

    sec("阈值扫描 · 只看调参组选参数，留出组验证")
    sweep = {}
    for did, fams in FAMILIES.items():
        cases = DETECTORS[did]["cases"]
        tune = [c for c in cases if c["grp"] == "T"]
        hold = [c for c in cases if c["grp"] == "H"]
        for name, rule, params, current in fams:
            cur = grace if current == "grace" else current
            scored = []
            for p in params:
                m = confusion(tune, sig, lambda s, p=p: rule(s, p))
                youden = (m["recall"] or 0) - (m["fpr"] or 0)
                scored.append(((-round(youden, 6), abs(p - cur) if cur is not None else p), p, m))
            scored.sort(key=lambda x: x[0])
            _, best, mt = scored[0]
            mh = confusion(hold, sig, lambda s, p=best: rule(s, p))
            row = dict(detector=did, family=name, best=best, current=cur, tune=mt, hold=mh)
            if cur is not None:
                row["current_tune"] = confusion(tune, sig, lambda s: rule(s, cur))
                row["current_hold"] = confusion(hold, sig, lambda s: rule(s, cur))
            sweep[f"{did}:{name}"] = row
            ev(f"{did}「{name}」：调参组选 {best}（现行 {cur if cur is not None else '无'}）")
            ev(f"    调参组 {fmt_cm(mt)}；留出组 {fmt_cm(mh)}")
            if cur is not None:
                ev(f"    现行值 调参组 {fmt_cm(row['current_tune'])}；留出组 {fmt_cm(row['current_hold'])}")
    return out, sweep

# ============================================================================
#  主流程
# ============================================================================
def main():
    ctx = {}
    restore = []
    try:
        sec("0. 一次性库守卫与可达性")
        if os.environ.get("RISK_E2E_DISPOSABLE") != "YES_DELETE_FIXTURES":
            raise Fatal("缺少一次性库确认：RISK_E2E_DISPOSABLE=YES_DELETE_FIXTURES")
        if not NODESIM or not os.path.isfile(NODESIM) or not os.access(NODESIM, os.X_OK):
            raise Fatal("NODESIM 必须指向可执行的 nodesim 二进制")
        for name, url in (("ADM", ADM), ("PUB", PUB)):
            if not is_loopback_origin(url):
                raise Fatal(f"{name} 必须是带端口的数字回环地址")
        if not ADMIN_EMAIL or not ADMIN_PASS:
            raise Fatal("ADMIN_EMAIL / ADMIN_PASS 必须显式给出")
        dbname = sql("SELECT current_database()")[0]
        if not re.search(r"(^|[-_])(test|e2e)([-_]|$)", dbname, re.I):
            raise Fatal("库名必须含独立的 test/e2e 段", dbname)
        ok(f"一次性库守卫通过（{dbname}）")
        code, _, _ = http("GET", PUB + "/readyz")
        check(code == 200, "public 网关 readyz 200")
        ADMIN.login()
        ok("后台管理员已登录")
        _, js = ADMIN.call("GET", "/v1/devices")
        ctx["orig_policy"] = dict(mode=js["mode"], grace=js["grace"])
        ok(f"设备判定原值：{js['mode']}，grace {js['grace']}，窗口 {js['window_minutes']} 分钟")

        sec("1. 夹具：节点与套餐（后台 API + nodesim）")
        global SIM_PROC, SIM_TMP
        SIM_TMP = subprocess.check_output(["mktemp", "-d"], text=True).strip()
        status_path = os.path.join(SIM_TMP, "status.json")
        tokens_path = os.path.join(SIM_TMP, "tokens")
        log_path = os.path.join(SIM_TMP, "nodesim.log")
        ctx["status_path"] = status_path

        ADMIN.login()
        _, schemas = ADMIN.call("GET", "/v1/node-protocol-schemas")
        cipher = "aes-128-gcm"
        for s in (schemas or {}).get("schemas") or []:
            if s.get("node_type") == "shadowsocks":
                methods = s.get("methods") or []
                cipher = methods[0] if methods else cipher
                break
        ok(f"shadowsocks cipher from schema: {cipher}")

        pool_code = f"risk-{STAMP}"
        _, pool_js = ADMIN.call("POST", "/v1/node-pools",
                                {"code": pool_code, "name": f"Risk pool {STAMP}"}, expect=(200,))
        pool_id = need_uuid((pool_js or {}).get("id"), "节点池 id")

        _, srv_js = ADMIN.call("POST", "/v1/servers",
                               {"name": f"risk-{STAMP}-s1", "hostname": f"risk-{STAMP}.example.test",
                                "public_ipv4": "203.0.113.34"}, expect=(201,))
        server_id = need_uuid((srv_js or {}).get("id"), "服务器 id")

        _, node_js = ADMIN.call("POST", "/v1/nodes",
                                {"name": f"risk-{STAMP}-n1", "server_id": server_id, "pool_id": pool_id,
                                 "node_type": "shadowsocks", "server_host": f"risk-{STAMP}.example.test",
                                 "server_port": 22104, "traffic_rate": 1.0, "display_name": f"Risk {STAMP}",
                                 "protocol_config": {"cipher": cipher}},
                                idem=f"risk-node-{STAMP}", expect=(201,))
        node_id = need_uuid((node_js or {}).get("id"), "节点 id")
        node_rv = int((node_js or {}).get("row_version") or 0)
        ctx["node_id"], ctx["node_rv"] = node_id, node_rv
        restore.append("node")

        ADMIN.login()
        _, boot = ADMIN.call("POST", f"/v1/servers/{server_id}/bootstrap-token", {},
                             idem=f"risk-bootstrap-{STAMP}", expect=(201,))
        token = (boot or {}).get("token")
        cmd = (boot or {}).get("install_command") or ""
        m_panel = re.search(r"--panel\s+'([^']+)'", cmd)
        m_ca = re.search(r"--ca-sha256\s+'([^']+)'", cmd)
        if not token or not m_panel or not m_ca:
            raise Fatal("bootstrap-token 响应无法解析")
        # 一开始就按 0600 建文件，令牌不落在宽权限文件里
        with os.fdopen(os.open(tokens_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8") as f:
            f.write(f"s1 {token}\n")
        panel_addr, ca_sha = m_panel.group(1), m_ca.group(1)
        ok("bootstrap token 写入临时文件（不打印）")

        # report-interval 拉长：避免模拟器在脚本期间上报合成在线 IP，污染 node_alive_ips
        with open(log_path, "ab") as logf:
            SIM_PROC = subprocess.Popen(
                [NODESIM, "--panel", panel_addr, "--ca-sha256", ca_sha,
                 "--tokens-file", tokens_path, "--state-dir", os.path.join(SIM_TMP, "state"),
                 "--report-interval", "1h", "--status-file", status_path],
                stdout=logf, stderr=subprocess.STDOUT)
        wait_nodesim_server_ready(status_path, "s1")

        ADMIN.login()
        _, act = ADMIN.call("POST", f"/v1/nodes/{node_id}/activate", {"row_version": node_rv},
                            idem=f"risk-activate-{STAMP}", expect=(200,))
        ctx["node_rv"] = int((act or {}).get("row_version") or node_rv)
        wait_nodesim_node_config(status_path, "s1", node_id)

        _, js = ADMIN.call("POST", "/v1/plans/complete",
                           {"code": f"risk-{STAMP}", "name": f"风控评估 {STAMP}", "visibility": "public",
                            "traffic_gb": 100, "max_devices": 3, "pool_ids": [pool_id],
                            "prices": [{"billing_interval": "month", "interval_count": 1,
                                        "unit_amount": 990, "currency": "CNY", "trial_days": 0}],
                            "publish": True},
                           idem=f"risk-plan-{STAMP}", expect=(201,))
        ctx["plan_id"] = need_uuid(js["plan"]["id"], "套餐 id")
        ctx["price_id"] = need_uuid(js["price_ids"][0], "价格 id")
        restore.append("plan")
        check(js.get("published") is True, "套餐（设备上限 3）已发布，绑定夹具节点池")

        sigs = [run_round(0, ctx), run_round(1, ctx)]

        sec("strict 模式：超限订阅不再下发到节点")
        v = {c["id"]: c["sub"][1][2] for c in DEVICE_CASES}
        node_id = ctx["node_id"]
        status_path = ctx["status_path"]

        def roster():
            return roster_users_for_node(status_path, node_id)

        # 第 2 轮的订阅刚建不久，名单推送是异步的：先等它们出现在节点名单里，再判断宽松模式不摘人
        mode0 = ctx['orig_policy']['mode']
        wait_roster_proxy(status_path, node_id, v["V05"], True, timeout=60,
                          explain=f"{mode0} 模式下超限的 V05 在下发名单里")
        wait_roster_proxy(status_path, node_id, v["V01"], True, timeout=60,
                          explain=f"{mode0} 模式下未超限的 V01 在下发名单里")

        sub_ids_r1 = [c["sub"][1][0] for c in DEVICE_CASES]
        in_list = ",".join("'" + s + "'" for s in sub_ids_r1)
        sql(f"UPDATE node_alive_ips SET last_seen_at = now() WHERE subscription_id IN ({in_list})")
        ok("第 2 轮 DEVICE 用例的在线记录已刷新 last_seen_at")

        ADMIN.call("POST", "/v1/settings/device-limit",
                   {"mode": "strict", "grace": ctx["orig_policy"]["grace"]})
        restore.append("policy")
        wait_roster_proxy(status_path, node_id, v["V05"], False, explain="strict 后 V05 不在名单")
        wait_roster_proxy(status_path, node_id, v["V04"], False, explain="strict 后 V04 不在名单")
        after = roster()
        check(v["V01"] in after and v["V02"] in after and v["V07"] in after,
              "strict 模式保留未超限的 V01、V02（在线 = 上限 + grace）、V07")
        check(v["V08"] in after, "V08（同一 NAT 后多人共用）在 strict 下仍被下发——单地址看不出共享")

        ADMIN.call("POST", "/v1/settings/device-limit", ctx["orig_policy"])
        restore.remove("policy")
        ok("设备判定模式已恢复原值")
        wait_roster_proxy(status_path, node_id, v["V05"], True, explain="恢复宽松后 V05 回到名单")

        sec("两轮一致性")
        diffs = []
        for c in ALL_CASES:
            a, b = sigs[0][c["id"]], sigs[1][c["id"]]
            if a != b:
                diffs.append(f"{c['id']}: {a} ≠ {b}")
        check(not diffs, f"{len(ALL_CASES)} 个用例两轮的系统输出完全一致", "；".join(diffs)[:600])

        result, sweep = evaluate(sigs[0], ctx["grace"])
        if diffs:
            ev("两轮不一致：以下评估只用第 1 轮，差异见上")
        print("RISK_E2E_JSON " + json.dumps(dict(
            stamp=STAMP, grace=ctx["grace"], window=ctx["window"],
            cases={c["id"]: dict(grp=c["grp"], label=c["label"], title=c["title"], why=c["why"])
                   for c in ALL_CASES},
            signals=sigs, consistent=not diffs, detectors=result, sweep=sweep),
            ensure_ascii=False, default=str), flush=True)
    except Fatal as e:
        print(f"  [FATAL] {e.msg}", flush=True)
        if e.detail:
            print(f"          {e.detail}", flush=True)
        bad("脚本因致命错误中止")
    finally:
        sec("收尾：恢复设置、归档套餐、停 nodesim、节点退役")
        try:
            if "policy" in restore:
                ADMIN.call("POST", "/v1/settings/device-limit", ctx["orig_policy"])
                ok("设备判定模式已恢复原值")
            if "plan" in restore:
                _, js = ADMIN.call("GET", f"/v1/plans/{ctx['plan_id']}")
                ADMIN.call("POST", f"/v1/plans/{ctx['plan_id']}/archive",
                           {"expected_row_version": js["plan"]["row_version"]}, idem=f"risk-archive-{STAMP}")
                ok("自建套餐已归档")
            stop_nodesim()
            if SIM_TMP and os.path.isdir(SIM_TMP):
                subprocess.run(["rm", "-rf", SIM_TMP], check=False)
            if "node" in restore:
                try:
                    # 行版本直接从库里取：节点列表接口没有按 id 过滤，取错行会 409
                    rv = int(sql(f"SELECT row_version FROM nodes WHERE id = '{ctx['node_id']}'")[0])
                    ADMIN.call("POST", f"/v1/nodes/{ctx['node_id']}/retire",
                               {"row_version": rv, "reason": "risk e2e cleanup"},
                               idem=f"risk-retire-{STAMP}")
                    ok("夹具节点已 retire")
                except Fatal as e:
                    bad(f"节点 retire 失败：{e.msg}", e.detail)
        except Fatal as e:
            bad(f"收尾失败：{e.msg}", e.detail)
    print(f"\n风控评估：OK {PASS}，FAIL {FAIL}", flush=True)
    sys.exit(1 if FAIL else 0)

main()
PY
