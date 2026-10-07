# 服务器级绑定合约（v1）

一台机器装一次 pdnd（pandora-native），在面板里绑定成一台「服务器」，可以同时绑定多个面板。节点由面板在服务器下创建、下发，机器上不需要任何操作。本文是面板与 pdnd 两端都要遵守的线上合约：绑定文件、签名原像、清单、名单、合并上报、事件流、失败码、链路钉住、面板触发升级、旧节点自动升级。

- 本文只用占位：面板地址写作 `https://<IP:端口>` 或 `panel.example.com`，密钥写作 `<…>`。仓库公开，不写任何真实部署值。
- 两端的原像实现：
  - 面板：`panel/internal/platform/bindingcontract`；
  - pdnd：`pdnd/bindingcontract`。
- 两端用同一份金样本 `testdata/bindingcontract-v1-golden.json`，两份逐字节相同（§17）。
- 本合约是第 0 阶段（P0）的产物，只定义格式，不接线。表结构、接口实现、内核改动分别在后续阶段 P1–P7 做（§18）。

## 目录

0. 通用约定
1. 绑定文件 `bindings.d/<id>.json`
2. 服务器请求签名 `aegis-server-request-v1`
3. 接入 S1
4. 清单 S2 `aegis-server-manifest-v1`（含墓碑）
5. 批量生效配置 S3（沿用 v1 节点合约）
6. 按池名单 S4 `aegis-server-users-v1`
7. 合并上报 S5
8. 事件流 S6
9. 机器侧解绑 S7
10. 失败码
11. 链路钉住：网关 SPKI 与 `--panel-key`
12. 「同一面板同一租户只绑一台服务器」的判定键
13. 面板触发升级：升级指令与官方发布清单
14. 旧节点自动升级 `POST /v1/nodes/upgrade-to-server`
15. 兼容与协商（features）
16. 发布签名：现状与最小方案
17. 金样本
18. 留给后续阶段

---

## 0. 通用约定

### 0.1 签名原像的写法

所有新原像沿用生效发布契约（`aegis-node-effective-config-release-v1`）的写法：

- 第一行是合约名（域分隔串），其后每行一个 `key=value`；
- 每一行，包括最后一行，都以一个 LF（`\n`）结尾；不用 CRLF，不加 BOM；
- 键名、键序固定，见各节表格；值为空时照样写出 `key=`；
- 列表类内容（清单的节点、发布清单的产物）每项一行，行首是固定标记（`node`、`artifact`），其后是空格分隔、键序固定的 `key=value`；
- 每个值都先按规范形式校验，再拼接。非规范输入一律拒绝，不做改写。所以同一组语义只有一种字节表示。

签名算法一律是 Ed25519（RFC 8032，纯 Ed25519，不预哈希），线上的签名值是 64 字节签名的标准 base64（带填充）。

### 0.2 字段的规范形式

| 类型 | 规范形式 | 例 |
|---|---|---|
| UUID | 小写、带连字符、非全零 | `0193f0b0-2222-7000-8000-0000000000b1` |
| 内容哈希（`*_sha256`，面板侧载荷） | sha256 的标准 base64（带填充，44 个字符） | `47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=` |
| 产物哈希（发布清单 `sha256`） | sha256 的 64 位小写十六进制，与 `sha256sum` 输出相同 | `1bc9…89a6` |
| 公钥（线上） | 32 字节原始公钥的标准 base64 | — |
| key id | `base64url(sha256(公钥原始字节)[:8])`，无填充，11 个字符；与现有配置签名 key id 同一种格式 | `0mMJ8wMlBzo` |
| nonce | 16 字节随机数的无填充 base64url（22 个字符），与节点请求 nonce 同格式 | `EREREREREREREREREREREQ` |
| 请求时间戳 `ts` | RFC3339 UTC，精确到秒，以 `Z` 结尾 | `2026-10-07T08:00:00Z` |
| 签发与过期时间 | Go `time.RFC3339Nano` 的 UTC 写法，精度不超过微秒（与 PostgreSQL 往返一致），去掉末尾的 0 | `2026-10-07T08:00:00.123456Z` |
| 代际、版本号 | 十进制，无前导零，1 到 2^63−1 | `17` |
| serial | 十进制，1 到 2^31−1 | `1` |
| agent 版本 | `vMAJOR.MINOR.PATCH`，各段十进制、无前导零、不超过 uint32，不带预发布或构建后缀 | `v1.6.0` |

### 0.3 签名有效窗口与防回放

- 清单、名单、升级指令：`expires_at - issued_at` 大于 0 且不超过 10 分钟，与生效发布一致。
- 这三类响应的原像都含 `request_nonce`（发起本次请求时的 `X-Server-Nonce`）和 `serial`（服务器身份序号）：
  - 截获的响应不能拿去回答另一个请求；
  - 换了服务器身份之后，旧身份时期的响应一律不被接受。
- 落盘缓存连同 nonce 一起保存。启动时重新验签，只核签名、身份和代际单调，**不核过期时间**；过期时间只约束网络上刚收到的响应。

### 0.4 线上 JSON

- 两端解码一律拒收未知字段，与面板 `httpx.DecodeJSON` 的 `DisallowUnknownFields` 一致。
- 新字段、新枚举值只能在对方的 `features` 声明支持之后才发送（§15）。
- 请求体上限沿用面板的 1 MiB，响应体上限沿用 pdnd 的 2 MiB。超出的上报按 §7.4 拆分。

---

## 1. 绑定文件 `bindings.d/<id>.json`

### 1.1 位置与写入规则

- 目录：全局配置的 `bindings_dir`，缺省 `/etc/pandora-native/bindings.d`。一个绑定一个文件，文件名是 `<id>.json`。
- 只有 root 执行的 CLI（`pandora-native bind` / `unbind`）写这个目录。写法：
  1. 写临时文件 `.<id>.json.tmp`；
  2. fsync；
  3. rename 成正式文件名；
  4. fsync 目录。
- 文件权限 0600，属主 root，属组 pandora，服务进程只读。
- 服务进程运行时会变的状态不写回绑定文件，一律放在 `<state_dir>/bindings/<id>/`（§1.4）。原因是 systemd 单元用了 `ProtectSystem=strict`，`/etc` 对服务进程只读。
- `id` 是 8 字节随机数的 16 位小写十六进制，机器本地生成。
- 文件名必须等于 `<id>.json`。不匹配 `^[0-9a-f]{16}\.json$` 的文件（包括 `.tmp`）一律忽略，并打一条告警。
- 解码拒收未知字段。坏文件只让这一个绑定不启动，并在本地告警，不影响其它绑定。

### 1.2 `kind: pandora-server`（服务器级绑定）

```jsonc
{
  "version": 1,
  "id": "b7f3c2e1a9d04f6b",
  "kind": "pandora-server",
  "panel_url": "https://<IP:端口>",              // 写入前已按 §12 规范化
  "tls_pin": "sha256//<网关 SPKI 的 sha256，标准 base64>",
  "panel_key": "sha256:<面板配置签名公钥 sha256 的 64 位小写十六进制>",
  "tenant_id": "<uuid>",
  "server_id": "<uuid>",
  "bound_at": "2026-10-07T08:00:00Z"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `version` | 是 | 固定为 1 |
| `id` | 是 | 等于文件名去掉 `.json` |
| `kind` | 是 | `pandora-server` |
| `panel_url` | 是 | `CanonicalPanelOrigin` 的输出：`https://host:port`；`http://` 只允许回环地址，用于开发与验收 |
| `tls_pin` | `https` 时必填 | §11.1。回环 `http` 时为空串 |
| `panel_key` | 是 | §11.2 |
| `tenant_id`、`server_id` | 是 | 取自接入响应 |
| `bound_at` | 是 | 绑定完成的时间（RFC3339 UTC 秒）。冷启动时多个绑定按它排先后，见 §1.5 |

