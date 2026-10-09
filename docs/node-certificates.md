# 节点入站证书

pdnd 的 TLS 入站（trojan、anytls、naive、hysteria2、tuic、juicity，以及开了 TLS 的 vless / vmess）都需要一张服务端证书。证书有两种来源：

| 来源 | 适合谁 | 现状 |
|---|---|---|
| **本机证书文件（file 模式）** | 已经在节点上用 certbot / acme.sh 签证书的人 | 现在就能用；换证书后要重启 pdnd，热更新在后续版本接上 |
| **面板托管证书** | 想让面板统一签发、续期、下发的人 | 面板侧签发与续期已上线（后台「网络 › 证书」）；下发到节点在后续版本，见第 3 节 |

文中的域名一律用 `example.com` 占位，换成你自己的。

> **哪些是将来时**：本文凡是写「后续版本」「规划」的部分都还没上线。目前已经落地的是：pdnd 进程内的证书仓库（`pdnd/certstore`）、证书包的签名与加密契约（`pdnd/certbundle`、`panel/internal/platform/certbundle`），以及面板集中签发（第 3.5–3.8 节：DNS 凭据、ACME 设置、签发 worker、后台页面）。签出的证书暂时只存在面板里，还没有下发到节点，对运行中的节点没有影响。

---

## 1. file 模式：把证书放进 `/etc/pandora-native/certs/`

### 1.1 目录与权限约定

pdnd 以 `pandora` 用户运行，systemd 单元开着 `ProtectSystem=strict`，`/etc` 对它只读。证书文件只要它读得到即可：

| 对象 | 属主 | 权限 |
|---|---|---|
| `/etc/pandora-native/certs/` 及其子目录 | `root:pandora` | `0750` |
| 证书链、私钥文件 | `root:pandora` | `0640` |

建议每个域名一个子目录：

```
/etc/pandora-native/certs/
└── example.com/
    ├── fullchain.pem   # 叶子证书在前，后面跟中间证书
    └── privkey.pem
```

后台节点表单里的 `cert_path` / `key_path` 填这两个文件的绝对路径，例如 `/etc/pandora-native/certs/example.com/fullchain.pem`。

初始化目录（只需一次）：

```sh
sudo install -d -o root -g pandora -m 0750 /etc/pandora-native/certs
```

### 1.2 路径限制

- **现在**：pdnd 按节点配置里的路径直接读文件，还不检查目录。请从现在起就按 1.1 的约定放，后续版本收紧时不用再搬。
- **后续版本（将来时）**：pdnd 只接受这个目录下的证书文件，面板保存节点时也会做同样的检查。
  - 路径必须是绝对路径，按字面规范化后要在 `/etc/pandora-native/certs/` 下（`../` 逃不出去）。
  - 再把符号链接全部解析一遍，真实文件也必须在这个目录下。目录内的符号链接（例如 certbot 风格的 `live/` → `archive/`）可以用，指到目录外的会被拒绝。
  - 路径里不能有 `|`。
  - 证书或私钥文件超过 1 MiB 直接拒收。

### 1.3 pdnd 怎么加载（含后续版本的热更新）

- **现在**：pdnd 只在建立入站时读一次证书文件。证书换了，要重启才会用上新证书：

  ```sh
  sudo systemctl restart pandora-native
  ```

  单元没有定义 `ExecReload`，`systemctl reload` 不可用，请用 `restart`。重启会断开当前连接。

- **后续版本（第 1a 阶段，将来时）**：入站改用证书仓库的 `GetCertificate`。
  - pdnd 每 60 秒检查一次两个文件的 mtime 和大小，变了就重读，新握手立即用新证书，已建立的连接不断，不用重启。
  - 新文件读不出来、PEM 坏了、私钥和证书不配对，或者新证书已过期，都保留旧证书继续服务，并把错误码（`read_failed`、`bad_pem`、`key_mismatch`、`expired`、`path_escape`）随心跳报给面板。
  - 心跳同时上报证书的到期时间（NotAfter），后台据此标颜色、挂横幅。

  上线后，下面 deploy-hook 里的重启那一行删掉即可。

