# AegisPanel 备份与恢复手册

## 安全模型

数据库备份只以 PostgreSQL custom format 生成，并在管道中直接交给 `age`
加密。脚本不会把明文 dump 写到磁盘。备份主机只需持有 Age 公钥
recipient；解密 identity 应保存在独立的密钥系统或恢复主机上。

`deploy/.env`、`AEGIS_MASTER_KEY`、Age identity 和数据库备份不得打入同一个
包。如果攻击者同时取得密文和解密密钥，备份加密就失去作用。

## 初始化

1. 在离线或受控主机上生成 Age 密钥：`age-keygen -o backup-age.key`。
2. 只把输出的 `age1...` 公钥填入 `AEGIS_BACKUP_AGE_RECIPIENT`。
3. 通过 Vault/KMS/离线介质将 identity 供给恢复主机，权限设为 `0600`。
4. 创建备份目录：`install -d -m 0700 /var/backups/aegispanel`。
5. 确认安装 `age`，并用 `bash deploy/backup-postgres.sh` 完成首次手工备份。

`deploy/.env` 必须由 root 持有、只有一个硬链接，权限必须是 `0400` 或 `0600`；
它的全部父目录也必须由 root 持有且不可被 group/world 写入。备份、验证和恢复脚本
从已验证的文件描述符读取该文件，不接受可替换的符号链接配置。

若修改 `AEGIS_BACKUP_DIR`，必须同时修改
`aegis-backup.service` 的 `ReadWritePaths`，否则 systemd 会正确地拒绝写入。

## 备份产物与验证

每次备份产生：

- `aegis-postgres-YYYYmmddTHHMMSSZ.dump.age`
- 同名 `.sha256` 校验文件

备份过程会在内存管道/FIFO 中把同一份 custom dump 同时交给
`pg_restore --list` 和 `age`；只有结构校验、加密和 SHA256 全部成功后，
才会发布到最终文件名。单个文件的 rename 是原子的，但密文和校验文件
无法跨文件同时原子发布：脚本先发布校验文件、再发布密文，验证器缺少
任意一件都会拒绝。极端中断最多留下不可用的孤立校验文件。

恢复主机上的完整校验：

```bash
deploy/verify-backup.sh /var/backups/aegispanel/aegis-postgres-YYYYmmddTHHMMSSZ.dump.age
```

默认校验 SHA256、Age 解密和 TOC。若要在随机临时库中完整恢复演练：

```bash
AEGIS_VERIFY_RESTORE=1 deploy/verify-backup.sh /absolute/path/backup.dump.age
```

临时库使用唯一名称，并在结束时通过 `dropdb --force` 清理；不会复用或
删除现有业务库。

## 恢复防误操门

恢复必须同时指定密文文件、目标库和与目标库精确匹配的确认值：
恢复已存在的库前必须停止所有应用服务和并发写入。脚本会在确认门通过后
强制删除目标库，从 `template0` 重建空库，再恢复；不会将备份与原有对象混合。

```bash
export AEGIS_RESTORE_CONFIRM='RESTORE:aegis_recovery'
deploy/restore-postgres.sh \
  --archive /absolute/path/backup.dump.age \
  --target-db aegis_recovery
```

若目标已存在，还需要
`AEGIS_RESTORE_EXISTING_CONFIRM=OVERWRITE_EXISTING:<database>`。若目标正是 `.env` 配置的
`POSTGRES_DB`，另外需要
`AEGIS_RESTORE_PRODUCTION_CONFIRM=OVERWRITE_CONFIGURED_DATABASE:<database>`。

任何确认值不匹配时，脚本都会在连接目标库之前退出。

恢复配置中的生产数据库时，脚本要求 `aegis-public`、`aegis-admin`、`aegis-node`
和 `aegis-backup` 全部处于 `inactive/dead` 且没有 MainPID/ControlPID。恢复期间会用
runtime mask 阻止这些服务被重新启动，并用 PostgreSQL connection limit 阻止普通应用角色
重新连接。提交时会在连接闸门仍关闭的情况下仅撤销本次创建的 runtime mask、再次确认服务
完全停止，最后才开放数据库连接；任一步骤失败或收到信号都会重新 mask 四个服务并把连接限制
恢复为 0。失败状态必须先排查并完成恢复，再由运维人员人工解除。备份与恢复共用
`/run/aegispanel/database-maintenance.lock`，两者不能并发执行。

## 远程副本与失败通知

`AEGIS_BACKUP_REMOTE_HOOK` 和 `AEGIS_BACKUP_FAILURE_HOOK` 只接受可执行文件的
绝对路径。脚本不使用 `eval` 也不解析 shell 命令字符串。Hook 在清空后的
环境中运行，不会继承数据库口令、JWT 密钥或主密钥。远程 hook 应先上传
密文，再上传 SHA256 文件，并在远程使用不可变对象锁/版本保留。

### WebDAV 自动备份

