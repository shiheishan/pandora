# P8 性能测试操作手册

本手册供测试机操作员（主 Agent）照着在测试机上运行 P8 第一轮性能测试并采集数据。
测试规划与目标详见 [docs/perf-test-plan.md](../../../docs/perf-test-plan.md)。测试规模为 5000 用户、200 台服务器（每台 1 个节点），流量走同机房 VPC 内网。

---

## 1. 用途与范围

### 四项测量

1. **静默占用**：nodesim 连着 200 台模拟服务器，无任何用户流量，观察面板、PostgreSQL、Redis 的 CPU 与内存底噪，分别在 0、100、200 台三个点取稳态平均，做线性拟合拆分「固定部分 + 每台服务器增量」。
2. **每请求数据库往返与慢 SQL**：通过 k6 依次对订阅拉取、门户读写、后台列表施压，看访问日志中的 `sql_n`（每请求 SQL 条数）、`redis_n` 与慢 SQL 的执行计划。
3. **三个突发**：
   - 整点批量到期：大量订阅在同一时刻到期，名单增量扇出到 200 台服务器的全量送达耗时与断线延迟；
   - 面板重启批量重连：面板重启后 200 台服务器同时重连恢复稳态的耗时与全量下发控制（期望 0 全量）；
   - 群发邮件：向全体 active 用户群发通知时，在线拉取请求的延迟波动（p95/p99）与通知积压清空曲线。
4. **节点转发**：在 1 台真节点（pdnd + sing-box）上观察万级名单应用是增量热更还是重建入站、已有代理连接是否断开。优先级低，单独安排。

### 本目录文件清单

| 文件 | 说明 |
|---|---|
| `README.md` | 本操作手册 |
| `prometheus.yml` | Prometheus 采集配置（采集 serve、worker、node_exporter、postgres_exporter 等） |
| `access_stats.py` | 访问日志统计脚本（解析 `serve.log`，输出每路由延迟分位数与 `sql_n`、`redis_n`） |
| `nginx-perf-exempt.sh` | 临时给压测机 IP 免除 nginx 每 IP 限流与并发限制的脚本 |
| `sql/pg_stat_statements.sql` | pg_stat_statements 扩展建表、清零、耗时 Top 20、调用频次 Top 20 与全表扫描查询模板 |
| `sql/perf_settings.sql` | 订阅前缀查询、临时放宽订阅每小时拉取上限及恢复 SQL |
| `k6/lib.js` | k6 脚本公共函数库 |
| `k6/sub_pull.js` | 订阅拉取压测场景（固定速率） |
| `k6/portal.js` | 门户读写混合场景（登录、查节点/订阅、下单续费） |
| `k6/admin.js` | 管理后台列表与看板接口压测场景 |
| `k6/bulk_mail.js` | 群发邮件触发与通知队列积压观测场景 |

---

## 2. 机器角色与内网拓扑

### 机器角色

| 角色 | 规格 | 推荐内网 IP | 运行组件 |
|---|---|---|---|
| 面板机 | 2c4g | `10.0.0.10` | install.sh 全套（`pandora-serve`、`pandora-worker`、PostgreSQL 18、Redis 8、nginx）+ `prometheus-node-exporter` + `prometheus-postgres-exporter` + 可选 `redis_exporter` |
| 压测机 1 | 4c8g+ | `10.0.0.20` | nodesim、k6、Prometheus、Mailpit、`prometheus-node-exporter` |
| 压测机 2（可选） | 4c8g+ | `10.0.0.21` | 需要隔离 nodesim 与 k6 资源时专门跑 k6 |
| 真节点（第 4 项） | 2c2g | `10.0.0.30` | 真实 pdnd + sing-box（仅第 4 项转发测试启用） |

### 网络与防火墙规则

所有测试流量**只走 VPC 内网**，严禁走公网：
1. **压测机 `/etc/hosts`** 追加：
   ```text
   10.0.0.10 panel.example.com
   ```
