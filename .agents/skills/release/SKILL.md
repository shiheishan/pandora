---
name: release
description: pandora 发版：把主线合进 main、在 main 上打 v* 标签触发 release.yml、看 workflow 出包与来源证明、核对 Release 附件、在测试机上用 get.sh 实装验收，以及发错了怎么办。用户说「发版」「发个 Release」「打 tag」「出一个版本」「v0.x」，或要演练发布流程、检查发布包、验证 get.sh 一条命令安装时使用。推 main、打标签、发 Release 每一步都要先问用户。
---

# 发版

发布链路：`main` 上的提交 → 推 `v*` 标签 → `.github/workflows/release.yml` 构建 amd64 / arm64 两份包、出来源证明、建 Release → 部署者用 Release 里的 `get.sh` 一条命令首装或升级。部署者看的说明在 README「部署」一节，这里是维护者的步骤。

**推 `main`、推标签、建或删 Release 都对外可见，每一步做之前先问用户，拿到明确同意再做。** 同意只管那一步，不延伸到下一步。

## 1. 发版前

- 主线 CI 全绿：最新主线提交跑过 `wait-github.sh` 退出 0（verify skill）。
- 改了热路径的话，性能闸门已过（perf-gate skill）；认证、钱、迁移、节点通道的改动审查过（pre-merge-review skill）。
- 定版本号：`vMAJOR.MINOR.PATCH`，预发布加连字符后缀（如 `v0.1.0-rc.1`，会发成 prerelease）。标签必须匹配 `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`，否则 workflow 拒绝。同一个版本号不重发（见第 5 节）。
- 演练（可选，`release.yml` 已在 `main` 上时才能用）：`gh workflow run release.yml --ref <分支> -f version=v0.0.0-dryrun`，只构建、上传 artifact，不建 Release。改过 `release.yml`、`build-release.sh`、`get.sh` 的话先演练一次。
- 汇总要发的内容：自上个标签以来的提交（`git log --oneline <上个标签>..main`；第一次发版就写主要能力），供 Release 说明与给用户确认用。

## 2. 合进 main（先问用户）

```bash
git checkout main && git merge --ff-only origin/main && git merge --no-ff feat/panel-redesign
git diff --stat feat/panel-redesign main   # 应为空：两边树一致
git push origin main
git checkout feat/panel-redesign
```

推完等 `main` 上的 CI 结论（`wait-github.sh <sha>`）。

## 3. 打标签（先问用户）

```bash
git tag -a v0.1.0 -m "v0.1.0" main
git push origin v0.1.0
```

- 标签打在 `main` 已推送的提交上；不在 `main` 上的提交，workflow 会拒绝发布。
- 用附注标签（`-a`），不要轻量标签。

## 4. 看 workflow 与核对附件

```bash
gh run list --workflow release.yml --limit 3
gh run watch <run-id> --exit-status
gh release view v0.1.0
```

核对：

- 附件齐全：两个架构的 `pandora-panel_<版本>_linux_<arch>.tar.gz`，各自的 `.sha256` 与 `.manifest.sha256`，`SHA256SUMS`，`get.sh`。
- 来源证明能过（下载到临时目录再核）：
  ```bash
  gh attestation verify pandora-panel_v0.1.0_linux_amd64.tar.gz --repo shiheishan/pandora \
    --signer-workflow shiheishan/pandora/.github/workflows/release.yml --source-ref refs/tags/v0.1.0
  ```
- `sha256sum -c SHA256SUMS` 通过。
- 发布说明里的版本号、核对命令正确；预发布标记与版本号一致。

## 5. 实装验收（测试机）

在一台全新测试机上按 README 第 1 节跑一条命令安装，按 test-machine skill 开机、登记、事后回收（删机要用户点名）：

- 首装：`curl -fsSL https://github.com/shiheishan/pandora/releases/download/<版本>/get.sh | sudo bash`（`latest` 只指向非预发布版本），用虚构域名或 IP 证书走完安装器。
- 看 `systemctl status pandora-serve pandora-worker`、`pandora migrate status`、门户与后台能打开、节点一键安装能接入（flow-walk / node-e2e 按需）。
- 有上一个版本时再验升级：先装旧版本，再跑新版本的 `get.sh`，确认迁移跑完、数据还在。

测试机上只用虚构数据；后台前缀、口令留在机器和 `ops-local/`，不写进对话与报告。

## 6. 发错了怎么办

- **workflow 失败、Release 没建出来**：修好后删掉远端标签再打（`git push origin :refs/tags/<版本>`，先问用户），同一个版本号可以重打，因为还没有人拿到过它。
- **Release 已建出来但包有问题**：不覆盖附件、不重用版本号（部署者可能已经装了，`get.sh` 遇到同版本目录内容不同会停下）。发一个新的补丁版本；问题严重时把坏版本改成 prerelease 或删掉（先问用户），并在新版本说明里写明。
- **部署者要回退**：`get.sh` 不能用来回退到旧版本（新迁移已经跑过，旧二进制会跑在新 schema 上）。按 `panel/deploy/RUNBOOK.md` 先 `pandora migrate down` 逐个退迁移，再装旧版本的包。

## 仓库设置（在本仓库以外，交给用户）

审查建议用户在 GitHub 仓库设置里打开：

- **Immutable releases**：发布后附件与标签不能再改，README 里「核过的就是装的那份」才真正成立。
- **Rulesets**：限制谁能创建 `v*` 标签。

这些是仓库设置，不在代码里；没开之前，在给用户的发版说明里提醒一次。
