---
paths:
  - "panel/deploy/**"
---

# 部署脚本与模板

- 这里是产品的一部分，任何人下载后照同一套脚本部署。脚本与模板里不出现具体部署的值：域名、后台前缀、主密钥都在安装时生成，落在主机的 `.env`
  - nginx 模板只含占位符 `__AEGIS_ADMIN_PATH__`、`__AEGIS_DOMAIN__`；`.env.example` 的机密与域名全是 `CHANGE_ME`。守卫：`render-nginx_test.sh`（渲染后不残留占位符或具体域名）
  - 维护者操作自己服务器的一次性脚本不进 deploy/，放仓库根被忽略的 `ops-local/`
- 破坏性脚本先校验全部输入，再做第一次破坏动作（先例 `migrate-to-new-host.sh`、`test-install.sh` 发现库里已有用户即拒绝）
- 迁移类脚本按自身位置找与 deploy/ 并排的 `migrations/`（可由 `AEGIS_MIGRATIONS_DIR` 覆盖），不写死安装路径：`/opt/aegispanel`（install.sh）与 `/opt/pandora`（install-native.sh）两种布局都要成立。守卫：`migrate-layout_mock_test.sh`
- 读 `.env` 的渲染类脚本不 source 它（文件里有机密，不能被执行）：`render-nginx.sh` 用 awk 逐键读，`public-base-url.sh` 带不 source 的单键读取
- 对外地址 `AEGIS_PUBLIC_BASE_URL` 的合规规则有三份，改一处要改三处：`public-base-url.sh` 的 `pandora_valid_public_base_url`、`render-nginx.sh` 的域名校验、Go 侧 `platform/config` 的 `CanonicalPublicOrigin`（生产要求 https + 公网 Host）。nginx 的 `server_name` 与证书路径就从这个值生成
- Cloudflare 真实 IP 信任表 `/etc/aegispanel/cloudflare-realip.conf` 的路径在模板、`render-nginx.sh`、`update-cloudflare-realip.sh` 三处写死，须一致；渲染器只在文件缺失时写一份不信任任何代理的默认文件，已存在绝不覆盖。守卫：`cloudflare-realip_mock_test.sh`
- 日志路径：`logrotate-aegis` 的 glob 必须覆盖三个 systemd 单元 `StandardOutput=append:` 的全部文件。守卫：`logrotate-aegis_static_test.sh`
- 新的发布物文件（脚本、模板、单元）要同时进 `build-release.sh` 的拷贝清单与随后的归档清单（两处 `for script in` 列表）、`install-linux-binaries.sh` 的安装事务；install-native.sh 要用的还得在它自己的拷贝行里加上
- `release-artifact.env` 由 `build-release.sh` 生成，只含 pdnd 版本与两架构 SHA-256，绝不写 `AEGIS_ENV`：aegis-node 在 `.env` 之后加载它，写进去会在升级时把已装机器的运行模式悄悄翻掉。守卫：`release-artifact-binding_mock_test.sh`
- 发布包装出来的就是生产：install.sh 首装写 `AEGIS_ENV=production`；升级不改现有运行模式，升级前自动全量备份，不替人造管理员
- 桩测试与静态检查（`*_mock_test.sh`、`*_static_test.sh`）不需要数据库；与安装、迁移、nginx、发布物绑定相关的，CI 的 `.github/workflows/panel-deploy.yml` 逐个点名跑，新增这类测试要补进那份清单
  - `release-stop-the-world_mock_test.sh`、`verify-backup_manifest_mock_test.sh` 需要 Linux root
- nginx 的节点路径（`/api/v1/server/UniProxy/`、`/v1/nodes/`）用自己的限速区：`aegis_node` 按「来源 IP + 节点标识」分桶（签名通道 `X-Node-Id` 头、兼容通道 query `node_id`，只认 UUID 形状，否则退回按 IP 一个桶），外加宽松的每 IP 总上限 `aegis_node_ip`；`limit_conn` 在这两个 location 单独写（`aegis_node_conn`），server 层的 64 不再作用于节点。一台机器 60 个节点约 810 次/分、60 条事件流。守卫：`render-nginx_test.sh`
- nginx 主配置的连接上限：`render-nginx.sh` 在输出位于 `<nginx 目录>/conf.d/` 时（或 `PANDORA_NGINX_MAIN_CONF` 指定）把 `nginx.conf` 的 `worker_connections` 抬到至少 8192、`worker_rlimit_nofile` 至少 65536，已更高的不动、认不出的结构不改；`install.sh` 的 `apply_edge_config` 连同 nginx.conf 一起备份，`nginx -t` 不过一起换回。每条 SSE / 节点事件流占两个连接，Debian 缺省 768 约一千条就满（5k-r4）。站点的 `error_log` 写 `/var/log/nginx/aegis-error.log crit`，不要再改回 /dev/null。守卫：`render-nginx_test.sh`、`install-chain_mock_test.sh`
- 三个网关单元的 `TimeoutStopSec` 一律 45 秒（两段停机各 20 秒）；valkey.sock 权限 700（容器 valkey 组的 gid 在宿主上常撞第一个普通用户）。守卫：`install-chain_mock_test.sh`