发布包包含静态 Go 上传器 `aegis-backup-webdav`。它直接作为现有
`AEGIS_BACKUP_REMOTE_HOOK` 使用，不会让 Admin HTTP 进程获得 root、Docker 或
systemd 权限。上传器只接收本地已经完成 `pg_restore --list`、Age 加密与 SHA256
校验的 `.dump.age` 和 `.sha256` 文件。

安装配置：

```bash
install -d -o root -g root -m 0700 /etc/aegispanel
install -o root -g root -m 0600 \
  deploy/backup-webdav.example.json \
  /etc/aegispanel/backup-webdav.json
install -o root -g root -m 0600 /dev/null \
  /etc/aegispanel/backup-webdav.password
install -d -o root -g root -m 0700 /var/lib/aegispanel/backup-webdav
/opt/aegispanel/bin/aegis-backup-webdav init-signing-key \
  /etc/aegispanel/backup-manifest-ed25519.seed \
  > /root/aegispanel-backup-manifest.public
chmod 0600 /root/aegispanel-backup-manifest.public
```

最后一条命令只会在密钥不存在时创建独立 Ed25519 seed，并把公钥输出到重定向文件。
公钥和最新的 `latest.manifest.json` 检查点必须通过 WebDAV 之外的独立通道保存；检查点如果
和备份一起只放在同一 WebDAV 中，就不能识别服务端回放旧备份。不要把 seed、Age identity、
WebDAV 密码或检查点混为同一份密钥材料。

`checkpoint_replication_hook` 是必填的 root 专用绝对路径，Hook 只接收一个参数：
本次已原子提交的 `latest.manifest.json` 路径。它必须把该文件持久化到
与备份 WebDAV 不同的信任域（不同凭据的第二存储、KMS ledger、离线仓或
受保护的恢复主机），只有确认持久化成功后才返回 0。Hook 未配置、不安全或
返回非 0 时，整次自动备份会报错；重试会复用同一份签名 manifest，不重复增加序列。
Hook 必须安装在 root:root、0700 的固定目录 `/opt/aegispanel/checkpoint-sink`，
上传器会从已验证的文件 FD 执行，不会再按可替换路径打开。Hook 在独立存储中
原子写入并回读校验后，必须向 stdout 精确输出：

```text
AEPB-CHECKPOINT-RECEIPT-V1 <checkpoint-file-sha256>
```

多余输出、错误摘要、非零退出或回执超限都会使整次备份失败关闭。

使用 root 权限的编辑器填写两个文件；不要把 WebDAV 密码放进 shell 历史、URL、
`.env`、命令行参数或日志。`backup-webdav.json` 的 `endpoint` 只填写 HTTPS 站点，
目录放在 `base_path`。远端目录必须预先存在。默认拒绝 loopback、link-local、云
元数据地址和全部私网地址；只有确实使用内网 NAS 时才把
`allow_private_network` 设为 `true`，本机地址仍会被拒绝。

然后在 `deploy/.env` 设置：

```dotenv
AEGIS_BACKUP_REMOTE_HOOK=/opt/aegispanel/bin/aegis-backup-webdav
```

每个文件的远端发布流程为随机 `.partial.<run-id>` PUT、完整 GET 回读 SHA256、
同源 `MOVE` 到最终名、再次完整 GET 校验。按密文、`.sha256`、Ed25519 签名 manifest
的顺序发布；manifest 才是远端备份完成标记。同内容对象可安全重试，已存在但内容
不同的对象会被拒绝覆盖。重定向、HTTP 降级、跨源认证、覆盖已有最终对象、
符号链接、本地摘要不匹配或 WebDAV 不支持安全 MOVE 时都会失败关闭。远端失败
不会触发本地保留清理，便于修复配置后重试。

从 WebDAV 恢复时，把对应的 `.manifest.json` 与密文、checksum 放在同一受保护目录，
并在恢复主机设置：

```dotenv
AEGIS_BACKUP_MANIFEST_PUBLIC_KEY=/secure/off-webdav/aegispanel-backup-manifest.public
AEGIS_BACKUP_TRUSTED_CHECKPOINT=/secure/off-webdav/latest.manifest.json
```

`verify-backup.sh` 会先验证签名、制品绑定以及待恢复 manifest 与可信检查点完全一致，
再执行 SHA256、Age 解密和 `pg_restore` 检查。只有公钥而没有 WebDAV 之外的可信检查点时，
无法识别合法旧备份回放，必须拒绝恢复。
默认即为这个 fail-closed 模式。只有经人工审批的历史无签名备份才能在单次命令中
显式设置 `AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY=RESTORE_UNSIGNED:<archive-basename>`，
脚本会输出高风险警告；该值不得写入 `.env`，且 `restore-postgres.sh` 始终拒绝此降级模式。

## systemd

```bash
install -m 0644 deploy/systemd/aegis-backup.service /etc/systemd/system/
install -m 0644 deploy/systemd/aegis-backup.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now aegis-backup.timer
systemctl list-timers aegis-backup.timer
```

建议每月至少执行一次临时库恢复演练，并将结果记录在独立运维系统中。