2. **端口连通**：面板机防火墙对压测机内网网段放行：
   - `443`（HTTPS 门户与后台，经过 nginx）
   - `9443`（节点 gRPC 通道，面板直接终结 mTLS，**不经 nginx**）
   - `9464`（`pandora-serve` metrics，只听内网）
   - `9465`（`pandora-worker` metrics，只听内网）
   - `9100`（node_exporter）
   - `9187`（postgres_exporter）
   - `9121`（redis_exporter，可选）
3. 压测机放行：
   - `1025`（Mailpit SMTP，供面板机 worker 推信，只听内网）
   - `8025`（Mailpit Web/API）
   - `9090`（Prometheus Web）

---

## 3. 环境准备

下文命令里的 `panel/tests/perf/...`、`panel/tests/invariants.sql` 指仓库里这些文件拷到机器上的位置（如 `scp -r panel/tests/perf root@10.0.0.10:/root/perf-tools/`），按实际路径替换。
两台机器都要开时间同步（`timedatectl` 看 `System clock synchronized: yes`，或 `chronyc tracking`）：突发场景要拿面板机与压测机的时间相减。

### 3.1 面板机准备

#### (1) 安装面板
按 `panel/deploy/RUNBOOK.md` 规范，使用 `install.sh` 完成初装，确认就绪探针均为 200：
```bash
curl -s http://127.0.0.1:9000/readyz
curl -s http://127.0.0.1:9001/readyz
```

#### (2) 安装监控 Exporter
```bash
sudo apt-get update && sudo apt-get install -y prometheus-node-exporter prometheus-postgres-exporter
```
配置 postgres_exporter：端口与库名取 `/etc/pandora/deploy.env` 的 `POSTGRES_PORT`、`POSTGRES_DB`；口令放进只有 exporter 用户可读的 0600 文件，不写进命令行和环境文件：
```bash
# 口令文件：awk 从 deploy.env 读 POSTGRES_SUPER_PASSWORD，经管道写入，不经命令行参数
sudo awk -F= '$1=="POSTGRES_SUPER_PASSWORD"{sub(/^[^=]*=/,""); print}' /etc/pandora/deploy.env \
  | sudo install -m 0600 -o prometheus -g prometheus /dev/stdin /etc/prometheus/pg-pass
sudo tee /etc/default/prometheus-postgres-exporter >/dev/null <<'CONF'
DATA_SOURCE_URI="127.0.0.1:<POSTGRES_PORT>/<POSTGRES_DB>?sslmode=disable"
DATA_SOURCE_USER="postgres"
DATA_SOURCE_PASS_FILE="/etc/prometheus/pg-pass"
ARGS="--web.listen-address=10.0.0.10:9187"
CONF
sudo systemctl restart prometheus-postgres-exporter
curl -s 10.0.0.10:9187/metrics | grep -m1 '^pg_up'   # 应为 pg_up 1
```
node_exporter 同理把监听改到内网地址（`/etc/default/prometheus-node-exporter` 里 `ARGS="--web.listen-address=10.0.0.10:9100"`）。

#### (3) 开启 PostgreSQL pg_stat_statements
创建 `/etc/postgresql/18/main/conf.d/zz-perf.conf`：
```ini
shared_preload_libraries = 'pg_stat_statements'
pg_stat_statements.track = top
pg_stat_statements.max = 5000
track_io_timing = on
```
重启 PostgreSQL 并初始化扩展：
```bash
sudo systemctl restart postgresql@18-main
sudo /opt/pandora/deploy/psql.sh -v action=setup -f panel/tests/perf/sql/pg_stat_statements.sql
```