- 身份密钥不写在绑定文件里，也不允许用字段指定路径。路径固定由 `state_dir` 和 `id` 推出，避免绑定文件把私钥指到任意位置。

### 1.3 `kind: uniproxy-node`（兼容绑定：按节点对接 Xboard / V2board）

```jsonc
{
  "version": 1,
  "id": "4c0d9e2f7a1b3c5d",
  "kind": "uniproxy-node",
  "panel_url": "https://panel.example.com:443",
  "tls_pin": "",                // 可选：填了就按 §11.1 钉住，不填走系统 CA 校验
  "node_id": "12",              // 第三方面板的节点 ID，十进制
  "node_type": "vless",         // UniProxy 的 node_type
  "bound_at": "2026-10-07T08:00:00Z"
}
```

- 运行令牌不进绑定文件，放在 `<state_dir>/bindings/<id>/token`，权限 0600。
- 兼容绑定沿用现有 UniProxy 通道，未签名。它永远拿不到托管证书，也不参与面板触发升级。
- `config.json` 里旧格式的 `panel` + `nodes[]` 在启动时视作一个隐式的 legacy 绑定，行为与现在相同，直到 §14 自动升级把它迁走。

### 1.4 每个绑定的状态目录 `<state_dir>/bindings/<id>/`

目录权限 0700，属主 pandora。

| 文件 | 内容 |
|---|---|
| `identity.json` | 服务器身份：Ed25519 私钥、`serial`，以及当前钉住的面板配置公钥与 `config_key_id`（随换钥迁移而更新） |
| `enc.key` | X25519 私钥原始 32 字节，权限 0600。与证书设计共用，每个绑定各一把（cert-design §3.1） |
| `state.json` | 已接受的最高 `manifest_generation`、已接受的 `tls_pin_next`、升级指令的处理记录 |
| `cache/` | 清单、名单、生效发布的落盘缓存，连同请求 nonce 一起存，启动时重新验签 |
| `token` | 只有兼容绑定才有 |

- 每个绑定一套独立的密钥：两个面板拿到的是不同的公钥，无法凭公钥判断它们共用一台机器（IP 相同属于无法避免的部分）。

### 1.5 进程级约束

- 入站 tag 写作 `<绑定ID>/<协议>-<节点ID>`。
- 端口登记表的键是 `(端口, L4)`，值是 `(scope=绑定ID, node_id)`。「同一面板」的判定就是「scope 相同」。
- 冷启动顺序：
  1. 先按 `bound_at` 升序，相同时按 `id` 升序；
  2. 同一绑定内按清单的节点顺序；
  3. 串行地首次 bind，保证每次重启赢家一致。

---

## 2. 服务器请求签名 `aegis-server-request-v1`

`/v1/servers/*` 下除接入 begin 之外的所有请求，都由服务器身份私钥签名。

### 2.1 请求头

| 头 | 值 |
|---|---|
| `X-Server-Id` | server_id（UUID） |
| `X-Server-Serial` | 服务器身份 serial |
| `X-Server-Ts` | 请求时间戳（RFC3339 UTC 秒） |
| `X-Server-Nonce` | 16 字节随机数（base64url，22 字符），每个请求新取 |
| `X-Server-Sig` | 对原像的 Ed25519 签名，标准 base64 |
| `X-Report-Id` | 只有合并上报带（§7.1），UUID；其它请求不带 |

租户不放在请求头里：面板按自己的租户解析得出，pdnd 取自绑定文件。两边各自把租户写进原像，对不上就验不过。

### 2.2 原像

```
aegis-server-request-v1
method=<方法>
target=<路径[?查询]>
tenant_id=<uuid>
server_id=<uuid>
serial=<serial>
ts=<RFC3339 UTC 秒>
nonce=<base64url 16 字节>
report_id=<X-Report-Id，没有就为空>
body_sha256=<base64(sha256(请求体原始字节))>
```

| 字段 | 规则 |
|---|---|
| `method` | `GET`、`POST`、`PUT`、`PATCH`、`DELETE` 之一，大写 |
| `target` | 见 §2.3 |
| `body_sha256` | 对线上收到的原始请求体字节取 sha256，没有请求体时就是空串的哈希（`47DEQpj8…uFU=`） |
| `report_id` | 有 `X-Report-Id` 头时必须是规范 UUID，原样写入；没有这个头时写 `report_id=`。上报编号因此受签名保护，中间人不能改编号让同一份流量记两次账 |

金样本：`server_requests`，含两条合法向量（带上报编号的 POST、带查询串的 GET）和七条必须拒绝的向量。

### 2.3 请求目标的规范形式

- 路径以 `/v1/servers/` 开头，只含 `[A-Za-z0-9._~-]` 和段分隔符 `/`：
  - 没有空段（`//`）、没有 `.` 与 `..` 段，不以 `/` 结尾；
  - 不出现百分号编码。
- 查询串可选，紧跟一个 `?`：
  - 由 `key=value` 用 `&` 连接，`?` 后不能为空；
  - 键匹配 `[a-z][a-z0-9_]{0,31}`，**严格升序且不重复**；
  - 值只含 `[A-Za-z0-9._~-]`，最长 128；
  - 整个目标最长 512 字节。
- 面板验签时用 `r.URL.EscapedPath()`，有查询串时再接上 `"?" + r.URL.RawQuery`，按上述规则校验通过后再进原像。不规范的目标直接 401。
- 这样路径与查询都不需要编码，同一个请求只有一种写法。

### 2.4 面板的验签流程

1. 五个签名头（`X-Server-Id`、`X-Server-Serial`、`X-Server-Ts`、`X-Server-Nonce`、`X-Server-Sig`）齐全，否则 401。
2. `ts` 是规范写法，且与面板时钟相差不超过 ±5 分钟。
3. 读请求体（上限 1 MiB），重算 `body_sha256`，拼原像。
4. 按（租户，server_id，serial）取 `server_identities` 里状态为 active 的公钥验签。吊销、不存在、serial 不符一律 401，文案统一为「服务器身份校验失败」，不区分原因。
5. 认领 nonce：复用 w3node 的 Valkey nonce 守卫，键空间按 server 隔离；认领失败回 401。
6. 后续 handler 只认这个身份所属的 `server_id`。对 `node_id` 的任何引用都必须校验 `node.server_id = 该 server`。

pdnd 侧对 2xx 以外的响应一律返回 `StatusError`，沿用 `SignedClient.do` 的约定。

---

## 3. 接入 S1

接口照搬节点接入的 begin → status → commit / abort 证据链，只换成服务器身份。字段细节在 P1 定稿，这里只钉住和本合约相关的部分。

### 3.1 begin：`POST /v1/servers/enrollments`

begin 不签名，由一次性绑定令牌证明身份：`bootstrap_tokens` 增加 `kind=server`，令牌绑定 server_id、只能用一次、1 小时内有效。

请求体：

