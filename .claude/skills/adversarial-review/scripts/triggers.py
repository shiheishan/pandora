#!/usr/bin/env python3
"""只读：判断一个任务分支的改动是否触发合并前的对抗式审查，并按领域列出命中的文件与新增行。

用法：triggers.py <基点> <头> [-C 仓库目录] [--grok]
  - 实际比较 merge-base(基点, 头)..头。基点给该路的上游分支名（一般是主线 feat/panel-redesign；叠在集成分支上的路
    给集成分支，如 feat/panel-redesign-s，见 dispatch-task「叠在集成分支上的路」），不给 brief 里的原基点：
    分支中途合过上游时，原基点会把合进来的上游改动也算进去（w7pdnd 从 43 个文件变成 161 个）。
  - 集成分支并主线前：基点给主线、头给集成分支，看整条集成分支。
  - 已合入的分支：基点给合并提交的 ^1，头给 ^2（例：triggers.py M^1 M^2）。
  - --grok：执行者是 Cursor 的 Grok。除小件外都要审（根 CLAUDE.md「大任务拆子 agent」），不看领域命中；
    小件 = 领域无命中，且非测试文件的增删合计不超过 GROK_SMALL 行。
退出码：0 有触发（或 Grok 的非小件），1 无触发，2 参数或 git 出错。
"""
import re
import subprocess
import sys

# 领域 → (路径正则, 新增行正则 或 None)。路径只看非测试文件；新增行只看非测试 .go / .sql / .sh。
AREAS = [
    ("钱与计费", r"^panel/internal/domain/(billing|payment|purchase|giftcard)/"
                 r"|^panel/cmd/aegis-payctl/"
                 r"|^panel/internal/api/(public|admin)/[^/]*(order|checkout|pay|wallet|balance|commission|withdraw|giftcard|redeem|traffic_pack|plan_change)"
                 # 流量入账：上报、去重编号、日用量、汇总与保留（S3 的 traffic_ingest 也在这里）
                 r"|^panel/internal/domain/nodefabric/(traffic_|usage_daily|uniproxy_traffic)",
     None),
    ("权限与认证", r"^panel/internal/middleware/"
                  r"|^panel/internal/api/admin/router"
                  r"|^panel/internal/domain/identity/"
                  r"|^panel/internal/platform/(sessionauth|iamguard|token|credentialrevocation|idempotencybind|gatewaytls)/"
                  r"|^panel/internal/api/node/(router|server_router)\.go$"
                  # 节点与服务器的身份：会话认证（mTLS + 指纹集）、接入、令牌、nonce，以及吊销经 LISTEN 载荷下发
                  r"|^panel/internal/api/node/session/"
                  r"|^panel/internal/domain/nodefabric/(server_session|server_identity|server_enrollment|enrollment|nonce_|bootstrap|node_identity|epoch_watch)"
                  r"|^panel/internal/platform/cache/watch\.go$",
     r"RequirePermission|RequireRecentReauth|Idempotency\(|INSERT INTO (app\.)?permissions|role_permissions"),
    # gatewaytls 两节都进：它既核客户端证书（认证），又装载网关自己的私钥与证书（秘密）
    # 加密备份（封条、age 密钥、WebDAV 凭据）与管理员口令工具也在这里：改动不一定带 secret 关键词
    ("秘密与证书", r"^panel/internal/domain/certs/|^panel/internal/platform/(crypto|certbundle|bindingcontract|gatewaytls)/"
                  r"|^pdnd/(certstore|certbundle|bindingcontract|binding)/"
                  r"|^panel/internal/domain/dbbackup/|^panel/cmd/(aegis-backup-webdav|aegis-adminctl)/"
                  r"|^panel/deploy/(edge-tls|install|install-native|bootstrap|psql|backup-postgres|restore-postgres|verify-backup|migrate-to-new-host)\.sh$",
     r"\.Seal\(|\.Open\(|Envelope|password|private[_ ]?key|privkey|secret|--password-stdin|chmod 0?600|AAD|PGPASSWORD"),
    ("迁移", r"^panel/migrations/.*\.sql$|^panel/deploy/(configure-app-role\.sql|migrate\.sh|check-migrations\.sh)$",
     None),
    # 安装、升级、迁移、备份恢复与节点接入脚本（多以 root 运行）；CI 跑测用的 run-*、test-*、fixtures、文档不算
    ("部署脚本与安装链", r"^panel/deploy/(?!fixtures/|run-|test-)(?!.*\.md$)"
                       r"|^pdnd/release/[^/]*\.(sh|service)$"
                       r"|^panel/internal/api/public/pdnd_install",
     None),
    ("节点内核与协议", r"^pdnd/(kernel|core|internal|outbound|route|node|panel|session|binding|server|uniproxy|cmd)/|^pdnd/main\.go$"
                     r"|^(panel/internal/platform|pdnd)/bindingcontract/"
                     r"|^panel/cmd/aegis-node/"
                     r"|^panel/internal/api/node/"
                     r"|^panel/internal/platform/cache/watch\.go$"
                     r"|^panel/internal/domain/nodefabric/(protocol_|uniproxy|xboard_|service\.go|heartbeat"
                     r"|session_hub|nodestream|epoch_watch|effective_release|config_delivery)"
                     r"|^panel/internal/domain/subscription/render",
     None),
    ("新依赖", r"(^|/)go\.mod$|(^|/)package\.json$", None),
    ("并发与锁", None,
     r"FOR UPDATE|FOR SHARE|SKIP LOCKED|pg_advisory|LOCK TABLE|InTxSerializable|SERIALIZABLE"
     r"|lease_owner|sync\.(Mutex|RWMutex)|atomic\.|go func"),
]
GROK_SMALL = 30  # Grok 交回可以不审的上限：非测试文件增删合计行数
TEST = re.compile(r"(_test\.go|_test\.sh|_test\.ps1|\.test\.tsx?|\.spec\.ts)$|/testdata/|/tests?/")
CODE = re.compile(r"\.(go|sql|sh)$")