#### (4) 临时放宽面板应用层限流与开启 Metrics
编辑 `/etc/pandora/config.yaml`，在对应层级追加或修改：
```yaml
observability:
  metrics_addr: "10.0.0.10:9464"
worker:
  metrics_addr: "10.0.0.10:9465"
ratelimit:
  ip_per_min: 1000000
  account_per_min: 1000000
  auth_per_min: 100000
auth:
  access_ttl: 1h
```
放宽订阅每小时单凭据拉取上限（`show` 顺便打印订阅前缀，k6 的 `SUB_PREFIX` 用它）：
```bash
sudo /opt/pandora/deploy/psql.sh -v action=up -f panel/tests/perf/sql/perf_settings.sql
sudo /opt/pandora/deploy/psql.sh -v action=show -f panel/tests/perf/sql/perf_settings.sql
```
给压测机来源 IP 免除 nginx 每 IP 限流：
```bash
sudo bash panel/tests/perf/nginx-perf-exempt.sh apply 10.0.0.20/32 10.0.0.21/32
```
重启面板生效：
```bash
sudo systemctl restart pandora-serve pandora-worker
```

### 3.2 压测机准备

#### (1) 安装 k6、Prometheus 与 Mailpit
- **k6**：安装官方 release（deb 或二进制），确认版本为 v2.x：
  ```bash
  k6 version
  ```
- **Prometheus**：解压官方二进制，将 `panel/tests/perf/prometheus.yml` 拷到压测机，确认 targets 中的内网 IP 为 `10.0.0.10`、`10.0.0.20`，启动：
  ```bash
  setsid -f ./prometheus --config.file=prometheus.yml --web.listen-address="10.0.0.20:9090" > prometheus.log 2>&1 < /dev/null
  ```
- **Mailpit**：解压官方 release，SMTP 监听绑定内网：
  ```bash
  # --max 0：不限存储条数（缺省只留 500 封，群发的收件数会被截断）
  setsid -f ./mailpit --smtp 10.0.0.20:1025 --listen 10.0.0.20:8025 --max 0 > mailpit.log 2>&1 < /dev/null
  ```
- **node_exporter**：启动并监听 `:9100`。

#### (2) 配置面板 SMTP 指向 Mailpit
在面板机上登录后台并配置发信（口令只经 0600 文件读取，不出现在命令行）：
```bash
# 获取管理员 token（ADMIN_PASS_FILE 为保存密码的 0600 文件）
ADMIN_TOKEN=$(curl -sS -X POST "http://127.0.0.1:9001/v1/auth/login" \
  -H "Content-Type: application/json" \
  --data @- <<EOF | python3 -c 'import sys, json; print(json.load(sys.stdin)["access_token"])'
{"email":"admin@panel.example.com","password":"$(cat /root/.admin_pass)"}
EOF
)

# 改邮件设置要近期重新验证：用同一个口令换一枚重新验证过的 token
ADMIN_TOKEN=$(curl -sS -X POST "http://127.0.0.1:9001/v1/auth/reauth" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  --data @- <<EOF | python3 -c 'import sys, json; print(json.load(sys.stdin)["access_token"])'
{"password":"$(cat /root/.admin_pass)"}
EOF
)

# 写入 Mailpit 配置
curl -sS -X POST "http://127.0.0.1:9001/v1/settings/mail" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"smtp_host":"10.0.0.20","smtp_port":1025,"encryption":"none","from_address":"noreply@panel.example.com","from_name":"Perf"}'
```

---

## 4. 造数与 nodesim 启动顺序

所有造数和节点连入必须严格按以下顺序进行，每步均有检查点：

### 步骤 1：编译工具
测试机上不装 Go：在开发机交叉编译，再 `scp` 过去（本机 Go 加 `GOTOOLCHAIN=go1.26.10`）。
```bash
( cd panel && GOTOOLCHAIN=go1.26.10 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/perfseed ./tools/perfseed )
( cd pdnd && GOTOOLCHAIN=go1.26.10 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/nodesim ./release/nodesim )
scp /tmp/perfseed root@10.0.0.10:/root/perf-data/
scp /tmp/nodesim root@10.0.0.20:/root/perf-sim/
```