### 1.4 certbot deploy-hook

certbot 每次成功续期后，会执行 `/etc/letsencrypt/renewal-hooks/deploy/` 下的脚本，并把证书目录放在环境变量 `RENEWED_LINEAGE` 里。新建 `/etc/letsencrypt/renewal-hooks/deploy/pandora-native.sh`：

```sh
#!/bin/sh
# certbot 续期成功后，把证书复制到 pandora-native 的证书目录。
set -eu

DOMAIN=example.com                        # 换成证书的主域名（即 /etc/letsencrypt/live/ 下的目录名）
DEST=/etc/pandora-native/certs/$DOMAIN

# 只处理这一张证书；同一台机器上的其他证书不动。
[ "$(basename "$RENEWED_LINEAGE")" = "$DOMAIN" ] || exit 0

install -d -o root -g pandora -m 0750 "$DEST"
# 先写临时文件再 rename：pdnd 任何时刻都不会读到写了一半的文件。
for f in fullchain.pem privkey.pem; do
  install -o root -g pandora -m 0640 "$RENEWED_LINEAGE/$f" "$DEST/.$f.tmp"
  mv -f "$DEST/.$f.tmp" "$DEST/$f"
done

# 现在：重启 pdnd 才会用上新证书。热更新上线后删掉这一行。
systemctl restart pandora-native
```

```sh
sudo chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/pandora-native.sh
# 第一次手动跑一遍（模拟续期后的环境）
sudo RENEWED_LINEAGE=/etc/letsencrypt/live/example.com /etc/letsencrypt/renewal-hooks/deploy/pandora-native.sh
```

说明：

- `install` 先复制、再设属主和权限，`mv` 在同一目录里是原子替换。
- 两个文件不是同时替换的。热更新上线后，即使 pdnd 恰好在两次 `mv` 之间检查到「新证书 + 旧私钥」，也只会判为 `key_mismatch` 并保留旧证书，下一轮看到私钥也变了就会换上新的。

### 1.5 acme.sh

acme.sh 用 `--install-cert` 把证书安装到固定位置，并在每次续期后执行 `--reloadcmd`：

```sh
sudo install -d -o root -g pandora -m 0750 /etc/pandora-native/certs/example.com

acme.sh --install-cert -d example.com --ecc \
  --fullchain-file /etc/pandora-native/certs/example.com/fullchain.pem \
  --key-file       /etc/pandora-native/certs/example.com/privkey.pem \
  --reloadcmd "chown root:pandora /etc/pandora-native/certs/example.com/fullchain.pem /etc/pandora-native/certs/example.com/privkey.pem && chmod 0640 /etc/pandora-native/certs/example.com/fullchain.pem /etc/pandora-native/certs/example.com/privkey.pem && systemctl restart pandora-native"
```

说明：

- acme.sh 要以 root 运行（或者运行它的用户对上述目录有写权限、能执行 `chown` 和 `systemctl`）。
- 用 RSA 证书时去掉 `--ecc`。
- `--reloadcmd` 会被 acme.sh 记住，续期时自动执行；热更新上线后，把其中的 `&& systemctl restart pandora-native` 去掉，再执行一次 `--install-cert` 更新记录。

### 1.6 自查

```sh
sudo -u pandora head -c 64 /etc/pandora-native/certs/example.com/privkey.pem >/dev/null && echo 私钥可读
openssl x509 -in /etc/pandora-native/certs/example.com/fullchain.pem -noout -subject -enddate
```

---

## 2. 证书仓库的行为（已落地，尚未接线）

这一节说明 `pdnd/certstore` 的约定，供后续接线和排障参考。

- 每张证书按「命名空间 + ID」登记：
  - 托管证书：命名空间是证书包里签过名的 `server_id`，ID 是证书 ID。不同面板的 `server_id` 各不相同，面板 A 的配置引用不到面板 B 的证书。
  - file 模式：命名空间是 `file`，ID 是 `sha256(cert_path|key_path)`。
