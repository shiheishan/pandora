# Pandora NativeCore Linux 发布

## Native VLESS UDP

Pandora NativeCore accepts VLESS `command=UDP` on every stream transport that
already carries a VLESS request (TCP, WebSocket, HTTP Upgrade, gRPC, and XHTTP
stream/packet modes). The request header selects the destination; subsequent
datagrams use the native VLESS two-byte length framing and are routed through
the same DataPlane policy and per-user traffic accounting as TCP. No
compatibility kernel is started for this path.

NativeCore also accepts Trojan UDP ASSOCIATE and forwards each address-bearing
datagram using the Trojan length/CRLF framing, with per-destination DataPlane
routing and traffic accounting.

The native gRPC transport accepts standard gzip-compressed request messages
and exposes the decoded tunnel bytes to the protocol adapter; responses remain
valid identity-encoded gRPC frames for broad client compatibility.

## 能力矩阵探针

节点二进制提供不启动数据面的能力探针，供面板编排和发布前检查使用：

```bash
./pandora-native --capabilities
```

发布或升级后可先运行原生适配器自检；它不会打开监听端口，只检查能力矩阵与默认注册表是否一致：

```bash
./pandora-native --self-check
```

必须得到 `"status": "ok"`。若新增协议却没有同步注册 NativeCore 适配器，自检会失败并阻止继续部署。

仓库还提供独立进程探针 `cmd/pandora-h3-probe`，用于 REALITY-over-H3 的原始 XHTTP 回环或带 VLESS 头的验收。它与节点进程分开运行，适合在目标 Linux 机上做端口级检查；没有第三方客户端时，不应把该探针结果描述为第三方互操作。

外部客户端门禁另有一个显式 `interop` 测试：它启动真实 Xray 客户端，验证普通 TLS + XHTTP/HTTP3 + VLESS 的回环、VLESS 响应和流量统计。该测试只用于验收，不进入默认生产依赖，也不代表 REALITY + XHTTP/HTTP3 已被第三方客户端验证：

```bash
go test -mod=readonly -tags interop -run 'TestExternalXray(VLESSXHTTPRealityH2Interop|VLESSXHTTPH3Interop|XHTTPH3Transport)$' -count=1 ./kernel
```

普通 TLS + XHTTP/HTTP3 以及 REALITY + XHTTP/HTTP2 已有真实 Xray 客户端门禁；REALITY + XHTTP/HTTP3 仍是 NativeCore 自有协议路径。在没有独立第三方客户端成功证据前，REALITY-H3 发布门禁必须保持“未验证”，不能把内部探针结果包装成互操作承诺。

另有 `interop_mihomo` 黑盒门禁，要求显式提供外部 Mihomo 可执行文件：

```bash
MIHOMO_BIN=/path/to/mihomo go test -mod=readonly -tags interop_mihomo -run TestExternalMihomoVLESSXHTTPRealityH3Interop -count=1 -v ./kernel
```

在目标 Debian staging 上使用官方 Mihomo v1.19.29（下载包 SHA-256
`60de76a35a6cbf7b4fa4a20f5c257c24345d1d635ab1aa3877022a1997ef413c`）实测，
客户端在发起连接前明确记录 `xhttp HTTP/3 does not support REALITY`；测试因此以
“已识别的客户端能力限制”跳过，而不是将其误报为成功互操作。该边界必须继续显示为未验证，
直到有支持此组合的独立客户端通过完整回环。

发布脚本会同时产出节点二进制和 `pandora-h3-probe-linux-{amd64,arm64}` 两个诊断客户端，并将四个文件的 SHA-256 写入同一份 manifest；`verify.sh` 会逐项校验，避免只验证文件存在。

输出是稳定的 JSON，包含 `kernel`、`native_only` 和各协议的网络、加密、特性与明确边界。面板下发组合前应先读取该矩阵；VLESS `REALITY + xhttp-h3` 走 Pandora NativeCore 的 QUIC-native REALITY 握手，不会静默降级为普通 HTTP/3 TLS。

面板稳定 Schema、NativeCore 能力矩阵和 serving allowlist 由同一静态门禁校验，避免新增协议只更新一层：

```bash
python3 pdnd/release/check_native_panel_parity.py
```

该检查只读取源码，不启动 Go、监听端口或任何节点进程。

`build.sh` 生成不依赖 CGO 的 Linux x64 与 ARM64 产物，并写出包含 `runtime:pandora-native`、`native_only:true`、版本、提交、Go 工具链和 SHA-256 的 `manifest.json`；脚本会拒绝默认构建重新依赖兼容多内核。

```bash
cd pdnd
./release/build.sh ./release/dist v0.1.0
./release/verify.sh ./release/dist v0.1.0
```

可在目标 Linux 机器上先执行隔离验收，不会写入系统路径，也不会安装、启用或启动
systemd 服务。脚本会按当前架构选择 x64/ARM64 二进制，运行 `--self-check`、
`--capabilities`，并用 `systemd-analyze verify` 检查替换为 staging 路径后的单元：

```bash
bash ./release/staging-acceptance.sh ./release/dist /var/tmp/pandora-native-staging v0.1.0
```

只有该隔离验收和回滚窗口确认后，才允许人工执行上面的正式安装命令；此脚本本身不
负责生产切换。

仓库 CI 的 Linux release job 会对同一份四产物先执行 `verify.sh`，再执行该 staging
验收脚本；Linux 上 `verify.sh` 还会用 `file` 校验 node ELF 的 x86-64/aarch64 架构。
因此 CI 的绿色结果只代表隔离验收通过，不代表已经修改生产 systemd。