### 步骤 2：生成基础拓扑 (perfseed infra)
在面板机上执行，生成 200 台服务器的配置与接入凭据：
```bash
mkdir -p /root/perf-data
./perfseed infra \
  --admin-url "http://127.0.0.1:9001" \
  --admin-email "admin@panel.example.com" \
  --admin-password-file "/root/.admin_pass" \
  --servers 200 \
  --out-dir "/root/perf-data"
```
【检查点】：`/root/perf-data/` 下应生成 `tokens`、`nodesim.env`、`admin.env`。`nodesim.env` 含 `PANEL=...` 与 `CA_SHA256=...`。

### 步骤 3：同步凭据至压测机并启动 nodesim
将令牌文件同步到压测机（0600 权限传输）：
```bash
scp -P 22 /root/perf-data/tokens /root/perf-data/nodesim.env root@10.0.0.20:/root/perf-sim/
```
登录压测机 `10.0.0.20` 启动 nodesim：
```bash
cd /root/perf-sim
chmod 600 tokens nodesim.env
source nodesim.env
mkdir -p state

# 面板每分钟限 10 次接入；首批接入加流控参数，大约耗时 20 分钟
# --spread 让各台按真实节点错开上报（首轮随机、每轮 ±10% 抖动），免得 200 台同一时刻打到面板
setsid -f ./nodesim \
  --panel "$PANEL" \
  --ca-sha256 "$CA_SHA256" \
  --tokens-file tokens \
  --state-dir state \
  --status-file status.json \
  --status-users=false \
  --enroll-attempts 0 \
  --enroll-concurrency 4 \
  --enroll-retry-wait 10s \
  --spread \
  --report-interval 10s > nodesim.log 2>&1 < /dev/null
```
【检查点】：在压测机上循环查看接入汇总，必须等到 200 台全部接入并建立连接：
```bash
until ./nodesim summary --status-file status.json | grep -q 'connected=200 '; do sleep 10; done
./nodesim summary --status-file status.json
# 期望首行：servers=200 enrolled=200 connected=200 tombstoned=0 errors=0 nodes=200
# 接入被面板限流时 nodesim 自动重试（日志里是「接入失败，稍后重试」），重试不计 errors；errors 非 0 看 status.json 里各台的 last_error
```

### 步骤 4：激活节点与生成套餐 (perfseed activate)
在面板机执行：
```bash
./perfseed activate \
  --admin-url "http://127.0.0.1:9001" \
  --admin-email "admin@panel.example.com" \
  --admin-password-file "/root/.admin_pass" \
  --out-dir "/root/perf-data"
```
【检查点】：`/root/perf-data/catalog.json` 生成，节点全部进入 active 服务状态；nodesim summary 里出现 `version` 行（节点拿到了名单）。

### 步骤 5：造 5000 用户与到期订阅 (perfseed users)
在面板机执行（DSN 文件使用属主角色 `pandora_owner` 连接串）：
```bash
# 设定一批（例如 1000 个）在下一个整点到期（用于突发场景 1）
NEXT_HOUR=$(date -u -v+1H "+%Y-%m-%dT%H:00:00Z" 2>/dev/null || date -u -d "+1 hour" "+%Y-%m-%dT%H:00:00Z")

./perfseed users \
  --dsn-file "/etc/pandora/owner-dsn.txt" \
  --out-dir "/root/perf-data" \
  --users 5000 \
  --user-password-file "/root/.user_pass" \
  --expire-at "$NEXT_HOUR" \
  --expire-count 1000 \
  --history-days 30
```
【检查点】：按 `panel/tools/perfseed/README.md` 的说明让用户名单生效（触发增量同步/版本推进）。随后在压测机观察：
```bash
./nodesim summary --status-file status.json
```
确认全部 200 个节点的 `version` 已刷新为最新的版本号。

### 步骤 6：数据完整性校验
在面板机执行只读不变式检查：
```bash
sudo /opt/pandora/deploy/psql.sh -f panel/tests/invariants.sql
```
最后把 `users.csv` 与 `admin.env` 拷到压测机 `/root/perf-sim/`（`scp`，到达后 `chmod 600`）。管理员口令文件 `/root/.admin_pass`（0600）两台机器各放一份。
【检查点】：所有断言通过（单事务执行，末尾自动 ROLLBACK）。