def git(repo, *args):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, text=True).stdout


def main():
    args = sys.argv[1:]
    repo = "."
    grok = "--grok" in args
    if grok:
        args.remove("--grok")
    if "-C" in args:
        i = args.index("-C")
        repo = args[i + 1]
        del args[i:i + 2]
    if len(args) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base, head = args
    try:
        mb = git(repo, "merge-base", base, head).strip()
        files = [f for f in git(repo, "diff", "--name-only", f"{mb}..{head}").split("\n") if f]
        patch = git(repo, "diff", "-U0", f"{mb}..{head}")
        numstat = git(repo, "diff", "--numstat", f"{mb}..{head}")
    except subprocess.CalledProcessError as e:
        print(e.stderr.strip(), file=sys.stderr)
        return 2

    added = []  # (文件, 行)
    cur = None
    for line in patch.split("\n"):
        if line.startswith("+++ "):
            cur = line[6:] if line.startswith("+++ b/") else None
        elif line.startswith("+") and cur and CODE.search(cur) and not TEST.search(cur):
            text = line[1:].strip()
            if not text.startswith(("//", "--", "#")):  # 注释不算
                added.append((cur, text))

    print(f"范围 {mb[:7]}..{head}（{len(files)} 个文件）")
    if not files:
        print("范围是空的：分支多半已合入基点。改用合并提交 M^1 M^2", file=sys.stderr)
        return 2
    hit_any = False
    for name, path_re, line_re in AREAS:
        fs = [f for f in files if path_re and re.search(path_re, f) and not TEST.search(f)]
        if name == "新依赖":
            fs = [f for f in fs if dep_added(repo, mb, head, f)]
        ls = [(f, l) for f, l in added if line_re and re.search(line_re, l)]
        if not fs and not ls:
            continue
        hit_any = True
        print(f"\n## {name}")
        for f in fs[:12]:
            print(f"  文件 {f}")
        if len(fs) > 12:
            print(f"  …另 {len(fs) - 12} 个文件")
        for f, l in ls[:8]:
            print(f"  新增 {f}: {l[:110]}")
        if len(ls) > 8:
            print(f"  …另 {len(ls) - 8} 行")
    if grok:
        n = nontest_lines(numstat)
        print(f"\n执行者 Grok：非测试文件增删 {n} 行（小件上限 {GROK_SMALL}）")
        if hit_any or n > GROK_SMALL:
            print("Grok 交回、不是小件：合并前派 opus 审（豁免见 adversarial-review 第 1 节：只改测试注释文案、纯挪动）")
            return 0
        print("Grok 小件：可不审（仍按 accept-task 自己读全部 diff）")
        return 1
    if not hit_any:
        print("\n无触发：不必做对抗式审查（仍按 accept-task 自己读关键 diff）")
        return 1
    print("\n有触发：合并前按 adversarial-review skill 派审（命中只是线索，是否真的动到不变量由你读 diff 判断）")
    return 0


def nontest_lines(numstat):
    """非测试文件的增删合计；二进制文件（numstat 记 -）按超限算。"""
    total = 0
    for line in numstat.split("\n"):
        parts = line.split("\t")
        if len(parts) != 3 or TEST.search(parts[2]):
            continue
        if parts[0] == "-":
            return GROK_SMALL + 1
        total += int(parts[0]) + int(parts[1])
    return total


def dep_added(repo, mb, head, f):
    """go.mod 的 require 新增模块、package.json 新增依赖才算，只改 go 指令或版本号不算新依赖。"""
    d = git(repo, "diff", "-U0", f"{mb}..{head}", "--", f)
    plus = {l[1:].split()[0] for l in d.split("\n") if l.startswith("+") and not l.startswith("+++") and l[1:].split()}
    minus = {l[1:].split()[0] for l in d.split("\n") if l.startswith("-") and not l.startswith("---") and l[1:].split()}
    new = {m for m in plus - minus if m not in ("go", "toolchain", "require", "(", ")")}
    return bool(new)


if __name__ == "__main__":
    sys.exit(main())