发布前必须在真实 Linux runner 上补跑：

- `go test -race -count=1 ./...`
- 冷启动、SIGTERM 收尾、端口释放和重复启动
- x64/ARM64 目标机上的协议互操作与回滚

Windows 交叉构建只能证明 `CGO_ENABLED=0` 编译产物可生成，不能替代 Linux race 或生产部署验收。

## systemd 冷启动

Panel releases also generate `deploy/release-artifact.env` from the exact
`SHA256SUMS` entries for both NativeCore architectures. Load that file into
the panel service environment before enabling first-time enrollment; the
control plane rejects production evidence that does not match the selected
release version and architecture digest.

`pandora-native.service` 假定二进制安装到 `/usr/local/bin/pandora-native`，配置文件为
`/etc/pandora-native/config.json`，并以无特权 `pandora` 用户运行。安装后应执行：

正式部署省略 `native_only` 即启用 NativeCore-only，让未验证的协议组合直接报错；只有迁移阶段显式设置
使用 `-tags compat` 构建并设置 `"native_only": false` 才允许
回落到兼容内核；默认发布物不链接兼容多内核。
NativeCore-only 同样拒绝显式的 `kernel: "xray-core"` 与 `kernel: "sing-box"`。

```bash
install -d -o pandora -g pandora -m 0750 /etc/pandora-native /var/lib/pandora-native /var/log/pandora-native
install -o root -g root -m 0755 pandora-native-linux-amd64 /usr/local/bin/pandora-native
install -o root -g root -m 0644 pandora-native.service /etc/systemd/system/pandora-native.service
systemctl daemon-reload
systemctl enable --now pandora-native
systemctl show pandora-native -p ActiveState -p SubState
```

## External XHTTP/REALITY boundary

The NativeCore implementation includes a native REALITY-over-H3 path and its
own loopback probe. That probe is not a third-party interoperability result.
The `interop_external` suite also contains independent Juicity and Naive client
gates. Each gate requires an explicitly pinned external binary and a matching
SHA-256 value; a missing or untrusted binary must remain skipped rather than
being reported as an interoperability pass.
The external interop gate therefore reports TLS + XHTTP/H3 and REALITY +
XHTTP/H2 separately. Until an independent client completes a REALITY +
XHTTP/H3 round trip, the capability matrix must continue to expose
`external-reality-xhttp-h3-unverified`; release tooling must not silently
delegate that combination to Xray or sing-box.

The current pinned Xray dependency (`github.com/xtls/xray-core`)
provides an auditable reason for this boundary: its XHTTP dialer selects
HTTP/2 whenever a REALITY configuration is present, while its HTTP/3 branch
is selected only for ordinary TLS with a single `h3` ALPN. Consequently, the
checked-in Xray gates prove REALITY + XHTTP/H2 and TLS + XHTTP/H3 as separate
combinations; they do not prove REALITY + XHTTP/H3. This is an explicit
interop limitation, not a NativeCore fallback or a silently delegated
compatibility path.

The legacy REALITY `Show` compatibility field is retained only for config
decoding. Native production handshakes do not write per-flight debug traces or
derived authentication material to stdout, so enabling that legacy field
cannot create an unbounded log stream or disclose client ShortIDs.

用 `pandora-native --version` 验证发布版本；用 `systemctl stop pandora-native` 后检查端口释放，再执行回滚。

## 排查：节点不上报心跳

面板的 `nodes.last_heartbeat_at` 只由 UniProxy 的 `status` 接口写入，而订阅
下发会跳过从未上报过心跳的节点。所以「没有心跳」的后果不是告警，是这个节点
被静默排除在订阅之外——内核照常转发、用户照常同步，两边都不报错。

2026-08-22 在一台 1 核 2G 的共享节点机上排查过一次，根因是两处部署漂移，两个都值得先查：

**一、二进制比源码旧。** 状态上报是 2026-08-08（`657b95d`）才加进 `panel/status.go`
的，早于这个日期编出来的二进制根本没有这个调用。

```bash
ls -la /usr/local/bin/pandora-native          # 看编译日期
journalctl -u pandora-native -n 50 --no-pager # 有 status 相关日志吗
```

**二、装上去的 unit 和仓库里的不是同一份。** 当时机器上是一份手写 unit：
以 root 运行、没有 `StateDirectory`、也没把 `/var/lib/pandora-native` 放进
`ReadWritePaths`。配合 `CapabilityBoundingSet=CAP_NET_BIND_SERVICE`（丢掉了
`CAP_DAC_OVERRIDE`），root 反而读不进属主为 `pandora` 的状态目录，签名通道
静默降级成兼容通道，只在启动时打一行 WARN。

升级前先比一次，漂移了就用仓库这份覆盖：

```bash
diff /etc/systemd/system/pandora-native.service pandora-native.service
```

本目录的 `pandora-native.service` 是唯一正确的那份：`User=pandora`、
`StateDirectory=pandora-native`、`ReadWritePaths` 一样不缺。配套的权限要求：

```bash
chown root:pandora /etc/pandora-native/config.json && chmod 0640 /etc/pandora-native/config.json
chown -R pandora:pandora /var/lib/pandora-native  && chmod 0700 /var/lib/pandora-native
```

**签名通道**另需 `/var/lib/pandora-native/identity.json`，由 enrollment 流程签发。
没有它时日志报 `no such file or directory` 并降级到兼容通道；若报的是
`permission denied`，那是上面第二个问题，不是没签发。