---

## 5. 测量纪律与数据收集

1. **物理隔离**：面板机与压测机绝对分离，严禁在面板机上跑 k6 或 nodesim。
2. **以服务端为准**：延迟与吞吐以面板日志 `dur_ms`、`access_stats.py` 及 Prometheus 的 `pandora_http_request_duration_seconds` 为准，k6 的客户端计时仅作参考对照。
3. **稳态窗口**：测试场景必须先预热 2 分钟，再取至少 5 分钟的稳定状态窗口数据。
4. **重复验证**：每个场景至少独立重复执行 3 次，结果取平均与方差。
5. **控制单变量**：每次测试仅允许调整一个参数（如速率、并发数、连接数）。
6. **环境基线记录**：每次测试前执行 `/opt/pandora/bin/pandora version` 记下 commit sha，并记录机型、内核、配置变更与数据规模。
7. **SQL 状态重置**：每个场景开跑前，必须重置 `pg_stat_statements`：
   ```bash
   sudo /opt/pandora/deploy/psql.sh -v action=reset -f panel/tests/perf/sql/pg_stat_statements.sql
   ```

---

## 6. 第 1 项测量：静默占用与线性拟合

### 测量目的
验证 200 台服务器连上 gRPC 会话保持心跳与在线上报、无任何用户请求时的系统底噪。拆分系统「固定部分底噪」与「每台服务器底噪增量」。

### 观察点与命令
在 200 台保持连接下静默观察 15 分钟：
1. **面板机进程资源占用**（每 60 秒采样一次）：
   ```bash
   pidstat -urh -p $(pgrep -d, -f 'pandora serve|pandora worker|postgres|redis-server') 60
   ```
2. **Redis 指标前后差**：
   在开始和结束时分别抓取 `redis-cli info commandstats`，比对命令调用量增量。
3. **数据库执行频次**：
   ```bash
   sudo /opt/pandora/deploy/psql.sh -P pager=off -v action=calls -f panel/tests/perf/sql/pg_stat_statements.sql
   ```
   检查心跳是否写库（预期心跳不落库）、流量上报写表频次及单次行数。

### 0 / 100 / 200 台三点线性拟合
分别在 0 台、100 台、200 台三个点各取 10 分钟稳态平均：
- **0 台**：压测机直接 `pkill -f 'nodesim'` 停止模拟；
- **100 台**：截取 `tokens` 文件前 100 行至 `tokens_100`，启动 nodesim 指向 `tokens_100`，等待 100 台连接稳态；
- **200 台**：使用全部 200 行 `tokens` 启动。

### 监控指标 (PromQL)
- `pandora-serve` CPU 占用率：
  ```promql
  rate(process_cpu_seconds_total{job="pandora-serve"}[1m])
  ```
- 进程常驻内存 (RSS)：
  ```promql
  process_resident_memory_bytes{job=~"pandora-.*"}
  ```
- 面板请求速率（检查无多余背景流量）：
  ```promql
  sum by (route) (rate(pandora_http_requests_total[1m]))
  ```
  *(注：指标名以实际 `/metrics` 输出为准，可先通过 `curl -s 10.0.0.10:9464/metrics | grep ^pandora_ | cut -d'{' -f1 | sort -u` 查看)*

---

## 7. 第 2 项测量：每请求数据库往返与慢 SQL

### 测量目的
测量 ORM 重构后各热点接口是否存在 N+1 查询，测量数据库与 Redis 往返次数、耗时及全表扫描。

### 压测执行
在压测机上执行；每个场景前先在面板机清零 `pg_stat_statements`，并记下开始、结束时间（`date -Iseconds`），给 `access_stats.py` 用。