- 每次 TLS 握手都现读一次原子指针。客户端不发 SNI（用 IP 直连）时，也返回这一张证书。
- 托管证书落盘在 systemd 的 `StateDirectory` 里，`ProtectSystem=strict` 下可写，不用改单元：

  ```
  /var/lib/pandora-native/panels/<server_id>/certs/<cert_id>/
  ├── current.json           # 指向当前版本，原子 rename 更新
  ├── v3/
  │   ├── fullchain.pem      # 0600
  │   ├── privkey.pem        # 0600，本地明文，和 certbot 一样
  │   └── meta.json          # 0600，只有指纹、到期时间等公开信息
  └── retired_at             # 证书不在最新证书包里时才有，满 7 天整目录删除
  ```

  - 目录一律 `0700`。新版本先写进临时目录，逐个文件 fsync，再 rename 成 `v<版本>`，fsync 父目录，最后原子替换 `current.json`。
  - pdnd 启动时按 `current.json` 加载全部托管证书，面板不可达也能起来。
  - 版本号只能前进；同一版本号内容不同会被拒收。
  - 写盘失败（例如磁盘满）时，内存里照样换上新证书，状态记 `persist_failed`；重启后会回到盘上的旧版本。

---

## 3. 面板托管证书

面板侧的签发、续期、到期提示已经上线（3.5–3.8）；证书怎么到节点上（3.4）还在后续版本。

### 3.1 签发方式

- **面板集中签发，DNS-01**：支持 Cloudflare、阿里云 DNS、腾讯云 DNSPod 三家。面板用 ACME 客户端（lego）走服务商的 DNS API 写 `_acme-challenge` TXT 记录来证明域名控制权，验证完就删。
  - 不需要节点开放 80 端口，橙云、灰云、UDP 协议（hysteria2、tuic、juicity）都能用。
  - DNS 凭据只保存在面板上，加密存储，读接口只回末四位，永不下发到节点。
  - 不签 IP 证书，不做 HTTP-01 / TLS-ALPN-01（用户 2026-10-08 定）。
- **默认一张通配符证书，多台服务器共用**（例如 `*.example.com` 加 `example.com`）。
  - 好处：签发次数少，不容易撞 Let's Encrypt 的频率限制；新加节点不用再签。
  - 代价：同一把私钥分发到多台机器。服务器退役时后台会提示「换私钥重签」（rekey）。
  - 也可以选「每台服务器一张主机名证书」，隔离更好，证书更多。
- 多个面板为同一个注册域名签证书时，共用 Let's Encrypt 的频率额度（同一注册域名每 7 天 50 张，完全相同的域名组合每 7 天 5 张），而面板之间互不知情。
- 到期和失败只在后台显示横幅，不发 Telegram、不发邮件。

### 3.2 DNS 凭据：最小权限

#### Cloudflare API 令牌

在 Cloudflare 控制台「我的个人资料 → API 令牌 → 创建令牌 → 创建自定义令牌」：

| 项 | 取值 |
|---|---|
| 权限 | `Zone · DNS · Edit`、`Zone · Zone · Read`，只要这两项 |
| 区域资源 | `Include · Specific zone · example.com`，只选签证书要用的那一个 zone |
| 客户端 IP 地址筛选（可选） | 只允许面板服务器的出口 IP |
| TTL（可选） | 设一个到期时间，到期前在面板里换新令牌 |

不要用 Global API Key，也不要选「All zones」。

#### 阿里云 DNS（AccessKey）

- 在 RAM 控制台建一个只用于签证书的子用户，只开 OpenAPI 调用访问；不要用主账号的 AccessKey。
- 授权自定义策略，只给 `alidns:DescribeDomains`、`alidns:DescribeDomainRecords`、`alidns:AddDomainRecord`、`alidns:DeleteDomainRecord`，资源限定为 `acs:alidns:*:*:domain/example.com`。图省事可以给系统策略 `AliyunDNSFullAccess`，但范围是整个账号的云解析。

#### 腾讯云 DNSPod（SecretId / SecretKey）

