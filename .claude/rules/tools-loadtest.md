---
paths:
  - "panel/tools/loadtest/**"
---

# 面板压测工具链（loadtest）

工具只负责测得出、测得准，本身不做调优；压测中途发现的问题先记下，不顺手改面板。

## 边界与依赖

- 只经 `go run ./tools/loadtest` 或 `go build` 后拷到压测机使用，不被任何包 import，不进发布包（`deploy/build-release.sh` 只编 `cmd/<名字>`）。
- 四个子命令 seed / nodes / users / burst 之间只经 `ltkit.Manifest` 交换数据；第五个 `quiet-report` 只读 `scripts/quiet-collect.sh` 的采样目录，按 `-tier A|B` 两档静默标准（面板 + 数据库 CPU、nginx、整机内存与窗口内换页）出判定，`-strict` 不达标退出非 0，`-json` 给 perf-gate skill 的 verdict.py 读；口径改动要和 prod-retest skill 的静默一节、perf-gate 一起改。计量一律走 `ltkit.Recorder`，三类场景的 QPS、分位数、错误码才是同一口径。不要在子命令里另写计量。
- 签名规范串、配置验签、线格式直接调 `domain/nodefabric` 与 `platform/crypto`（如 `nodefabric.CanonicalPayloadV2`、`VerifyEffectiveReleaseSignature`），不自己抄一份；也不 import pdnd：它是另一个 module，引用要在 panel 的 go.mod 加 replace。

## 与被测对象保持同步（对方改了，这里要跟）

- nodesim 的请求序列、节拍、ETag 与失败处理逐段对齐 `pdnd/node/node.go` 与 `pdnd/panel/*`，只把内核换成虚构负载；pdnd 的节拍、退避、签名流程、用户同步语义变了，同步改 `nodesim/`。`-node-behavior=current`（缺省）跟 pdnd 走；`legacy` 冻结为 2026-10-06 改版前的节拍，只为和 5k-r1 旧数据对照，pdnd 再改时不跟。
- `seed/users.go` 镜像 `adminops.GenerateUsers` 与 billing 的开通（订阅先建 pending、经状态机触发器转 active、开通事件、配额、哈希凭据）；面板开通流程变了要同步。
- `userload/traffic.go` 的 UA 表逐条对应 `subscription.DetectFormat` 的一个分支（`TestSubscriptionUAsHitEveryFormatBranch`）；门户与后台读接口表取自前端调用处，接口改名要同步。
- `userload/preflight.go` 的 `panelLimits` 镜像面板限流缺省：`AEGIS_RL_*`（platform/config）、订阅凭据 `rate_limit_per_hour` 缺省、后台每 IP 240 次/分（`api/admin/router.go` 写死）；面板改缺省要同步，`seed/options.go` 的后台请求间隔也按 240 次/分定。

## 造数与清理

- 造数标记三件套：用户邮箱只用 `@loadtest.invalid`，节点与目录名以 `loadtest-` 开头，模拟来源 IP 全在 198.18.0.0/15（512 个 /24 轮转）。`seed/retire.go` 只靠这些标记圈定要清理的对象，改命名必须同步（`TestLoadtestMarkersIdentifyEverySeededName`、`TestRetireTouchesOnlyLoadtestMarkers`）。
- 造数经运行角色与租户上下文（`db.Scope`）写库，让 RLS 与触发器真起作用；不要为了快改用超级用户或直接插 active。
- 退役不 DELETE：追加写表连着订阅与节点。订阅经状态机转 expired；上一批里仍有有效接入身份的节点先经后台吊销接口吊销身份（后台不让退役仍有身份的节点），再经后台批量接口转 draining；接着按服务器状态机把上一批 `loadtest-` 服务器退役（节点是自己服务器的控制节点，服务器在役时节点退役会 409），最后把节点转 retired。每一步只处理还没处理的，中途失败原样重跑即可。
- 节点与目录全走真实网关，不直写库。

## 网关与限流

- 模拟来源 IP 经 `X-Real-IP` 带给面板（`httpx.ClientIP` 只认它）。`seed -admin-workers N`（N>1）开 N 个后台会话，各占一个 198.51.100.x 虚构来源、各自按每 IP 240 次/分节流；缺省 1 不带来源头，与旧行为一致。每个模拟节点都要带清单里的 `real_ip`（`nodesim/realip.go`）：不带会让全部节点挤在压测机一个地址上被 nginx 每 IP 限流。
- 节点侧请求只对限流重放：应用的 429，或 nginx limit_req 回的 503 `text/html` 页；应用自己的 JSON 错误一律不重放（`TestNodeClientDoesNotRetryApplication503`）。
- 报告里的端点名必须规范化：不出现令牌、邮箱、id、订阅前缀、后台秘密前缀（`userload/client.go`）。

## scripts/（压测期在面板主机上以 root 跑）

- 放在这里而不放 `deploy/`：deploy 是随产品分发的安装链，这些是压测期诊断工具，不进发布包。
- 只认 `deploy/install.sh` 的直装布局（`/opt/pandora`、系统单元 `postgresql@<版本>-main` 与 `valkey-server` / `redis-server`，公共段在 `lt-common.sh`）；没有 Docker 分支，旧轮次的 Docker 布局结果不由仓库代码读。
- 只读解析 `.env`、不 source；口令只经 `PGPASSWORD` / `REDISCLI_AUTH` 环境变量传给子进程，不出现在命令行参数里。
- `nginx-realip.sh` 只顶替 `deploy/render-nginx.sh` 生成的 cloudflare-realip 信任表并能还原，随包的 nginx 站点配置不动。
