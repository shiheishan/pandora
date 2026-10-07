# 节点入站证书

pdnd 的 TLS 入站（trojan、anytls、naive、hysteria2、tuic、juicity，以及开了 TLS 的 vless / vmess）都需要一张服务端证书。证书有两种来源：

| 来源 | 适合谁 | 现状 |
|---|---|---|
| **本机证书文件（file 模式）** | 已经在节点上用 certbot / acme.sh 签证书的人 | 现在就能用；换证书后要重启 pdnd，热更新在后续版本接上 |
| **面板托管证书** | 想让面板统一签发、续期、下发的人 | 规划中，见第 3 节 |

文中的域名一律用 `example.com` 占位，换成你自己的。

> **哪些是将来时**：本文凡是写「后续版本」「规划」的部分都还没上线。目前已经落地的是 pdnd 进程内的证书仓库（`pdnd/certstore`）和证书包的签名、加密契约（`pdnd/certbundle`、`panel/internal/platform/certbundle`），它们还没有接进入站和面板，对运行中的节点没有影响。

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

## 3. 面板托管证书（规划，将来时）

下面是已经定下的方向，具体阶段排期以设计稿为准，上线前不可用。

### 3.1 签发方式

- **面板集中签发，只做 Cloudflare DNS-01**。面板用 ACME 客户端走 Cloudflare 的 DNS API 写 `_acme-challenge` TXT 记录来证明域名控制权。
  - 不需要节点开放 80 端口，橙云、灰云、UDP 协议（hysteria2、tuic、juicity）都能用。
  - Cloudflare API 令牌只保存在面板上，加密存储，永不下发到节点。
  - 后续还会加一条「面板编排的 HTTP-01 / TLS-ALPN-01」路径，给灰云且没有 DNS API 的域名、以及 IP 证书用，排在最后。
- **默认一张通配符证书，多台服务器共用**（例如 `*.example.com` 加 `example.com`）。
  - 好处：签发次数少，不容易撞 Let's Encrypt 的频率限制；新加节点不用再签。
  - 代价：同一把私钥分发到多台机器。服务器退役时后台会提示「换私钥重签」（rekey）。
  - 也可以选「每台服务器一张主机名证书」，隔离更好，证书更多。
- 多个面板为同一个注册域名签证书时，共用 Let's Encrypt 的频率额度（同一注册域名每 7 天 50 张，完全相同的域名组合每 7 天 5 张），而面板之间互不知情。
- 到期和失败只在后台显示横幅，不发 Telegram、不发邮件。

### 3.2 Cloudflare API 令牌：最小权限

在 Cloudflare 控制台「我的个人资料 → API 令牌 → 创建令牌 → 创建自定义令牌」：

| 项 | 取值 |
|---|---|
| 权限 | `Zone · DNS · Edit`、`Zone · Zone · Read`，只要这两项 |
| 区域资源 | `Include · Specific zone · example.com`，只选签证书要用的那一个 zone |
| 客户端 IP 地址筛选（可选） | 只允许面板服务器的出口 IP |
| TTL（可选） | 设一个到期时间，到期前在面板里换新令牌 |

不要用 Global API Key，也不要选「All zones」。面板保存令牌时会当场校验，并检查令牌能看到几个 zone：多于一个时提示「令牌范围不是最小权限」。Cloudflare 的 API 查不到令牌的完整权限，面板只能做到这一步。

### 3.3 用 CNAME 把 `_acme-challenge` 委派出去（推荐）

如果不想让令牌能改主域名的 DNS，可以把验证记录委派到一个专门用来验证的 zone：

1. 在 Cloudflare 上准备一个只用来做验证的 zone，例如 `example.net`。
2. 在主域名 `example.com` 的 DNS 里加一条 CNAME（这条只需手工加一次）：

   ```
   _acme-challenge.example.com.  CNAME  _acme-challenge.example.com.example.net.
   ```

   通配符证书 `*.example.com` 用的也是 `_acme-challenge.example.com`，同一条 CNAME 即可。
3. 令牌只授权 `example.net` 这个 zone（权限同 3.2）。

签发时 ACME 客户端会跟随 CNAME，把 TXT 记录写到 `example.net` 里。这样即使令牌泄露，也改不了 `example.com` 的任何记录。

### 3.4 证书怎么到节点上

- 生效配置里只写证书 ID，续期时这个 ID 不变，所以续期不会重建入站、不会断连接。
- 证书和私钥走单独的「证书包」通道：
  - 证书包由面板的配置签名密钥签名，契约名 `pandora-node-certificate-bundle-v1`，有效窗口不超过 10 分钟。
  - 私钥用 HPKE（DHKEM(X25519) + HKDF-SHA256 + AES-256-GCM）加密给每台服务器上 pdnd 自己生成的 X25519 公钥，并绑定租户、服务器、证书 ID、版本和证书链哈希，挪到别的证书或版本上解不开。
  - 即使节点到面板是明文 HTTP，中间人也伪造不了、降级不了证书，最多只能延迟更新。
- 老版本 pdnd 不登记加密公钥，后台不允许给它绑定托管证书，只能继续用 file 模式。