- 在访问管理（CAM）建一个只用于签证书的子用户，只开编程访问；不要用主账号的 API 密钥。
- 授权自定义策略，只给 `dnspod:DescribeDomainList`、`dnspod:DescribeRecordList`、`dnspod:CreateRecord`、`dnspod:DeleteRecord`，资源限定为这个域名。图省事可以给 `QcloudDNSPodFullAccess`，但范围是整个账号的 DNSPod。

#### 面板怎么校验

保存凭据时面板当场校验一次（之后可以在后台随时「重新校验」）：

1. 调服务商的「列出域名」接口：凭据被拒（鉴权失败）或看不到填写的域名，判为失败；
2. 在这个域名下建一条 `_pandora-check` TXT 记录再立刻删掉，证明能写；
3. 数凭据一共看得到几个域名，多于一个时提示「不是最小权限」。

三家的 API 都查不到凭据的完整权限，面板只能做到这一步。校验被服务商明确拒绝时，用这个凭据的证书停止自动签发（状态「DNS 凭据失效」）；改好凭据、校验通过后自动恢复。

### 3.3 用 CNAME 把 `_acme-challenge` 委派出去（推荐）

如果不想让令牌能改主域名的 DNS，可以把验证记录委派到一个专门用来验证的 zone：

1. 在 Cloudflare 上准备一个只用来做验证的 zone，例如 `example.net`。
2. 在主域名 `example.com` 的 DNS 里加一条 CNAME（这条只需手工加一次）：

   ```
   _acme-challenge.example.com.  CNAME  _acme-challenge.example.com.example.net.
   ```

   通配符证书 `*.example.com` 用的也是 `_acme-challenge.example.com`，同一条 CNAME 即可。
3. 凭据只授权 `example.net` 这个 zone（权限同 3.2），后台凭据的「域名」填 `example.net`。新建证书时域名不在凭据的 zone 下，表单会提示要先配好这条 CNAME。

签发时 ACME 客户端会跟随 CNAME，把 TXT 记录写到 `example.net` 里。这样即使令牌泄露，也改不了 `example.com` 的任何记录。

### 3.4 证书怎么到节点上

- 生效配置里只写证书 ID，续期时这个 ID 不变，所以续期不会重建入站、不会断连接。
- 证书和私钥走单独的「证书包」通道：
  - 证书包由面板的配置签名密钥签名，契约名 `pandora-node-certificate-bundle-v1`，有效窗口不超过 10 分钟。
  - 私钥用 HPKE（DHKEM(X25519) + HKDF-SHA256 + AES-256-GCM）加密给每台服务器上 pdnd 自己生成的 X25519 公钥，并绑定租户、服务器、证书 ID、版本和证书链哈希，挪到别的证书或版本上解不开。
  - 即使节点到面板是明文 HTTP，中间人也伪造不了、降级不了证书，最多只能延迟更新。
- 老版本 pdnd 不登记加密公钥，后台不允许给它绑定托管证书，只能继续用 file 模式。

### 3.5 后台入口

后台「网络 › 证书」，三个标签（读要 `node.certificate.read`，写要 `node.certificate.write`；迁移 00147 把它们分别授给已有 `node.read`、`node.provision` 的角色）：

- **证书列表**：名称、域名、状态（正常 / 排队中 / 签发中 / 签发失败 / 已暂停 / DNS 凭据失效）、到期等级与剩余时间、下次续期、最近错误。有即将到期、快到期、已过期或签发失败的证书时，页面顶部出现横幅（到期与失败只在后台显示，不发 Telegram、不发邮件）。点一行打开详情：版本历史（不显示私钥）、最近 50 次签发记录与错误详情；操作有「立即续期」「暂停 / 恢复」「编辑」「删除」。
  - 每次签发都生成新私钥，所以「立即续期」就是换私钥重签：服务器退役或怀疑失陷时用它。
  - 通配符证书在新建表单和详情里都提示私钥共享的风险。
- **DNS 凭据**：服务商、域名、密钥末四位、校验结论；添加与编辑时按服务商给出最小权限做法。凭据只写不读，编辑时密钥框留空表示不改。还有证书在用的凭据不能删。
- **ACME 设置**：联系邮箱（可留空）、是否用 Let's Encrypt 测试环境、备用 CA ZeroSSL（默认关，要填 EAB KID 与 HMAC；HMAC 只写不读）。保存即表示同意所用 CA 的服务条款。