```bash
# 公共变量。K6_INSECURE_SKIP_TLS_VERIFY 是 k6 自己读的环境变量（证书不受信时用），必须 export，不能用 -e 传
export K6_INSECURE_SKIP_TLS_VERIFY=true
export BASE_URL=https://panel.example.com
export SUB_PREFIX=<面板机 perf_settings.sql action=show 打印的 sub_prefix>
ADMIN_PATH=<面板机 sudo /opt/pandora/deploy/admin-url.sh 打印的后台路径前缀>
export ADMIN_BASE_URL="https://panel.example.com/$ADMIN_PATH"
export ADMIN_ENV_FILE=/root/perf-sim/admin.env ADMIN_PASSWORD_FILE=/root/.admin_pass
export USERS_CSV=/root/perf-sim/users.csv

# 1. 订阅拉取（RATE 20、50、100 三档，各 2 分钟预热 + 5 分钟稳态）
for r in 20 50 100; do
  RATE=$r DURATION=7m k6 run --summary-export=summary-sub-$r.json panel/tests/perf/k6/sub_pull.js
done

# 2. 门户读写（setup 先登录 200 人；登录限流已按 3.1 放宽）
LOGIN_USERS=200 WRITE_USERS=50 READ_RATE=20 WRITE_RATE=1 DURATION=7m \
  k6 run --summary-export=summary-portal.json panel/tests/perf/k6/portal.js

# 3. 后台列表与看板
RATE=5 DURATION=7m k6 run --summary-export=summary-admin.json panel/tests/perf/k6/admin.js
```

门户写场景缺省是「续费下单 → 取消」。要连支付回调一起测（续费单经易支付回调入账），先建一个测试易支付渠道（只在测试机做；`pay.example.test` 不会被访问，回调由 k6 自己签名发起）：
1. 面板机生成渠道密钥：`head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n' > /root/.epay_key && chmod 600 /root/.epay_key`；
2. 照 `panel/tests/epay_e2e.sh` 第 0 步，对 `http://127.0.0.1:9001` 依次：登录拿 token → `POST /v1/auth/reauth`（口令经 `--data @-` 从 stdin 给）→
   `POST /v1/payment-providers`（带 `Idempotency-Key`；body `{"code":"epay-perf","adapter":"epay","display_name":"Perf","base_url":"https://pay.example.test","methods":["alipay"],"default_method":"alipay","merchant_id":"1001","key":<密钥>}`，用 python3 读密钥文件拼 JSON、经 stdin 交给 curl）→
   `POST /v1/payment-providers/epay-perf/toggle`，body `{"enabled":true,"accepting_new":true}`；
3. 把 `/root/.epay_key` 拷到压测机（0600），然后：
```bash
EPAY_CODE=epay-perf EPAY_KEY_FILE=/root/.epay_key LOGIN_USERS=200 WRITE_RATE=1 DURATION=7m \
  k6 run --summary-export=summary-portal-epay.json panel/tests/perf/k6/portal.js
```
回调接口每 IP 每分钟限 120 次（不受配置控制），`WRITE_RATE` 不要超过 2。

### 数据分析
每个场景结束后，在面板机提取日志与 SQL 状态：
```bash
# 访问日志指标提取（查看 sql_avg、sql_max、redis_avg 等）
python3 panel/tests/perf/access_stats.py \
  --from "2026-10-13T10:00:00Z" \
  --to "2026-10-13T10:05:00Z" \
  --top 20 \
  /var/log/pandora/serve.log

# 抓取 SQL 耗时 Top 20、频次 Top 20、全表扫描嫌疑
sudo /opt/pandora/deploy/psql.sh -P pager=off -v action=top -f panel/tests/perf/sql/pg_stat_statements.sql > top_sql.txt
sudo /opt/pandora/deploy/psql.sh -P pager=off -v action=scans -f panel/tests/perf/sql/pg_stat_statements.sql > scan_sql.txt

# 对热点慢 SQL 查看执行计划（将 $1 换为实际参数值）
sudo /opt/pandora/deploy/psql.sh -c "EXPLAIN (ANALYZE, BUFFERS, SETTINGS) <SQL语句>;"
```

