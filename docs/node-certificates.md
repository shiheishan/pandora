# 节点入站证书

pdnd 的 TLS 入站（trojan、anytls、naive、hysteria2、tuic、juicity，以及开了 TLS 的 vless / vmess）都需要一张服务端证书。证书放在节点机上，由你自己用 certbot / acme.sh 等工具签发和续期（file 模式），后台节点表单里填证书与私钥的路径。

面板不签发、不下发节点证书：曾经上线过的「面板集中签发」（DNS-01）因为没有把证书送到节点的路径，已于 2026-10-14 下线（迁移 00048 删表）。以后要做，按节点 gRPC 会话下发重新设计。

文中的域名一律用 `example.com` 占位，换成你自己的。

> **哪些是将来时**：本文凡是写「后续版本」的部分都是规划，还没有实现、也没有排期。

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

- **后续版本（将来时）**：入站改为每次握手现取证书（`GetCertificate`）。
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