录入或替换秘密（DNS 凭据、EAB）和删除证书、凭据要近期重认证。

### 3.6 签发与续期怎么跑

aegis-admin 里有一个「证书巡检」循环（每 30 秒一轮）：

1. **排队**：没签出过的证书、到了续期时间的证书（有 CA 的 ARI 建议窗口时取窗口内随机一点，否则剩余寿命 1/3 时），过了退避时间就排一张订单。同一张证书同时最多一张进行中的订单（数据库部分唯一索引）。
2. **认领**：worker 用租约认领一张订单（`FOR UPDATE SKIP LOCKED`，租约 5 分钟、每 30 秒续一次）。进程退出或卡死，租约一过别的实例或重启后的自己接手；落库前核对租约还是自己的，同一张订单不会写出两个版本。
3. **凭据预检**：先调服务商 API 确认凭据还能用、看得到域名。失败不碰 CA，不消耗 CA 的验证失败次数。
4. **本地限额对账**：按面板自己签过的版本数 Let's Encrypt 的两条限额——同一注册域名每 7 天 50 张新证书（续期不算）、完全相同的域名组合每 7 天 5 张。预计会超就不去请求 CA，退避到最早那张满 7 天；带 ARI `replaces` 的续期 Let's Encrypt 免一切限额，不拦。超限且证书剩不到 7 天（或从没签出过）、又配了 ZeroSSL 时，改走 ZeroSSL。
5. **签发**：lego 走 DNS-01，续期订单带上一版的 ARI certID（`replaces`）。私钥是 PKCS#8，用面板主密钥信封加密后入库，AAD 绑定租户、证书、版本。
6. **失败退避**：CA 限流（429）按 Retry-After 退避、不计入失败；凭据被拒停在「DNS 凭据失效」；服务商 API 不可达 15 分钟后再试；其余失败按 1h → 2h → 4h … 最长 24h 退避，连续失败 3 次暂停自动重试（避开 Let's Encrypt「每小时 5 次验证失败」的上限），在后台改好后点「恢复」。

每次签发成功、失败、暂停、到期等级上升，以及凭据、ACME 设置的增删改，都写审计（`certificate.*`、`dns_credential.*`、`acme_settings.updated`、`acme_account.created`）。

到期等级按证书寿命缩放：剩余少于 min(14 天, 寿命 × 25%) 为「即将到期」，少于 min(3 天, 寿命 × 10%) 为「快到期」，一律按面板时钟算。

### 3.7 数据与保留

- 证书版本表只插不改（运行角色没有 UPDATE 授权，另有触发器拒绝 UPDATE）；删证书时连同全部版本（含私钥密文）与订单一起删。
- 订单表就是签发历史；过期版本与旧订单的定期清理在后续版本。
- 迁移：00147（DNS 凭据、ACME 账号、权限码）、00148（证书、版本、订单）。00148 回滚时若已有签出的版本会拒绝，先在后台删掉证书。

### 3.8 开发与测试：ACME 目录覆盖

只在非生产环境（`AEGIS_ENV` 不是 `production`）可用，生产环境设了网关一律拒绝启动：

| 环境变量 | 作用 |
|---|---|
| `AEGIS_ACME_DIRECTORY_OVERRIDE` | 所有签发改打到这个 ACME 目录（如本地 pebble 的 `https://127.0.0.1:14000/dir`），版本记为 `custom` |
| `AEGIS_ACME_TRUSTED_ROOTS` | 信任这个目录 HTTPS 证书用的 PEM 文件 |

自动化测试不靠这两个变量：`go test ./internal/domain/certs` 在进程内起 pebble、DNS 服务器和三家服务商 API 的模拟，跑通签发、续期（ARI replaces）、CA 限流、凭据失效、本地限额与切换 ZeroSSL；需要数据库的部分在 PG18 门禁（`run-pg18-gates.sh` 的 certs 域）里跑。阿里云 SDK 只认环境里的 HTTPS 代理与系统根证书，它的模拟用例只在 Linux 上跑。