```jsonc
{
  "token": "<绑定令牌>",
  "request_id": "<uuid，pdnd 生成，用于重试幂等>",
  "public_key": "<服务器身份 Ed25519 公钥，标准 base64>",
  "enc_kem": "dhkem-x25519-hkdf-sha256",
  "enc_public_key": "<X25519 公钥，标准 base64>",
  "agent_version": "v1.6.0",
  "features": ["…"],                       // §15
  "capabilities": { … },                   // pandora-native --capabilities --json 的输出
  "hostname": "…", "cpu_cores": 2, "memory_mb": 2048, "disk_gb": 40
}
```

响应体：

```jsonc
{
  "enrollment_id": "<uuid>", "tenant_id": "<uuid>", "server_id": "<uuid>", "serial": 1,
  "state": "pending", "expires_at": "…",
  "config_key_id": "<key id>", "config_public_key": "<面板配置签名公钥，标准 base64>",
  "features": ["…"]
}
```

pdnd 收到响应后按顺序做三项检查：

1. `CheckPanelKey(--panel-key, config_public_key)` 通过，并且 `KeyID(config_public_key) == config_key_id`。不通过就中止（§11.2）。
2. 按 §12 判定这台机器上是否已经有同一面板同一租户的绑定：
   - 已有、且 `server_id` 相同：这是重复执行，abort 本次接入，回报「已绑定」，退出码 0；
   - 已有、但是另一台服务器：abort，报错退出。
3. 两项都通过，才继续 commit。

### 3.2 status / commit / abort

- 路径：`/v1/servers/enrollments/{id}/status`、`/commit`、`/abort`。
- 由新生成的服务器身份按 §2 签名，`server_id` 与 `serial` 取自 begin 的响应。commit 的签名同时证明 pdnd 持有新私钥。
- commit 请求体沿用节点接入的证据字段：`agent_version`、`architecture`、`binary_sha256`、`config_sha256`、`unit_sha256`、`preflight_sha256`。
- 面板核对 `binary_sha256` 的口径改为：它必须出现在某份**官方签名的发布清单**里，并且版本不低于面板随包的版本（§13.6）。
- commit 成功之后才原子写绑定文件。中途任何失败都先 abort，不留下半个绑定。

---

## 4. 清单 S2 `aegis-server-manifest-v1`

接口：`GET /v1/servers/manifest`，带 `If-None-Match`，没有变化时回 304。

### 4.1 线上 JSON

```jsonc
{
  "contract": "aegis-server-manifest-v1",
  "tenant_id": "<uuid>", "server_id": "<uuid>", "serial": 1,
  "manifest_generation": 17,
  "state": "bound",                       // bound | unbound
  "unbound_reason": "",                   // 只在 unbound 时非空
  "features": ["…", "…"],                 // 面板支持的特性，严格升序
  "min_agent_version": "v1.5.0",          // 可空
  "tls_pin_next": "sha256//…",            // 可空：预告的下一个网关 SPKI
  "intervals": { "manifest_pull_seconds": 15, "users_pull_seconds": 15,
                 "report_seconds": 60, "stream_online_pull_seconds": 60 },
  "nodes": [
    { "node_id": "<uuid>", "protocol": "vless", "port": 443, "l4": "tcp",
      "pool_id": "<uuid>", "effective_generation": 7, "content_sha256": "<base64>" },
    { "node_id": "<uuid>", "protocol": "hysteria2", "port": 8443, "l4": "udp",
      "pool_id": "", "effective_generation": 3, "content_sha256": "<base64>" }
  ],
  "agent_upgrade": null,                  // 或 §13.2 的升级指令对象（单独签名）
  "key_id": "<配置签名 key id>",
  "issued_at": "…", "expires_at": "…",
  "signature": "<base64>"
}
```

- `request_nonce` 不在 JSON 里：它就是本次请求的 `X-Server-Nonce`，pdnd 自己记得。
- 节点的 `content_sha256` 与 `effective_generation` 指向该节点当前的生效发布。pdnd 据此判断要不要用 S3 取新 release。

### 4.2 原像

```
aegis-server-manifest-v1
tenant_id=<uuid>
server_id=<uuid>
serial=<serial>
manifest_generation=<n>
state=bound|unbound
unbound_reason=<原因或空>
features=<逗号连接，或空>
min_agent_version=<vX.Y.Z 或空>
tls_pin_next=<钉住值或空>
manifest_pull_seconds=<5..3600>
users_pull_seconds=<5..3600>
report_seconds=<5..3600>
stream_online_pull_seconds=<5..3600>
request_nonce=<本次请求的 X-Server-Nonce>
key_id=<配置签名 key id>
issued_at=<时间>
expires_at=<时间>
node_count=<节点数>
node node_id=<uuid> protocol=<协议> port=<端口> l4=<tcp|udp> pool_id=<uuid 或空> effective_generation=<n> content_sha256=<base64>
…（每个节点一行，按 JSON 里的顺序）
```

| 字段 | 规则 |
|---|---|
| `state` | `bound`：`unbound_reason` 必须为空。`unbound`：`unbound_reason` 必须是 `server_deleted`、`server_unbound`、`identity_revoked`、`machine_unbound` 之一，且 `nodes` 必须为空 |
| `features` | 每个匹配 `[a-z0-9][a-z0-9.-]{0,47}`，严格升序、不重复 |
| `protocol` | `[a-z0-9][a-z0-9-]{0,31}` |
| `l4` | `tcp` 或 `udp`，口径与 pdnd 端口登记表相同 |
| `pool_id` | 节点不在任何池时为空串，这时它的名单为空 |
| 节点 | 最多 4096 个，`node_id` 不重复。同一 `(port, l4)` 出现两次不算格式错误：面板建节点时已经回 409，万一出现，由 pdnd 运行时对后一个报 `port_in_use` |
| 节点顺序 | 有意义：面板按节点创建时间升序排，pdnd 冷启动按这个顺序串行 bind，原像按原顺序写，不排序 |

金样本：`manifests`，含两条合法向量（两个节点的正常清单、注销墓碑）和七条必须拒绝的向量。

### 4.3 pdnd 的接受规则

全部满足才接受：

1. 签名用本绑定当前钉住的配置公钥验过，`key_id` 与之相符。`key_id` 对不上时先按换钥流程（§18）补查，补查失败就拒收。
2. `tenant_id`、`server_id`、`serial` 与本绑定一致；`request_nonce` 等于本次请求发出的 nonce；现在落在 `[issued_at, expires_at)` 之内。
3. `manifest_generation` ≥ `state.json` 记录的最高值。小于就拒收（防回放）。等于时，内容必须与缓存逐字节相同，否则拒收并告警（同代不同内容）。
4. 通过后先落盘，再推进最高值，然后与运行中的节点求差：新增或变了的节点走 S3，消失的节点 RemoveInbound。

### 4.4 注销墓碑（`state=unbound`）

- 只有**验签通过**的墓碑才触发清理：
  1. 停掉本绑定名下的全部入站；
  2. 最后一次上报流量；
  3. 删除 `<state_dir>/bindings/<id>/`；
  4. 删除绑定文件。服务进程写不了 `/etc` 时，写一个 `revoked` 标记，以后忽略这个绑定，由下一次 CLI 调用清掉文件。
- 未签名的 401 或 404、验签失败、身份不符，**一律不触发清理**：只告警，沿用缓存继续服务。这样明文或被劫持的链路伪造不了解绑。
- 墓碑同样受 `request_nonce` 和 `serial` 约束，不能拿旧身份时期的墓碑去注销重新绑定后的新身份。

