# 节点入站证书

pdnd 的 TLS 入站（trojan、anytls、naive、hysteria2、tuic、juicity，开了 TLS 的 socks / http，以及 vless / vmess 的 xhttp-h3）都需要一张服务端证书。后台节点表单里「证书来源」二选一：

- **自动申请（acme，新建节点的默认）**：节点端自己向 Let's Encrypt 申请并自动续期，见第 1 节。
- **手填证书路径（file）**：证书由你自己用 certbot / acme.sh 等工具放到节点机上，表单里填路径，见第 2 节。2026-10-20 之前建的节点没有「证书来源」这一项，一律按手填处理，行为不变。

vless / vmess 的普通 TLS 还没开放；它们的 xhttp-h3 只支持手填。

面板不签发、不下发节点证书：证书在节点机上申请、存放，私钥不离开节点机，面板只收节点端心跳里报的状态。

文中的域名一律用 `example.com`、IP 用 `192.0.2.10` 占位，换成你自己的。

> **哪些是将来时**：本文凡是写「后续版本」的部分都是规划，还没有实现、也没有排期。
>
> 以后做 file 模式的路径检查与热更新（2.2、2.3 的「后续版本」）时，可以参考已删的 pdnd 证书仓库：`git show bd01b3b2^:pdnd/certstore/file.go`（按 mtime 与大小轮询、坏证书保留旧的、`read_failed` / `bad_pem` / `key_mismatch` / `expired` / `path_escape` 错误码都在里面，连同同目录的 `atomicfile.go` 与测试）。

---

## 1. 自动申请：节点端向 Let's Encrypt 申请

### 1.1 申请什么名字

证书的名字由面板自动推，表单里没有单独的字段：

- 填了「客户端 SNI」就用 SNI；
- 没填就用节点的连接地址。

名字可以是域名，也可以是公网 IP：

| 名字 | 证书 | 有效期 | 自动续期 |
|---|---|---|---|
| 域名（如 `node1.example.com`） | Let's Encrypt 的缺省证书 | 约 90 天 | 按 Let's Encrypt 的续期建议（ARI）；拿不到建议时在过了 2/3 有效期时续 |
| 公网 IP（如 `192.0.2.10`） | `shortlived` 档的 IP 证书 | 约 6 天 | 同上；拿不到建议时在有效期过半时续 |

保存节点时面板会拦下签不了的名字：内网、回环、链路本地、CGNAT 地址，单段名字（`node`），通配符（`*.example.com`），`.local`、`.internal`、`.lan` 这类内网后缀。

域名要先把 A / AAAA 记录解析到节点机；域名设了 CAA 记录的，要允许 `letsencrypt.org`。

订阅里客户端照常按这个名字校验证书：域名证书按 SNI 校验；IP 证书按连接的 IP 校验（证书里带 IP SAN）。自动申请的证书受客户端信任，所以「允许不安全证书」只在手填时出现，选了自动申请再打开会被拒绝。

### 1.2 前提：80 端口

验证走 HTTP-01：Let's Encrypt 从公网访问节点机的 80/TCP。

- 防火墙、云厂商安全组要放行入站 80/TCP。
- 节点机上不能有别的程序占着 80（nginx、caddy、宝塔等）。节点端只在申请、续期那几秒临时监听 80，用完就关。
- systemd 单元已经给了 `CAP_NET_BIND_SERVICE`，不用 root 也能监听 80；后台也不允许把节点开在 80 上，留给验证用。

### 1.3 申请和生效的过程

1. 保存节点、面板下发配置后，节点端立即排队申请。还没拿到证书时，节点**不起入站**：
   - 回执是「等待证书」（原因码 `cert_pending`）；
   - 后台列表显示中性色的「等待证书」，不算失败。
   - 如果节点原来在用旧配置服务（比如刚从手填改成自动申请，或者改了 SNI），旧配置继续服务，新配置等证书到了再换。
2. 证书一到，节点端立刻装上入站，不用等下一轮。
3. 续期是原地换证书：新握手用新证书，已建立的连接不断，不用重启。
4. 节点端重启后直接用落盘的证书，不重新申请。

证书和 ACME 账户存在节点机的 `/var/lib/pandora-native/acme/`（`cache_dir` 下的 `acme/`，权限 0700）：

- `account.json` 是账户；
- `certs/<名字>/` 下是证书。

不再用到的名字（改了 SNI 或删了节点）保留 7 天后删除。节点身份失效（服务器在后台被删除、退役或吊销）时，连同证书与账户一起删掉。

### 1.4 失败了会怎样

失败后节点端自己退避重试：

- 一般失败：从 15 分钟开始，每次翻倍，最长 4 小时一次（各加 ±10% 抖动）；
- 触发频率限制：按 Let's Encrypt 给的时间再试（至少 15 分钟）；
- 80 端口被占：每 5 分钟试一次，不消耗 Let's Encrypt 的额度。

