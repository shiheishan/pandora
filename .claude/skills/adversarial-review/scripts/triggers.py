#!/usr/bin/env python3
"""只读：判断一个任务分支的改动是否触发合并前的对抗式审查，并按领域列出命中的文件与新增行。

用法：triggers.py <基点> <头> [-C 仓库目录]
  - 实际比较 merge-base(基点, 头)..头。基点给主线分支名（feat/panel-redesign），不给 brief 里的原基点：
    分支中途合过主线时，原基点会把合进来的主线改动也算进去（w7pdnd 从 43 个文件变成 161 个）。
  - 已合入的分支：基点给合并提交的 ^1，头给 ^2（例：triggers.py M^1 M^2）。
退出码：0 有触发，1 无触发，2 参数或 git 出错。
"""
import re
import subprocess
import sys

# 领域 → (路径正则, 新增行正则 或 None)。路径只看非测试文件；新增行只看非测试 .go / .sql / .sh。
AREAS = [
    ("钱与计费", r"^panel/internal/domain/(billing|payment|purchase|giftcard)/"
                 r"|^panel/cmd/aegis-payctl/"
                 r"|^panel/internal/api/(public|admin)/[^/]*(order|checkout|pay|wallet|balance|commission|withdraw|giftcard|redeem|traffic_pack|plan_change)",
     None),
    ("权限与认证", r"^panel/internal/middleware/"
                  r"|^panel/internal/api/admin/router"
                  r"|^panel/internal/domain/identity/"
                  r"|^panel/internal/platform/(sessionauth|iamguard|token|credentialrevocation|idempotencybind)/"
                  r"|^panel/internal/api/node/(router|server_router)\.go$",
     r"RequirePermission|RequireRecentReauth|Idempotency\(|INSERT INTO (app\.)?permissions|role_permissions"),
    ("秘密与证书", r"^panel/internal/domain/certs/|^panel/internal/platform/(crypto|certbundle|bindingcontract)/"
                  r"|^pdnd/(certstore|certbundle|bindingcontract)/"
                  r"|^panel/deploy/(edge-tls|install|install-native|bootstrap|psql|backup-postgres|restore-postgres|migrate-to-new-host)\.sh$",
     r"\.Seal\(|\.Open\(|Envelope|password|private[_ ]?key|privkey|secret|--password-stdin|chmod 0?600|AAD|PGPASSWORD"),
    ("迁移", r"^panel/migrations/.*\.sql$|^panel/deploy/(configure-app-role\.sql|migrate\.sh|check-migrations\.sh)$",
     None),
    # 安装、升级、迁移、备份恢复与节点接入脚本（多以 root 运行）；CI 跑测用的 run-*、test-*、fixtures、文档不算
    ("部署脚本与安装链", r"^panel/deploy/(?!fixtures/|run-|test-)(?!.*\.md$)"
                       r"|^pdnd/release/[^/]*\.(sh|service)$"
                       r"|^panel/internal/api/public/pdnd_install",
     None),
    ("节点内核与协议", r"^pdnd/(kernel|core|internal|outbound|route|node|panel)/|^pdnd/main\.go$"
                     r"|^panel/internal/api/node/"
                     r"|^panel/internal/domain/nodefabric/(protocol_|uniproxy|xboard_|service\.go|heartbeat)"
                     r"|^panel/internal/domain/subscription/render",
     None),
    ("新依赖", r"(^|/)go\.mod$|(^|/)package\.json$", None),
    ("并发与锁", None,
     r"FOR UPDATE|FOR SHARE|SKIP LOCKED|pg_advisory|LOCK TABLE|InTxSerializable|SERIALIZABLE"
     r"|lease_owner|sync\.(Mutex|RWMutex)|atomic\.|go func"),
]
TEST = re.compile(r"(_test\.go|_test\.sh|_test\.ps1|\.test\.tsx?|\.spec\.ts)$|/testdata/|/tests?/")
CODE = re.compile(r"\.(go|sql|sh)$")


def git(repo, *args):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, text=True).stdout


def main():
    args = sys.argv[1:]
    repo = "."
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
    if not hit_any:
        print("\n无触发：不必做对抗式审查（仍按 accept-task 自己读关键 diff）")
        return 1
    print("\n有触发：合并前按 adversarial-review skill 派审（命中只是线索，是否真的动到不变量由你读 diff 判断）")
    return 0


def dep_added(repo, mb, head, f):
    """go.mod 的 require 新增模块、package.json 新增依赖才算，只改 go 指令或版本号不算新依赖。"""
    d = git(repo, "diff", "-U0", f"{mb}..{head}", "--", f)
    plus = {l[1:].split()[0] for l in d.split("\n") if l.startswith("+") and not l.startswith("+++") and l[1:].split()}
    minus = {l[1:].split()[0] for l in d.split("\n") if l.startswith("-") and not l.startswith("---") and l[1:].split()}
    new = {m for m in plus - minus if m not in ("go", "toolchain", "require", "(", ")")}
    return bool(new)


if __name__ == "__main__":
    sys.exit(main())