---

## 5. 批量生效配置 S3

- 接口：`POST /v1/servers/effective-configs`，请求体 `{"node_ids": ["<uuid>", …]}`（最多 64 个，不重复）。
- 响应：`{"releases": [<生效发布>, …]}`。
- 每份生效发布**原样沿用现有 v1 节点合约** `aegis-node-effective-config-release-v1`：
  - 字段、原像、签名与校验规则都不变，定义见 `panel/internal/domain/nodefabric/effective_release_codec.go`、`pdnd/panel/effective_release.go`；
  - 本合约不重复定义。
- pdnd 的额外校验：
  - 每份 release 的 `node_id` 必须在当前清单里；
  - `generation` 与 `content_sha256` 必须与清单里该节点的值相同。对不上的那一份丢弃，等下一轮清单。
- 一份 release 验签失败只影响那一个节点：它报 `config_invalid`，其它节点照常应用。
- 面板侧：只返回属于这台服务器的节点；不属于的 node_id 直接略过，不报错，也不说明原因（免得被用来探测节点是否存在）。

---

## 6. 按池名单 S4 `aegis-server-users-v1`

- 接口：`GET /v1/servers/users?pool=<pool_id>`，带 `If-None-Match: "<pool_id>:<users_version>"`，没有变化时回 304。
- 同一服务器上同池的节点共用一份名单。

### 6.1 线上 JSON

```jsonc
{
  "contract": "aegis-server-users-v1",
  "tenant_id": "<uuid>", "server_id": "<uuid>", "serial": 1,
  "pool_id": "<uuid>", "users_version": 42, "kind": "full", "user_count": 2,
  "content_sha256": "<base64(sha256(payload 原始字节))>",
  "payload": {"users":[{"id":1,"uuid":"<uuid>","speed_limit":0,"device_limit":0}, …]},
  "key_id": "<配置签名 key id>", "issued_at": "…", "expires_at": "…",
  "signature": "<base64>"
}
```

- `payload` 里每个用户的形状与 UniProxy 用户一致：`id`（int64）、`uuid`、`speed_limit`、`device_limit`。
- `content_sha256` 对响应体里 `payload` 值的**原始字节**取哈希。pdnd 用 `json.RawMessage` 取出原样字节重算。
- 面板必须对实际写出的字节取哈希：
  - `encoding/json` 编码 `RawMessage` 时会压缩空白，并转义 `<`、`>`、`&`；
  - 名单里只有整数和 UUID，不会出现这三个字符；
  - 但 P2 的面板实现仍要有一条「编码后再解出、哈希不变」的测试。

### 6.2 原像

```
aegis-server-users-v1
tenant_id=<uuid>
server_id=<uuid>
serial=<serial>
pool_id=<uuid>
users_version=<n>
kind=full
user_count=<用户数>
content_sha256=<base64>
request_nonce=<本次请求的 X-Server-Nonce>
key_id=<配置签名 key id>
issued_at=<时间>
expires_at=<时间>
```

- v1 只有 `kind=full`。增量名单以后作为新的 `kind` 取值，经 `features` 协商后才会出现（§8.3）。
- `user_count` 必须等于 `payload.users` 的长度，pdnd 解析后核对。

金样本：`users`，含一条合法向量（含 `payload` 原文）和两条必须拒绝的向量。

---

## 7. 合并上报 S5

接口：`POST /v1/servers/report`，按 §2 签名。

### 7.1 上报编号放在请求头 `X-Report-Id`

- 每次上报生成一个 UUID，放在 `X-Report-Id` 头里，**不放进请求体**。原因：
  - 面板解码拒收未知字段（`panel/internal/platform/httpx/httpx.go` 的 `DecodeJSON` 开着 `DisallowUnknownFields`）；
  - 编号放在头里，请求体结构以后演进时不会和它纠缠。
- 编号经原像的 `report_id` 行受签名保护（§2.2）。
- 重试必须用**同一个编号和同一份请求体**。面板按（server_id，report_id）去重至少 24 小时：重复的上报回 200，但不重复记账。
- 请求体的哈希与已记录的不同、编号却相同时，面板回 409，不记账。

### 7.2 请求体

```jsonc
{
  "agent_version": "v1.6.0",
  "features": ["…"],                        // pdnd 支持的特性，严格升序
  "applied_manifest_generation": 17,
  "capabilities": null,                     // 只在能力表变化时（例如升级之后）带完整对象
  "machine": { "cpu_bp": 1234, "mem_used_mb": 512, "mem_total_mb": 2048,
               "disk_used_gb": 3, "disk_total_gb": 40, "load1_cbp": 12, "load5_cbp": 10,
               "load15_cbp": 8, "net_rx_bytes": 0, "net_tx_bytes": 0, "tcp_conns": 40,
               "uptime_sec": 3600 },        // 与 nodefabric.Metrics 同一口径
  "nodes": [
    {
      "node_id": "<uuid>",
      "runtime_state": "failed",            // running | pending | failed | stopped
      "applied_release_id": "<uuid>", "applied_generation": 7,
      "applied_content_sha256": "<base64>",
      "failure": { "code": "port_in_use", "detail": "端口 443/TCP 已被本机其他服务占用",
                   "at": "2026-10-07T08:00:00Z" },
      "warnings": [ { "code": "cert_expiring", "detail": "证书剩余 2 天", "at": "…" } ],
      "receipt": { "release_id": "<uuid>", "generation": 7, "content_sha256": "<base64>",
                   "result": "failed", "detail": "…" },
      "traffic": { "1": [1024, 2048] },     // 与 UniProxy push 同形：用户 id → [上行, 下行] 字节增量
      "alive":   { "1": ["<客户端IP>"] }     // 与 UniProxy alive 同形：用户 id → 在线 IP
    }
  ],
  "agent_upgrade": { "order_id": "<uuid>", "target_version": "v1.6.0", "state": "failed",
                     "failure": { "code": "upgrade_hash_mismatch", "detail": "…", "at": "…" },
                     "at": "…" }
}
```

| 字段 | 规则 |
|---|---|
| `runtime_state` | `running`、`pending`、`failed`、`stopped` |
| `failure` | 只在 `runtime_state=failed` 时出现，`code` 取自 §10。`detail` 最长 512 字符；只有同一 scope（同一绑定）时才允许写对方节点 ID，别的面板的节点、端口、用户一概不写 |
| `warnings` | 不阻断运行的提示，目前只有 `cert_expiring` |
| `receipt.result` | `switched`、`health_passed`、`failed`，语义与现有配置回执相同。每个版本只报一次，送不到就随下一次上报补报 |
| `traffic`、`alive` | 只出现在**本服务器**的节点下。面板拆开后写进现有流量账和在线表，记账逻辑复用 `uniproxy_traffic.go` |
| `agent_upgrade.state` | `accepted`、`downloading`、`installing`、`succeeded`、`failed`、`rolled_back`。失败码见 §10.3 |

### 7.3 面板的处理

- 每个 `node_id` 都必须属于这台服务器。
  - 已经退役、但还没销毁的节点，仍然接收它最后一次的流量。
  - 不属于这台服务器的条目略过，记进响应的 `rejected_node_ids`，不让整份上报失败。被略过的条目 pdnd 不重试。