---

## 8. 第 3 项测量：三个突发场景

### 8.1 场景一：整点批量到期扇出
- **前提**：在造数阶段已将 1000 个订阅的到期时间设为整点 `T`（即 `perfseed users --expire-at` 的值，如 `T=2026-10-13T15:00:00Z`）。
- **操作**：
  1. 到点前 2 分钟，在压测机启动低速探针保持基准拉取：
     ```bash
     RATE=20 DURATION=10m k6 run --summary-export=summary-expiry-probe.json panel/tests/perf/k6/sub_pull.js > expiry-probe.log 2>&1 &
     ```
  2. 到点 `T` 后，在压测机循环观测名单版本变化：
     ```bash
     ./nodesim summary --status-file status.json --since "$T"
     ```
     记录出现的新版本号 `V`。然后带版本参数循环等待直到退出码为 0：
     ```bash
     until ./nodesim summary --status-file status.json --version "$V" --since "$T"; do sleep 0.5; done
     ```
  3. **指标记录**：
     - `applied_max` 对应的 `(+...s)` 即为全量送达 200 台服务器耗时，`applied_min` 为最快送达耗时；
     - 核对 `rosters deltas` 增量应接近 200（每节点一次增量下发），确认无意外 fulls 全量广播；
     - 查阅 `worker.log` 中到期任务执行时间，计算任务执行与增量推送之间的调度延迟。

### 8.2 场景二：面板重启后批量重连
- **操作**：
  1. 在压测机记下时间并经 ssh 重启（T 与 nodesim 用同一个时钟）：
     ```bash
     T=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
     ssh root@10.0.0.10 'systemctl restart pandora-serve'
     ```
  2. 在压测机循环等待：全部 connected，且最早一台的 `connected_at` 也在 T 之后（偏移为正）才算全部重连过。
     刚重启时 nodesim 可能还没发现断线、仍显示 connected=200，所以不能只看台数：
     ```bash
     until out=$(./nodesim summary --status-file status.json --since "$T") \
       && grep -q 'connected=200 ' <<<"$out" \
       && grep -Eq '^connected min=[^ ]+ \(\+' <<<"$out"; do sleep 0.5; done
     echo "$out"
     ```
  3. **指标记录**：
     - 重连总耗时：输出中 `connected max` 相对 `$T` 的偏移；
     - 核对 `rosters fulls` 增量：理想情况下版本一致应为 0（不重发全量配置）；
     - 观察面板机 CPU 峰值与 gRPC 握手耗时；
     - 独立重复执行 3 次。

### 8.3 场景三：通知群发与在线业务干扰
- **操作**：
  1. 在压测机终端 1 启动订阅拉取长跑（基准压力），先预热 3 分钟：
     ```bash
     RATE=50 DURATION=20m k6 run --summary-export=summary-bulk-probe.json panel/tests/perf/k6/sub_pull.js
     ```
  2. 预热 3 分钟后，在压测机终端 2 触发群发：
     ```bash
     POLL_SEC=2 k6 run panel/tests/perf/k6/bulk_mail.js | tee bulk_mail.log
     ```
  3. **指标记录**：
     - Prometheus 查询群发开始前后订阅拉取请求延迟：
       ```promql
       histogram_quantile(0.95, sum by (le) (rate(pandora_http_request_duration_seconds_bucket{server="public"}[1m])))
       histogram_quantile(0.99, sum by (le) (rate(pandora_http_request_duration_seconds_bucket{server="public"}[1m])))
       ```
     - 确认 Mailpit 最终收件总数：
       ```bash
       curl -s http://10.0.0.20:8025/api/v1/messages?limit=1 | python3 -c 'import sys, json; print("已收邮件:", json.load(sys.stdin)["total"])'
       ```
     - 记录 `bulk_mail.js` 输出的队列积压清空曲线及总耗时。

---

## 9. 第 4 项测量：节点转发（低优先级）