后台在三处显示：

- 节点列表的徽标：「证书申请中」「证书申请失败」「续期失败，N 天内到期」「N 天内到期」「证书已过期」；
- 节点详情右侧的「证书」一行：中文原因、Let's Encrypt 的原话、下次重试时间；
- 仪表盘「要处理」的「证书异常」：申请失败或快到期的节点数。快到期的门槛是 IP 证书剩 2 天、域名与手填证书剩 14 天。

证书过期了（续期一直失败），节点**继续服务**，运行状态标「降级」（原因码 `cert_expired`）。这时客户端校验证书会失败。订阅里它排到后面，有别的可用节点时先摘掉，修好后立刻恢复。

排障步骤见 `panel/deploy/RUNBOOK.md`「节点证书申请失败」。

### 1.5 中转

父节点用自动申请的证书时：

- 中转没填 SNI：订阅里自动写上父节点证书的名字。客户端连中转机，按父节点的名字（或 IP）校验证书。
- 中转填了 SNI：必须等于父节点证书的名字，否则保存时报错。

### 1.6 Let's Encrypt 订户协议

节点端注册 ACME 账户时不留邮箱，注册即表示同意 [Let's Encrypt 订户协议](https://letsencrypt.org/repository/)。后台表单的说明里写明了「选自动申请即视为同意」，不另设勾选框。

### 1.7 节点端版本

自动申请要求节点端支持能力 `cert_acme_v1`（2026-10-20 之后的版本）。旧版节点端收到自动申请的配置会装失败。先在后台在线升级节点端，再把节点改成自动申请。

### 1.8 验收与测试用的设置

生产上 `config.json` 不写 `acme` 段。验收时可以指向测试 CA（pebble、Let's Encrypt staging）：

```json
{
  "acme": {
    "directory_url": "https://acme-staging-v02.api.letsencrypt.org/directory",
    "http_port": 80,
    "ca_file": ""
  }
}
```

- `directory_url`：ACME 目录地址，必须是 https；空的时候用 Let's Encrypt 生产环境。
- `http_port`：HTTP-01 在本机监听的端口，0 表示 80。CA 总是连公网 80，只有 80 前面另有转发时才改。
- `ca_file`：额外信任的 CA 证书（PEM），连 pebble 这类自签 CA 时用。

---

## 2. file 模式：把证书放进 `/etc/pandora-native/certs/`

### 2.1 目录与权限约定

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

### 2.2 路径限制

- **现在**：pdnd 按节点配置里的路径直接读文件，还不检查目录。请从现在起就按 2.1 的约定放，后续版本收紧时不用再搬。
- **后续版本（将来时）**：pdnd 只接受这个目录下的证书文件，面板保存节点时也会做同样的检查。
  - 路径必须是绝对路径，按字面规范化后要在 `/etc/pandora-native/certs/` 下（`../` 逃不出去）。
  - 再把符号链接全部解析一遍，真实文件也必须在这个目录下。目录内的符号链接（例如 certbot 风格的 `live/` → `archive/`）可以用，指到目录外的会被拒绝。
  - 路径里不能有 `|`。
  - 证书或私钥文件超过 1 MiB 直接拒收。

### 2.3 pdnd 怎么加载（含后续版本的热更新）

- **现在**：pdnd 只在建立入站时读一次证书文件。证书换了，要重启才会用上新证书：

  ```sh
  sudo systemctl restart pandora-native
  ```

  单元没有定义 `ExecReload`，`systemctl reload` 不可用，请用 `restart`。重启会断开当前连接。

- **后续版本（将来时）**：手填证书热更新。入站已经改成每次握手现取证书（`GetCertificate`，自动申请的续期就靠它），手填证书目前仍只在建立入站时读一次。
  - pdnd 每 60 秒检查一次两个文件的 mtime 和大小，变了就重读，新握手立即用新证书，已建立的连接不断，不用重启。
  - 新文件读不出来、PEM 坏了、私钥和证书不配对，或者新证书已过期，都保留旧证书继续服务，并把错误码（`read_failed`、`bad_pem`、`key_mismatch`、`expired`、`path_escape`）随心跳报给面板。

- **已经有的**：手填证书的到期时间（NotAfter）在建立入站时读出来，随心跳报给面板，后台列表与仪表盘按 1.4 的门槛提醒；在服务的证书过期了同样标「降级」（`cert_expired`）。

  上线后，下面 deploy-hook 里的重启那一行删掉即可。

### 2.4 certbot deploy-hook

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

### 2.5 acme.sh

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

### 2.6 自查

```sh
sudo -u pandora head -c 64 /etc/pandora-native/certs/example.com/privkey.pem >/dev/null && echo 私钥可读
openssl x509 -in /etc/pandora-native/certs/example.com/fullchain.pem -noout -subject -enddate
```