- 节点状态写进 `node_config_applications`，并带上 w4deliver 给节点加的 desired、applied 和最近失败。
- 失败状态翻转时发管理员通知。
- 响应（不签名，只作提示）：

  ```jsonc
  { "manifest_generation": 18, "rejected_node_ids": [] }
  ```

  pdnd 看到 `manifest_generation` 比本地高，就提前拉一次清单。

### 7.4 体积

- 单次上报的请求体不超过 1 MiB。
- 超出时 pdnd 把 `traffic`、`alive` 拆到多次上报里，每次用自己的 `X-Report-Id`。节点状态、`machine` 和 `agent_upgrade` 只放在第一份。

---

## 8. 事件流 S6

- 接口：`GET /v1/servers/stream`，按 §2 签名，`text/event-stream`。每台服务器一条，同一绑定只保持一条连接。

### 8.1 事件类型

| 事件 | `data`（JSON） | pdnd 的动作 |
|---|---|---|
| `sync.manifest` | `{"manifest_generation": 18}` | 拉清单 S2 |
| `sync.config` | `{"node_ids": ["<uuid>", …]}` | 先拉清单，再对清单里变了的节点走 S3 |
| `users.delta` | `{"pool_id": "<uuid>", "users_version": 43}` | 拉该池名单 S4（v1 拉整份，见 §8.3） |
| `users.full` | `{"pool_id": "<uuid>", "users_version": 43}` | 拉该池名单 S4 |
| `ping` | `{}` | 心跳，约 25 秒一帧，只用于保活和判断连接健康 |

### 8.2 事件只是提示，不签名

- 事件只说「什么变了、该拉了」，**不携带任何数据**，和面板 `platform/realtime` 的原则一致。
- pdnd 收到事件后，只是提前做一次对应的**签名拉取**，所有生效动作都以拉回来、验签通过的数据为准。所以伪造事件最多让 pdnd 多拉几次。
- pdnd 对事件触发的拉取做合并和限速：同一资源 2 秒内最多一次。
- 不明事件类型一律忽略。
- 解绑、停服之类的破坏性动作**只能**由验签通过的清单墓碑触发，事件不能触发。
- 事件流只是加速通路：断线时按退避重连，不向上冒泡，轮询一直在。事件流在线时，按清单的 `stream_online_pull_seconds` 兜底拉取。这些规则沿用现有节点 SSE 的约定。

### 8.3 关于增量名单

- 设计稿想让 `users.delta` 直接推增量。v1 先不这么做：
  - 事件不签名，不能携带数据；
  - 签名的增量需要定义「基于哪个版本」和合并规则。
- v1 里 `users.delta` 和 `users.full` 都只触发整份拉取，带 ETag，名单没变时只回 304。
- 以后加特性 `users-delta-v1`：S4 增加 `kind=delta`，原像增加 `base_version` 行，pdnd 在版本衔接不上时退回整份拉取。

---

## 9. 机器侧解绑 S7

- 接口：`POST /v1/servers/unbind`，按 §2 签名，请求体 `{"reason": "machine_unbound"}`。
- 面板把服务器标为「未绑定」、吊销该身份，回 200 `{}`。
- pdnd 的顺序：
  1. 停入站；
  2. 最后一次 S5；
  3. 发 S7（失败也继续）；
  4. 删除状态目录和绑定文件。

---

## 10. 失败码

### 10.1 节点失败码（S5 `nodes[].failure.code` 与 `warnings[].code`）

| 码 | 面板中文文案 | 触发 |
|---|---|---|
| `port_in_use` | 端口已被占用 | 端口登记表里已有占用者，或 bind 返回 EADDRINUSE。detail 只在同一 scope 时写出对方节点 ID |
| `port_reserved` | 端口是本机保留端口，不能用于节点 | 命中机器本地的 `reserved_ports`（缺省 22/tcp，80/tcp 预留给 ACME） |
| `cert_missing` | 找不到节点证书 | 文件模式下证书文件不存在，或托管证书还没下发 |
| `cert_invalid` | 节点证书无效 | 解析失败、与私钥不匹配、不覆盖节点域名，或已经过期 |
| `cert_expiring` | 节点证书即将过期 | **只作 warning**：节点照常运行。阈值按证书设计的寿命缩放规则计算 |
| `protocol_unsupported` | 节点程序版本不支持该协议，请升级节点程序 | 能力表里没有这个协议或传输组合 |
| `config_invalid` | 节点配置无效 | 生效发布验签失败、字段非法，或内核拒绝这份配置 |
| `bind_permission` | 节点程序没有权限监听该端口 | bind 返回 EACCES，例如低于 1024 的端口缺 `CAP_NET_BIND_SERVICE` |

### 10.2 展示规则

- 面板显示「文案：detail（时间）」。detail 是 pdnd 的补充说明，原样显示，前端照常转义。
- 未知的码：
  - 形如 `[a-z0-9_]{1,64}` 的，显示「节点上报了未知错误（<码>）」；
  - 其它形状的不回显原码。
  - 未知的码不当成错误拒收，这样新 pdnd 配旧面板时状态仍然可见。
- 面板实现：`bindingcontract.FailureMessage`，文案在 `panel/internal/platform/bindingcontract/codes_zh.go`。

### 10.3 升级失败码（S5 `agent_upgrade.failure.code`）

| 码 | 面板中文文案 |
|---|---|
| `upgrade_order_invalid` | 升级指令无效或已过期 |
| `upgrade_release_unavailable` | 取不到该版本的发布清单或程序 |
| `upgrade_signature_invalid` | 发布清单的官方签名校验失败 |
| `upgrade_hash_mismatch` | 下载的程序与发布清单的哈希不一致 |
| `upgrade_downgrade_refused` | 目标版本不高于当前版本，已拒绝降级 |
| `upgrade_install_failed` | 替换程序失败，仍在运行原版本 |
| `upgrade_rolled_back` | 新版本启动后自检失败，已回滚到原版本 |

所有枚举（失败码、warning 专用码、`runtime_state`、回执结果、升级状态、升级失败码、事件类型、墓碑原因）都写在金样本的 `enums` 里，两端的常量必须与它逐项、同序一致。

---

## 11. 链路钉住：网关 SPKI 与 `--panel-key`

两道钉住各管一件事，互不替代：

- **TLS 钉住**管链路的机密性和对端身份；
- **面板公钥钉住**管所有签名数据的来源。

### 11.1 网关 SPKI 钉住（`--pin`、绑定文件 `tls_pin`）

- 面板安装时生成一把长期的 P-256 网关密钥和自签证书，节点网关用它提供 HTTPS。浏览器访问后台仍是 `http://<IP:端口>`。
- 钉住值的格式：
  - `sha256//` 后接**叶子证书 SubjectPublicKeyInfo DER**（`x509.Certificate.RawSubjectPublicKeyInfo`）的 sha256，标准 base64，带填充；
  - 与 curl `--pinnedpubkey` 的写法完全一致。
- 安装命令：

  ```sh
  curl -fsS -k --pinnedpubkey 'sha256//<SPKI>' https://<IP:端口>/pdnd/install.sh | sh -s -- \
    --panel https://<IP:端口> --pin 'sha256//<SPKI>' --panel-key sha256:<指纹> --token <绑定令牌>
  ```

  curl 带 `--pinnedpubkey` 时，即使加了 `-k`，也会校验钉住的公钥。