本项关注节点端转发核心在下发万级名单时的热更新行为。
- **环境要求**：1 台真实服务器部署 `pdnd`（原生 NativeCore / sing-box），以及压测客户端并发挂载多条活跃代理连接。
- **执行方式**：单独安排，详见 `node-e2e` 相关操作流程。
- **记录指标**：
  1. 万级名单应用总耗时（内核热更新时间）；
  2. 名单应用瞬间已有长连接是否中断（断开连接计数，预期 0）。

---

## 10. 收尾与环境还原

测试结束后，必须清理临时配置：
1. **还原 nginx 限流**：
   ```bash
   sudo bash panel/tests/perf/nginx-perf-exempt.sh revert
   ```
2. **还原数据库 settings 表**：
   ```bash
   sudo /opt/pandora/deploy/psql.sh -v action=down -f panel/tests/perf/sql/perf_settings.sql
   ```
3. **还原配置文件**：
   在 `/etc/pandora/config.yaml` 中删除临时追加的 `ratelimit`、`auth` 及 `metrics_addr`（若不再需要），重启服务：
   ```bash
   sudo systemctl restart pandora-serve pandora-worker
   ```
4. **移除 PostgreSQL 性能插件配置**：
   ```bash
   sudo rm -f /etc/postgresql/18/main/conf.d/zz-perf.conf
   sudo systemctl restart postgresql@18-main
   ```
5. **停止压测机后台进程**：
   ```bash
   pkill -f 'nodesim'
   pkill -f 'prometheus'
   pkill -f 'mailpit'
   ```
6. **机器回收**：测试机为一次性实例，按照 test-machine skill 完成销毁与账单结算。

---

## 11. 结果存档格式与 run.md 模板

测试数据与过程产物统一归档至主仓库被 git 忽略的目录：
`ops-local/perf/<YYYYMMDD>-<场景名>/`

### 归档内容清单
- `run.md`（本次运行的元数据与综合报告）
- k6 导出的汇总 JSON（`summary-*.json`）
- `nodesim summary` 输出文本
- `access_stats.py` 输出报告
- `top_sql.txt` 与 `scan_sql.txt`（pg_stat_statements 输出）
- Prometheus 导出图表或监控截图

### run.md 报告模板

```markdown
# 性能测试运行记录: <场景名>

- **测试时间**: 2026-10-13 14:00 ~ 16:00 (UTC)
- **提交版本**: git commit `abc1234` (/opt/pandora/bin/pandora version)
- **机器规格**:
  - 面板机: 2c4g (10.0.0.10, Debian 12, 内核 6.1)
  - 压测机: 4c8g (10.0.0.20)
- **数据规模**: 5000 用户, 200 模拟服务器/节点, 30 天历史账单与订单
- **参数变更**: 临时放宽 ratelimit，设置 access_ttl=1h，启用 pg_stat_statements

## 测量结果汇总

### 1. 资源底噪与拟合
- 0 台连接: CPU __%, RSS __ MB
- 100 台连接: CPU __%, RSS __ MB
- 200 台连接: CPU __%, RSS __ MB
- 拟合结论: 基础固定底噪 __ MB，每服务器底噪增量 __ KB/台

### 2. 热点接口与往返表现
| 接口/场景 | 吞吐 (RPS) | 服务端 p95 (ms) | 服务端 p99 (ms) | 每请求 SQL 条数 (avg/max) | 每请求 Redis 次数 (avg) |
|---|---|---|---|---|---|
| 订阅拉取 | | | | | |
| 门户读写 | | | | | |
| 后台列表 | | | | | |

### 3. 突发场景指标
- 整点到期扇出耗时: 最快 __s, 全部送达 __s, deltas __ 个, fulls __ 个
- 重启重连恢复耗时: __s (connected=200), 重发 fulls __ 个
- 群发邮件积压清空耗时: __s, 在线拉取 p99 波动: __ms -> __ms

### 4. 慢 SQL 与瓶颈
- 耗时最高 SQL: `...`
- 全表扫描嫌疑: `...`
- 优化建议: ...
```