- pdnd 的 TLS 配置：
  - `InsecureSkipVerify: true`，加上 `VerifyConnection`：对 `ConnectionState.PeerCertificates[0].RawSubjectPublicKeyInfo` 调 `CheckTLSPin(spki, tls_pin, tls_pin_next)`，任一命中才放行；
  - 用 `VerifyConnection` 而不是 `VerifyPeerCertificate`，因为后者在会话恢复时不会被调用。同时不设 `ClientSessionCache`；
  - `MinVersion` 至少 TLS 1.2；
  - 不把面板地址当作 SNI 校验的依据。
- 换网关密钥：
  1. 面板在签名清单里填 `tls_pin_next` 预告新值；
  2. pdnd 把它记进 `state.json`；
  3. 在有效期内，新旧两个钉住值都接受；
  4. 第一次用新值握手成功后，把它提升为当前值。
- 任何未经签名清单预告的新 SPKI 一律拒绝连接。
- 回环 `http://` 只供开发与验收，不钉住。

### 11.2 面板公钥钉住（`--panel-key`、绑定文件 `panel_key`）

- 格式：`sha256:` 后接面板配置签名公钥（32 字节原始 Ed25519）**完整** sha256 的 64 位小写十六进制。
  - 不用 11 字符的 key id 当钉住值：key id 只有 64 位，作为身份锚太短。
  - 刻意不用 `sha256//base64` 写法，免得和 TLS 钉住值填反。
- 校验流程：
  1. CLI 启动时用 `ParsePanelKey` 严格解析：
     - 前缀不对、长度不对、大写十六进制，都直接报错，不发任何请求。
  2. 接入 begin 的响应里拿到 `config_public_key`：
     - 标准 base64 解出 32 字节，用 `CheckPanelKey` 做常量时间比较；
     - 同时核对 `KeyID(config_public_key) == config_key_id`。
  3. 不一致就 abort 接入，不写身份、不写绑定文件，退出码非 0，报错写明「面板公钥指纹与安装命令不一致」。
  4. 通过后，配置公钥存进 `identity.json`。以后清单、名单、生效发布、升级指令都用它验签。
- 换钥：
  - 沿用现有的 `config_key_transition` 思路：旧钥签署新钥，链式钉住；
  - 现有原像 `pandora-config-signing-key-transition-v1` 里写的是 `node_id`，服务器级需要一个以 `server_id` 为主体的版本（§18）；
  - 换钥成功后 pdnd 更新 `identity.json`；
  - 绑定文件里的 `panel_key` 保留初始值，只用于 §12 的判定。

金样本：`keys.panel_key_pin`、`keys.gateway_tls_pin`（由测试网关密钥算出）。

---

## 12. 「同一面板同一租户只绑一台服务器」的判定键

### 12.1 面板来源规范化：`CanonicalPanelOrigin`

把 `--panel` 规范成 `scheme://host:port`：

- 只允许 `https`。`http` 只允许回环地址（`localhost`、`127.0.0.0/8`、`::1`）。
- 不允许用户信息、查询串、片段，路径只能为空或 `/`。
- IP 地址：
  - 用 `netip` 的规范写法，IPv6 小写、压缩，并加方括号；
  - IPv4 映射的 IPv6 地址还原成 IPv4；
  - 不允许 zone。
- 域名转小写，不允许末尾的点。
- 端口一律写出，缺省时 https 取 443、http 取 80。

| 输入 | 规范结果 |
|---|---|
| `https://panel.example.com` | `https://panel.example.com:443` |
| `HTTPS://Panel.Example.COM:8443/` | `https://panel.example.com:8443` |
| `https://[2001:DB8::1]:8443` | `https://[2001:db8::1]:8443` |
| `http://panel.example.com` | 拒绝（非回环的 http） |

完整列表见金样本 `panel_origins`。

### 12.2 判定键

一个绑定的判定三元组是（规范来源，`panel_key`，`tenant_id`）。两个绑定满足下面两条，即「同一面板同一租户」：

- 租户相同；
- 并且规范来源相同，**或者** `panel_key` 相同。

`panel_key` 相同也算，是为了堵住同一个面板换个访问地址（IP 与域名、换端口）重复绑定的情况。

面板实现：`bindingcontract.SamePanelTenant`。金样本：`same_panel_tenant`。

### 12.3 执行点

- 判定在 pdnd 侧执行（§3.1 第 2 步）：
  - 面板无法可靠识别「同一台机器」：每个绑定的密钥相互独立，IP 也可能经过 NAT；
  - 所以这条规则由机器侧兜住。
- 面板侧照常保证：
  - 一台服务器同时只有一个 active 服务器身份；
  - 同一服务器下按 `(server_id, 端口, L4)` 回 409。
- 判定命中、且 `server_id` 相同，是幂等重放：不改任何东西，只回报状态。
- 判定命中、但 `server_id` 不同，拒绝接入。

---

## 13. 面板触发升级

**原则**：

- 面板只能下令「升级到官方版本 X」，不能下发任何程序字节。
- 程序是不是官方的，只由**内置在 pdnd 里的发布签名公钥**判定。
- 只升不降，失败自动回滚。
- 多个面板都可以下令，但都只能选官方版本。多个指令同时有效时，取版本最高的那个。

### 13.1 官方发布清单 `pandora-release-manifest-v1`

线上文档（`release-manifest.json`）：

```json
{"contract":"pandora-release-manifest-v1","product":"pandora-native","version":"v1.6.0","commit":"<40 位小写十六进制>","released_at":"2026-10-01T00:00:00Z","release_key_id":"<发布公钥 key id>","artifacts":[{"name":"pandora-native-linux-amd64","size":23068672,"sha256":"<64 位小写十六进制>"},{"name":"pandora-native-linux-arm64","size":21495808,"sha256":"<…>"}],"signature":"<base64>"}
```

原像：

```
pandora-release-manifest-v1
product=pandora-native
version=<vX.Y.Z>
commit=<40 位小写十六进制>
released_at=<时间>
release_key_id=<key id>
artifact_count=<产物数>
artifact name=<名称> size=<字节数> sha256=<64 位小写十六进制>
…（每个产物一行，按 name 严格升序）
```

| 字段 | 规则 |
|---|---|
| `product` | 只能是 `pandora-native` |
| `version` | 规范的 `vX.Y.Z`。git describe 产出的 `v1.6.0-3-gabcdef0`、`dev` 一律不是官方版本 |
| `artifacts` | 1–32 个；`name` 匹配 `[a-z0-9][a-z0-9.-]{0,63}`，且不含 `..`；`size` > 0 |

- 发布清单**不过期**：同一版本永远是同一份字节，旧版本靠「只升不降」挡住。
- 签名密钥是离线保管的 Ed25519 发布密钥（§16），不是任何面板的密钥。

金样本：`release_manifests`。合法向量带完整的线上文档 `document`。

### 13.2 升级指令 `aegis-agent-upgrade-order-v1`

- 面板把指令放进清单响应的 `agent_upgrade` 字段，每次清单响应重新签一次。
- 清单签名不覆盖这个字段：指令自己单独签名，中间人删掉它最多是不升级。

```jsonc
{
  "contract": "aegis-agent-upgrade-order-v1",
  "tenant_id": "<uuid>", "server_id": "<uuid>", "serial": 1,
  "order_id": "<uuid>", "target_version": "v1.6.0",
  "release_manifest_sha256": "<base64(sha256(release-manifest.json 原始字节))>",
  "key_id": "<配置签名 key id>", "issued_at": "…", "expires_at": "…",
  "signature": "<base64>"
}
```

原像：

```
aegis-agent-upgrade-order-v1
tenant_id=<uuid>
server_id=<uuid>
serial=<serial>
order_id=<uuid>
target_version=<vX.Y.Z>
release_manifest_sha256=<base64>
request_nonce=<携带本指令的那次清单请求的 X-Server-Nonce>
key_id=<配置签名 key id>
issued_at=<时间>
expires_at=<时间>
```

金样本：`upgrade_orders`。合法向量的 `release_manifest_sha256` 等于金样本里合法发布清单 `document` 的哈希。

### 13.3 取发布物的接口

两个接口都按 §2 签名：

- `GET /v1/servers/agent-releases/{version}/manifest`：返回 `release-manifest.json` 的原始字节，面板不得重新编码。
- `GET /v1/servers/agent-releases/{version}/artifacts/{name}`：返回程序字节。

面板只提供它手里有签名发布清单的版本；首版就是面板发布包自带的那一版。

### 13.4 pdnd 的执行流程

1. **验指令**：
   - 用本绑定钉住的配置公钥验签；
   - `tenant_id`、`server_id`、`serial`、`request_nonce` 与本绑定、本次请求一致，现在落在有效窗口内；
   - 不满足就报 `upgrade_order_invalid`。同一个 `order_id` 只报一次。
2. **只升不降**：
   - 当前版本必须是规范版本：`dev` 等非官方构建一律不接受面板升级；
   - `CompareAgentVersions(target, 当前) > 0`，否则报 `upgrade_downgrade_refused`。
3. **挑指令**：多个绑定同时下令时，取 `target_version` 最高的那个执行。同一时刻只进行一次升级。
4. **取清单**：
   - 下载发布清单，`sha256(原始字节) == release_manifest_sha256`；
   - 严格解码；
   - `release_key_id` 必须在**内置发布公钥表**里，用对应公钥对原像验签；
   - `product`、`version` 与指令一致。
   - 任一项不满足，报 `upgrade_signature_invalid` 或 `upgrade_release_unavailable`。
   - 内置表为空时一律失败（fail closed）。
5. **取程序**：
   - 下载 `pandora-native-linux-<GOARCH>` 到 `<state_dir>/upgrade/<version>/`，读取上限等于清单里的 `size`；
   - sha256 必须相符，否则报 `upgrade_hash_mismatch`，并删掉文件。
6. **替换**：
   - 服务进程在 `ProtectSystem=strict` 下写不了 `/usr/local/bin`，所以替换由 root 的一次性助手完成（P5 实现）：
     1. pdnd 写出请求文件；
     2. path 单元触发 `pandora-native-upgrade.service`，它执行**当前已安装的**二进制的 `upgrade-apply` 子命令；
     3. 助手**重新做一遍第 2、4、5 步**：不信任服务进程的结论；
     4. 把旧程序改名为 `.previous`，用 rename 原子换上新程序，重启服务。
   - 替换失败报 `upgrade_install_failed`，原程序保持不动。
7. **自检与回滚**：
   - 新进程满足两项之一即视为健康，写健康标记：
     - 全部绑定的首次清单拉取都成功；
     - 或者连续正常运行 60 秒。
   - 助手最多等 120 秒。等不到就换回 `.previous`、重启，报 `upgrade_rolled_back`。
8. **上报**：每个阶段都在 S5 的 `agent_upgrade` 里报状态；成功之后，下一次 S5 带新的 `agent_version` 和 `capabilities`。

### 13.5 `min_agent_version`

- 清单里的 `min_agent_version` 只是提示：
  - 低于它时，后台显示「需要升级」；
  - 新协议的节点在创建时，被能力表校验拦下。
- pdnd 不会因为低于这个版本就自行升级，升级只由 §13.2 的签名指令触发。

### 13.6 对接入证据的影响

- 现有节点接入在 commit 时，要求 `binary_sha256` 等于面板随包 `release-artifact.env` 里的摘要。
- 面板触发升级之后，机器上的程序可能比面板随包的版本新。
- 所以 P1 的服务器接入（以及以后的节点接入）把口径放宽为：摘要出现在面板持有的任一**官方签名发布清单**里，并且版本不低于随包版本。生产环境下缺清单时仍然 fail closed。

---

## 14. 旧节点自动升级 `POST /v1/nodes/upgrade-to-server`

### 14.1 触发条件

新版 pdnd 启动时，同时满足下面三条才发起：

- legacy 节点（`config.json` 里的 `nodes[]`）都属于同一个面板；
- 这些节点都有签名身份；
- 面板的心跳回包里 `features` 含 `server-binding-v1`。

满足时，用其中任意一个**节点身份**按现有 `PANDORA-NODE-REQUEST-V2` 签名调用本接口。旧面板回 404 时保持 legacy，不重试到下一个发布周期。

### 14.2 请求

```jsonc
{
  "contract": "aegis-node-upgrade-to-server-v1",
  "server_public_key": "<新服务器身份 Ed25519 公钥，标准 base64>",
  "enc_kem": "dhkem-x25519-hkdf-sha256",
  "server_enc_public_key": "<新 X25519 公钥，标准 base64>",
  "possession_signature": "<新服务器私钥对 §14.3 原像的签名，标准 base64>",
  "legacy_node_ids": ["<uuid>", "…"],      // 这台机器上属于该面板的全部 legacy 节点，含签名者自己
  "agent_version": "v1.6.0",
  "features": ["…"],
  "capabilities": { … }
}
```

### 14.3 持有证明 `aegis-server-key-possession-v1`

外层签名证明「我是这个节点」，持有证明证明「新公钥的私钥确实在我手里」。两层绑定同一个 nonce，不能拆开重组。

```
aegis-server-key-possession-v1
purpose=upgrade-to-server
tenant_id=<uuid>
node_id=<发起升级的节点 ID，等于 X-Node-Id>
request_nonce=<外层请求的 X-Node-Nonce>
server_public_key=<标准 base64>
enc_kem=dhkem-x25519-hkdf-sha256
server_enc_public_key=<标准 base64>
```

金样本：`key_possessions`。

### 14.4 面板的判定

全部满足才签发：

1. 外层签名验过，节点身份 active。
2. 用 `server_public_key` 验持有证明。
3. 签名节点和 `legacy_node_ids` 里的每个节点都属于同一台服务器（`nodes.server_id` 相同且非空），并且都有 active 的节点身份。
4. 这台服务器还没有 active 的服务器身份。或者已有的那个身份公钥恰好等于 `server_public_key`：这是重放，按幂等处理，返回同样的结果。

### 14.5 响应

200：

```jsonc
{
  "tenant_id": "<uuid>", "server_id": "<uuid>", "serial": 1, "state": "bound",
  "config_key_id": "<key id>", "config_public_key": "<标准 base64>",
  "manifest_generation": 17,
  "adopted_node_ids": ["<uuid>", "…"]
}
```

| 状态 | 含义 | pdnd 的动作 |
|---|---|---|
| 200 | 签发成功 | 核对 `config_public_key` 与节点身份里已钉住的配置公钥相同（不同就放弃并告警）；写 `bindings/<id>/` 与绑定文件；按设计稿 §6.3 不断连交接；交接完成后从 `config.json` 原子移除 legacy 条目 |
| 401 | 节点身份校验失败 | 保持 legacy |
| 404 | 面板不支持 | 保持 legacy |
| 409 | 服务器已被另一把服务器公钥绑定，或节点没有挂在服务器上 | 保持 legacy，告警，等管理员在面板上处理 |
| 422 | `legacy_node_ids` 跨了多台服务器，或请求体不合法 | 保持 legacy，告警 |

- 服务器级绑定连续健康 10 分钟后，面板吊销这些节点的身份和运行令牌。

---

## 15. 兼容与协商（features）

- 双方各自声明支持的特性：
  - pdnd 在 S1 begin、S5、upgrade-to-server 的 `features` 里声明；
  - 面板在签名清单的 `features` 里声明。
- 特性名匹配 `[a-z0-9][a-z0-9.-]{0,47}`，按版本命名，如 `users-delta-v1`。
- 规则：
  - 新增的请求字段、响应字段、枚举值，只有在对方声明了对应特性之后才发送；
  - 未声明时按本文 v1 的形状发送；
  - 双方解码都拒收未知字段，所以违反这条规则会立刻 400 或者验签失败，不会悄悄丢数据。
- 基线：
  - 实现本合约 v1 的面板，在节点心跳回包与清单里声明 `server-binding-v1`；
  - pdnd 只有看到它，才会发起 S1 或 upgrade-to-server。
- 旧面板上，`/v1/servers/*` 回 404。pdnd 把它当作「不支持」，不重试，也不清理任何东西。

---

## 16. 发布签名：现状与最小方案

### 16.1 现状（基点 a7b99b5 核查）

- `pdnd/release/build.sh`：
  - 只产出 `manifest.json`，内容是版本、commit、Go 版本和四个产物的 sha256；
  - **没有任何签名**。
  - `verify.sh` 只核对哈希与架构。
- `panel/deploy/build-release.sh`：
  - 产出 `SHA256SUMS`、`<包>.tar.gz.sha256`、`<包>.tar.gz.manifest.sha256`；
  - 把两个架构 pdnd 的摘要写进 `deploy/release-artifact.env`，供节点接入时比对；
  - **没有任何签名**，也没有密钥。
- 装机脚本（`pdnd_install.go` 生成的 `install.sh`）从面板的 `/pdnd/bin/<产物>` 下载程序，再从同一面板的 `/pdnd/sha256/<产物>` 取哈希核对。哈希和程序同源，只防传输损坏，不防面板或链路被冒充。
- 仓库 workflow 里也没有签名步骤。

### 16.2 最小方案（P5 落地，本阶段只写方案，不生成任何密钥）

1. **发布密钥**：
   - 由维护者在离线环境生成一把 Ed25519 发布密钥（例如 `openssl genpkey -algorithm ed25519`）；
   - 私钥离线保管，例如密码管理器里的文档项，用时临时取到本机内存盘，签完即删；
   - 不进仓库、不进 CI、不上任何服务器。
2. **公钥内置**：
   - 发布公钥原始 32 字节的标准 base64 和它的 key id，写进 pdnd 源码的内置发布公钥表。公钥公开不涉密，可以进仓库；
   - 表为空时，所有升级指令都 fail closed。
3. **build 只产出待签清单**：
   - `build.sh` 额外产出 `release-manifest.unsigned.json`（§13.1 不含 `signature` 的部分）和 `release-manifest.preimage`；
   - 原像字节必须由 Go 代码（`bindingcontract.ReleaseManifestPreimage`）生成，例如 `pandora-native release-preimage` 子命令，不用 shell 拼接。
   - CI 照常跑，不接触私钥。
4. **维护者本机签名**：
   - 用 `openssl pkeyutl -sign -rawin -inkey <私钥> -in release-manifest.preimage | base64`，或者一个只依赖标准库的小工具，对原像签名；
   - 再用同一个 Go 子命令把签名合成最终的 `release-manifest.json`，并立即用内置公钥自验一次。
5. **随包分发**：
   - `build-release.sh` 把签名后的 `release-manifest.json` 一起打进面板发布包；
   - 面板经 §13.3 提供，装机脚本也改为核对它。
   - 没有签名清单的包仍然能装面板，但不能用于面板触发升级。
6. **轮换与吊销**：
   - 新密钥先随某个版本进入内置表（新旧并存），之后的发布改用新密钥签名，再过一个版本移除旧公钥；
   - 私钥泄露时，立即发一个只信任新公钥、版本号更高的修复版；
   - 「只升不降」保证机器不会被退回到旧版本。

---

## 17. 金样本

- 文件：
  - `panel/internal/platform/bindingcontract/testdata/bindingcontract-v1-golden.json`
  - `pdnd/bindingcontract/testdata/bindingcontract-v1-golden.json`
  - 两份逐字节相同。
- 内容：
  - 六个签名原像各有合法和非法向量。合法向量带输入字段、`preimage_hex`（原像字节的十六进制）和 `signature`（Ed25519，标准 base64）；非法向量要求原像函数报错。
  - 外加面板来源规范化、版本比较、同一面板判定，以及全部枚举。
- **全部是测试密钥**：
  - 每把密钥都由 `keys.*_label` 里写明的标签算 `sha256(标签)` 派生：Ed25519 用作种子，X25519 用作私钥，P-256 用作标量；
  - 文件里只有公钥与派生标签；
  - 标签里都带 `TEST ONLY`，绝不能用于生产。
- 测试：
  - 两端的 `TestGoldenSignedVectors` 都从金样本读输入，用本端实现重算原像，逐字节比对；再用派生公钥验签，用派生私钥重签，比对签名。Ed25519 是确定性签名。
  - 两端都有 `TestGoldenIdenticalIn…`，比对两份 testdata 逐字节相同，写法沿用 `panel/internal/api/public/pdnd_install_test.go` 的跨 module 文件契约测试。
  - 面板的 `TestGoldenFileMatchesDefinitions` 要求磁盘上的文件与定义重新生成的结果一致。
- 重新生成：在 `panel/` 下执行

  ```sh
  go test ./internal/platform/bindingcontract -run TestGoldenFileMatchesDefinitions -bindingcontract.update
  ```

  这条命令同时写两份文件。改合约时两端代码与两份金样本必须在同一个提交里一起改。

---

## 18. 留给后续阶段

| 事项 | 阶段 |
|---|---|
| 服务器级配置签名换钥原像：现有 `pandora-config-signing-key-transition-v1` 的主体是 `node_id`，需要以 `server_id` 为主体的版本，接口 `GET /v1/servers/config-signing-key` | P2 |
| `server_identities`、`server_enrollments`、`bootstrap_tokens.kind`、`servers.binding_state` / `manifest_generation` 迁移，及 S1 的面板实现 | P1 |
| S2–S7 的面板实现、按 server 隔离的 nonce 键空间、`/v1/servers/` 单独的 nginx 限速区（按 `$binary_remote_addr$http_x_server_id` 计数） | P2 |
| pdnd 的 Supervisor、BindingLoop、端口登记表的 scope 改为绑定 ID、同池共享 UserSet | P3 |
| 网关自签证书与 SPKI 钉住、`validateSignedServer` 与装机脚本放开 https 自签的条件 | P4 |
| `bind` / `unbind` / `upgrade-apply` CLI、root 升级助手与 path 单元、`bindings.d` 是否加进 `ReadWritePaths`、发布签名流程（§16.2） | P5 |
| upgrade-to-server 面板实现、不断连交接 `RebindInbound` | P6 |
| 增量名单特性 `users-delta-v1` | P2 之后按需 |
