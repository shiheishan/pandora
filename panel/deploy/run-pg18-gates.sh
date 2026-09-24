#!/usr/bin/env bash
# 一次跑完全部 PostgreSQL 18 集成测试。
#
# 这些测试各自要求一个专用的一次性数据库，而且每个域的入口检查都不一样：
# 库名前缀、32 位 run ID、数据库 OID、集群 system identifier……散在十几个
# 环境变量里。没有这个脚本的时候，它们的实际状态是「存在但没人跑」——
# signed_e2e 在两阶段接入落地后红了很久都没被发现，因为启动一次的成本
# 高到没人愿意付。
#
# 用法：
#   run-pg18-gates.sh <panel 源码目录> [域名...]
#
# 不带域名就跑全部。举例：
#   run-pg18-gates.sh /tmp/verify/panel
#   run-pg18-gates.sh /tmp/verify/panel effective enrollment
#
# 需要 docker 和 goose。全程只碰自己建的容器，不读写任何既有数据库。

set -Eeuo pipefail

PANEL_DIR="${1:-}"
if [[ -z "$PANEL_DIR" || ! -f "$PANEL_DIR/go.mod" ]]; then
  echo "用法: $0 <panel 源码目录> [域名...]" >&2
  exit 2
fi
PANEL_DIR="$(cd "$PANEL_DIR" && pwd)"
shift || true
WANTED=("$@")

CONTAINER="${PANDORA_PG18_GATE_CONTAINER:-pandora-pg18-gates-$$}"
PG_IMAGE="${PANDORA_PG18_GATE_IMAGE:-postgres:18-alpine}"
GOOSE="${GOOSE_BIN:-/root/go/bin/goose}"

# 用错 Go 版本时，编译错误会刷满整个输出（"package slices is not in GOROOT"
# 之类），看着像代码坏了，其实只是 PATH 里是系统的老 Go。这里先拦一道。
require_go_version() {
  local want have
  want="$(awk '/^go /{print $2; exit}' "$PANEL_DIR/go.mod")"
  [ -n "$want" ] || return 0
  command -v go >/dev/null 2>&1 || {
    echo "找不到 go；go.mod 要求 $want" >&2
    exit 2
  }
  have="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  # 只比较主次版本：go.mod 写 1.26.5 时，1.26.7 同样可用。
  local want_mm have_mm
  want_mm="$(printf '%s' "$want" | cut -d. -f1,2)"
  have_mm="$(printf '%s' "$have" | cut -d. -f1,2)"
  if [ "$want_mm" != "$have_mm" ]; then
    echo "Go 版本不匹配：go.mod 要求 $want，当前 $have（$(command -v go)）" >&2
    echo "把正确的 Go 放进 PATH 再跑，例如：export PATH=/usr/local/go126/bin:\$PATH" >&2
    exit 2
  fi
}
require_go_version
# 迁移用 aegis 跑，和生产一致。
#
# 这不是洁癖：`ALTER DEFAULT PRIVILEGES` 不带 FOR ROLE 时绑定的是执行它的
# 那个角色，新建表的属主也随执行者走。拿 postgres 跑出来的库，权限拓扑和
# 生产不是同一个形状——测出来的「权限被拒」可能只是环境差异，反过来也
# 可能把真实的授权缺失盖过去。
PGUSER_MIGRATE=aegis
PGPW=pg18-gate-password
APPPW=app-gate-password
# 迁移到 59 号为止有四道 fail-closed 闸门，要显式声明写入者已停、并逐个
# 批准该版本的升级。它们守的是生产误操作，一次性库里必须原样满足——
# 绕过闸门跑出来的绿灯证明不了生产能升上去。
MIGRATE_OPTS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes -c app.order_release_writers_stopped=yes'

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  [[ -n "${LOG_DIR:-}" ]] && rm -rf "$LOG_DIR"
  return 0
}
trap cleanup EXIT INT TERM
cleanup
# 每个域的 go test -v 输出留一份，跑完后用来查「有没有被跳过」。
LOG_DIR="$(mktemp -d)"

# 域定义：名字|库名|测试包|额外变量|标记表|数据库注释前缀|准备动作|测试过滤
#
# 后三样都是各域自己的一次性 fixture 护栏，缺一个测试就拒绝启动：
#   - 库名前缀：确认这不是随便一个库
#   - 标记表：库里必须有一行 run_id，与环境变量对上
#   - 数据库注释：shobj_description 也要带同一个 run_id
# 三道都要求同一个 run ID，误指向一个真库时三道都过不去。
#
# 这里有几处从别处拷代码留下的错位，如实照做而不是「修正」它们——库名前缀
# 和注释前缀写在测试源码里，脚本单方面改只会永远对不上：
#   - announcement 和 support 都要求 pandora_node_preview_ 库名前缀
#   - 这两个域的注释前缀也都是 pandora-node-preview-pg18
# 要理顺就得连测试一起改，那是另一件事。
#
# 测试过滤缺省是 PG18。好几个包里住着不止一个域（nodefabric：effective、
# enrollment 与 traffic_charge；api/admin：announcement 与 node_config；
# subscription：node_preview 与 usage_daily），缺省过滤会把另一个域的测试
# 也拉进来，它们因为拿不到自己的环境变量而 t.Skip。跳过在下面算失败（见
# 结果判定），所以同包的域必须把过滤写精确。过滤是最后一个字段，
# 里面的 | 会被 read 原样留给它。
DOMAINS=(
  "effective|pandora_effective_pg18|./internal/domain/nodefabric ./internal/api/node|||||^(TestEffectiveReleasePG18|TestSignedNodeHTTPPG18)$"
  "enrollment|pandora_enrollment_pg18|./internal/domain/nodefabric|||||^(TestNodeEnrollmentPG18|TestIssueServerTokenPG18)$"
  "announcement|pandora_node_preview_announce|./internal/api/admin|run_id|pandora_announcement_test_marker|pandora-node-preview-pg18||^(TestAnnouncementPG18|TestDeviceLimitWritesPG18|TestAccessLogCategoryPG18|TestNodeRoutingGlobalOutboundPG18|TestNodeListPagingPG18|TestIPClusterPG18|TestAuditLogPG18|TestNodeCountryAndCredentialsPG18|TestPluginDeliveryDurationPG18|TestSiteSettingsPG18|TestDashboardTasksPG18|TestFeatureSwitchGatesPG18|TestAdminMeProfilePG18|TestUserProfileRegisteredIPPG18)$"
  "node_config|pandora_nodecfg_gate|./internal/api/admin|run_id,oid,system_id||pandora-nodecfg-disposable||^(TestNodeConfigLegacyPG18|TestNodeConfigPG18LockSchedule)$"
  "catalog_sales|pandora_catalog_sales_gate|./internal/domain/adminops|run_id|pandora_catalog_sales_test_marker|pandora-catalog-sales-pg18||"
  "giftcard|pandora_giftcard_gate|./internal/domain/giftcard|run_id|pandora_giftcard_test_marker|pandora-giftcard-pg18||"
  "content|pandora_content_gate|./internal/domain/content|run_id|pandora_content_test_marker|pandora-content-pg18||"
  "appearance|pandora_appearance_gate|./internal/domain/appearance|run_id|pandora_appearance_test_marker|pandora-appearance-pg18||"
  "public_api|pandora_public_api_gate|./internal/api/public|run_id|pandora_public_api_test_marker|pandora-public-api-pg18||"
  "logout|pandora_logout_gate|./internal/domain/identity|run_id|pandora_logout_test_marker|pandora-logout-pg18||"
  "node_preview|pandora_node_preview_gate|./internal/domain/subscription|run_id|pandora_node_preview_test_marker|pandora-node-preview-pg18||^TestNodePreviewPG18$"
  "support|pandora_node_preview_support|./internal/domain/support|run_id|pandora_support_test_marker|pandora-node-preview-pg18||"
  "billing|pandora_billing_gate|./internal/domain/billing|billing|||app_role,billing_seed|TestCheckoutAtomicPG18|TestSettlementPG18"
  # order_release 必须独占一个库：它断言 app.order_release_00040_meta 这个
  # 全局单例水位在开跑时还是 false，而 checkout / settlement 一旦在同一个库里
  # 先跑过就会把它置上。原 runner 的 -run 从不包含它，所以这条冲突一直没显形。
  # 它自带 seed（orderReleasePG18Seed 用随机 UUID 建整套租户与套餐），不能
  # 再灌 billing 那份——后者带着已取消 / 已过期的订单，一进库就把释放路径的
  # 水位置上，而这个测试开跑第一件事就是断言水位还是干净的。
  "order_release|pandora_order_release_gate|./internal/domain/billing|order_release|||app_role|TestOrderReleasePG18"
  # 流量包（00070）：下单与余额在 billing，扣量在 nodefabric；各自独占一个库，
  # 过滤写精确，免得同包里别的 PG18 测试因拿不到环境变量而被算作跳过。
  "traffic_pack|pandora_traffic_pack_gate|./internal/domain/billing||||app_role|^TestTrafficPackOrderPG18$"
  "traffic_charge|pandora_traffic_charge_gate|./internal/domain/nodefabric||||app_role|^(TestTrafficChargePG18|TestUsageDailyWritePG18)$"
  # 变更套餐（00071）：同 traffic_pack，复用 order_release 的一次性租户夹具，独占一个库。
  "plan_change|pandora_plan_change_gate|./internal/domain/billing||||app_role|^TestPlanChangePG18$"
  # 按日流量（00072）：写入与扣量同事务，写入测试并进 traffic_charge 的库；
  # 读模型在 subscription 包，与 node_preview 同包，两边过滤都写精确。
  "usage_daily|pandora_usage_daily_gate|./internal/domain/subscription||||app_role|^TestUsageDailyReadPG18$"
  "idempotency|pandora_idempotency_gate|./internal/middleware||||app_role,idempotency_seed|"
  # 审计哈希链（00086 第二版口径）：篡改用例要绕过追加写触发器，只在这个一次性库里做
  "audit|pandora_audit_gate|./internal/platform/audit|run_id|pandora_audit_test_marker|pandora-audit-pg18||"
  "notify|pandora_notify_gate|./internal/domain/notify|run_id|pandora_notify_test_marker|pandora-notify-pg18||"
)

selected() {
  [[ ${#WANTED[@]} -eq 0 ]] && return 0
  local d
  for d in "${WANTED[@]}"; do [[ "$d" == "$1" ]] && return 0; done
  return 1
}

echo "==> 起 PostgreSQL 18"
docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD="$PGPW" -e POSTGRES_USER="$PGUSER_MIGRATE" -e POSTGRES_DB=postgres \
  -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null
# 就绪检查必须走 TCP：镜像初始化时会先起一个只听 unix socket 的临时实例，跑完
# 初始化脚本再关掉、重启成正式实例。经 socket 探测会在临时实例上报「就绪」，
# 紧接着的迁移正好撞上它关停（第 ④ 步 CI 的偶发失败）。临时实例不监听 TCP。
READY=""
for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -h 127.0.0.1 -U "$PGUSER_MIGRATE" -q 2>/dev/null; then
    READY=1
    break
  fi
  sleep 1
done
if [[ -z "$READY" ]]; then
  echo "PostgreSQL 18 容器 60 秒内没有在 TCP 上就绪" >&2
  docker logs "$CONTAINER" 2>&1 | tail -20 >&2
  exit 1
fi
PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$CONTAINER")"
psql_root() { docker exec -i "$CONTAINER" psql -U "$PGUSER_MIGRATE" -v ON_ERROR_STOP=1 "$@"; }
echo "    $(psql_root -d postgres -tAc 'SELECT version()' | cut -d, -f1)"

# 先把迁移跑进一个模板库，其余库从它克隆。
#
# 11 个库各跑一遍 59 个迁移要一分多钟，而 CREATE DATABASE ... TEMPLATE
# 是文件级复制，快一个数量级。克隆出来的库带着完整的表、约束、触发器和
# 表级 ACL——那些正是测试要验的东西。
echo "==> 迁移模板库（00001–最新）"
psql_root -d postgres -c "CREATE DATABASE pandora_gate_template" >/dev/null
cd "$PANEL_DIR"
PGOPTIONS="$MIGRATE_OPTS" "$GOOSE" -dir migrations postgres \
  "postgres://${PGUSER_MIGRATE}:${PGPW}@127.0.0.1:${PORT}/pandora_gate_template?sslmode=disable" up 2>&1 | tail -1

# 运行时角色的加固。search_path 和登录属性是集群级的，设一次就够；
# TEMPORARY 权限是每个库各自一份，克隆完要逐个收掉。
psql_root -d pandora_gate_template -c \
  "ALTER ROLE aegis_app LOGIN PASSWORD '${APPPW}'; ALTER ROLE aegis_app SET search_path TO pg_catalog, public, pg_temp;" >/dev/null

# 补一个不可登录的 postgres 角色。
#
# 容器的超级用户是 aegis（为了让 ALTER DEFAULT PRIVILEGES 和表属主与生产同形），
# 于是集群里根本没有 postgres 角色。而 settlement 有一条断言「aegis_app 执行
# SET ROLE postgres 必须被拒（42501）」——角色不存在时报的是 22023，那条越权
# 检查就变成了在验证一件无关的事。建一个空角色，断言才落回它要守的东西上。
psql_root -d pandora_gate_template -tAc "SELECT 1 FROM pg_roles WHERE rolname='postgres'" | grep -q 1   || psql_root -d pandora_gate_template -c "CREATE ROLE postgres NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS" >/dev/null

# 授权自查：代码里每一条 INSERT 写的列，运行时角色都得能写。
#
# 这类缺陷不会在开发机上暴露——那里连库的多半是超级用户——只在应用了
# 列级收窄的库上才炸，而且炸的是下单这种主路径。orders 就这么坏过：人工单
# 功能给它加了 manual_reason 和 created_by，checkout.go 开始写这两列，却
# 没人回头把它们加进 00036 的白名单。
#
# 检查从源码现扫而不是照一份维护的清单：清单本身就是会忘记更新的东西，
# 这个缺陷的成因正是「加了列忘了回头改另一处」。
#
# 也不看「列是否必填」——最初就是这么判的，结果两列都可空，正好漏过。
# 唯一站得住的判据是：代码写了它，就必须能写。
echo "==> 授权自查（源码 INSERT 列 vs 运行时授权）"
INSERT_COLS="$(cd "$PANEL_DIR" && python3 - <<'PY'
import re, os
pat = re.compile(r'INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)', re.I | re.S)
seen = set()
for root, _, files in os.walk('internal'):
    for fn in files:
        if not fn.endswith('.go') or fn.endswith('_test.go'):
            continue
        text = open(os.path.join(root, fn), encoding='utf-8', errors='replace').read()
        for table, cols in pat.findall(text):
            for col in cols.split(','):
                col = col.strip().strip('"').split()[0] if col.strip() else ''
                # 参数占位符和函数调用不是列名
                if re.fullmatch(r'[a-z_][a-z0-9_]*', col or ''):
                    seen.add((table, col))
for table, col in sorted(seen):
    print(table, col)
PY
)"
# 一条查询查完，不要在 shell 里循环调 psql：psql_root 走的是 docker exec -i，
# 它会把循环自己的 stdin 一起读掉，循环只跑一轮就结束——第一版就是这么
# 「只报出一个问题」的，而那一个还是假的。
VALUES_LIST="$(printf '%s\n' "$INSERT_COLS" | awk 'NF==2 {printf "%s(%c%s%c,%c%s%c)", sep, 39, $1, 39, 39, $2, 39; sep=","}')"
if [[ -n "$VALUES_LIST" ]]; then
  MISSING_GRANTS="$(psql_root -d pandora_gate_template -tAc "
    SELECT coalesce(string_agg(v.t||'.'||v.c, ', ' ORDER BY v.t, v.c), '')
      FROM (VALUES ${VALUES_LIST}) AS v(t,c)
     WHERE to_regclass('public.'||quote_ident(v.t)) IS NOT NULL
       -- 正则会从 SQL 文本里抓到一些并不是列的词，按「这张表上确实有这列」
       -- 过滤掉。漏检一个不存在的列没有代价，误报一个则会让整个 gate 变得
       -- 没人相信。
       AND EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema='public' AND table_name=v.t AND column_name=v.c)
       AND NOT has_column_privilege('aegis_app','public.'||quote_ident(v.t),v.c,'INSERT')")"
else
  MISSING_GRANTS=""
fi
if [[ -n "$MISSING_GRANTS" ]]; then
  echo "    代码会写、但 aegis_app 无权写的列:$MISSING_GRANTS" >&2
  echo "    这些插入会在全新部署的库里被拒绝。" >&2
  exit 1
fi
echo "    源码里的 INSERT 列全部有授权"

PASSED=(); FAILED=(); SKIPPED=()

for entry in "${DOMAINS[@]}"; do
  IFS='|' read -r NAME DB PKGS EXTRAS MARKER_TABLE COMMENT_TAG PREPARE RUNFILTER <<<"$entry"
  selected "$NAME" || continue

  echo "==> $NAME  ($DB)"
  psql_root -d postgres -c "CREATE DATABASE $DB TEMPLATE pandora_gate_template" >/dev/null
  psql_root -d postgres -c "REVOKE TEMPORARY ON DATABASE $DB FROM PUBLIC, aegis_app" >/dev/null

  # 域级准备动作。
  #
  # billing 那几个测试断言的是列级 ACL 与 forced RLS 的实际形状，光有 schema
  # 不够，必须先应用 configure-app-role.sql；它们的业务 fixture 也很重（租户、
  # 用户、套餐、价格、优惠券、账户、推荐关系……四百多行），过去内嵌在
  # test-checkout-atomic-00039-pg18.sh 的 heredoc 里只有那个 runner 能用。
  #
  # 那个 runner 自建网络、匿名卷和两个容器，在一台单核且跑着生产的机器上
  # 与自己抢资源，失败点在容器就绪前后随机漂移。改走这里的长驻容器 + 建库
  # 模式，它今天跑了十几次没出过环境问题。
  case ",$PREPARE," in *,app_role,*)
    docker exec -i -e PGPASSWORD="$PGPW" -e AEGIS_DB_APP_PASSWORD="$APPPW" "$CONTAINER"       psql -U "$PGUSER_MIGRATE" -d "$DB" -v ON_ERROR_STOP=1 -f -       < "$PANEL_DIR/deploy/configure-app-role.sql" >/dev/null ;;
  esac
  case ",$PREPARE," in *,idempotency_seed,*)
    docker exec -i -e PGPASSWORD="$PGPW" "$CONTAINER"       psql -U "$PGUSER_MIGRATE" -d "$DB" -v ON_ERROR_STOP=1 -f -       < "$PANEL_DIR/deploy/fixtures/idempotency-pg18-seed.sql" >/dev/null ;;
  esac
  case ",$PREPARE," in *,billing_seed,*)
    docker exec -i -e PGPASSWORD="$PGPW" "$CONTAINER"       psql -U "$PGUSER_MIGRATE" -d "$DB" -v ON_ERROR_STOP=1 -f -       < "$PANEL_DIR/deploy/fixtures/billing-pg18-seed.sql" >/dev/null ;;
  esac

  UP="$(echo "$NAME" | tr '[:lower:]' '[:upper:]')"
  ADMIN_DSN="postgres://${PGUSER_MIGRATE}:${PGPW}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
  APP_DSN="postgres://aegis_app:${APPPW}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
  RUN_ID="$(openssl rand -hex 16)"

  # 打上一次性 fixture 的标记。运行时角色只需要读，给 SELECT 就够——
  # 它对这张表有写权限的话，测试就等于自己给自己发通行证了。
  if [[ -n "$MARKER_TABLE" ]]; then
    # 三列一律建齐。有的域只查 run_id，有的还要集群 system identifier 和
    # 库 OID——那两样把标记钉死在这一个库的这一次生命周期上，dump 出来
    # 再灌到别处就对不上了。只查一列的域多两列无害，省掉一张按域分叉的表。
    MK_OID="$(psql_root -d "$DB" -tAc "SELECT oid FROM pg_database WHERE datname='${DB}'" | tr -d ' ')"
    MK_SID="$(psql_root -d "$DB" -tAc 'SELECT system_identifier FROM pg_control_system()' | tr -d ' ')"
    psql_root -d "$DB" \
      -c "CREATE TABLE public.${MARKER_TABLE}(run_id text PRIMARY KEY, system_identifier text NOT NULL, database_oid oid NOT NULL)" \
      -c "INSERT INTO public.${MARKER_TABLE}(run_id,system_identifier,database_oid) VALUES ('${RUN_ID}','${MK_SID}',${MK_OID})" \
      -c "GRANT SELECT ON public.${MARKER_TABLE} TO aegis_app" >/dev/null
  fi
  if [[ -n "$COMMENT_TAG" ]]; then
    psql_root -d postgres -c "COMMENT ON DATABASE $DB IS '${COMMENT_TAG}:${RUN_ID}'" >/dev/null
  fi

  ENVS=(
    "AEGIS_${UP}_PG18_FIXTURE=disposable-v1"
    "AEGIS_${UP}_PG18_DATABASE=${DB}"
    "AEGIS_${UP}_PG18_ADMIN_DSN=${ADMIN_DSN}"
    "AEGIS_${UP}_PG18_DSN=${APP_DSN}"
  )
  case ",$EXTRAS," in *,run_id,*) ENVS+=("AEGIS_${UP}_PG18_RUN_ID=${RUN_ID}") ;; esac
  case ",$EXTRAS," in *,oid,*)
    OID="$(psql_root -d "$DB" -tAc "SELECT oid FROM pg_database WHERE datname='${DB}'" | tr -d ' ')"
    ENVS+=("AEGIS_${UP}_PG18_DATABASE_OID=${OID}") ;;
  esac
  case ",$EXTRAS," in *,system_id,*)
    SID="$(psql_root -d "$DB" -tAc 'SELECT system_identifier FROM pg_control_system()' | tr -d ' ')"
    ENVS+=("AEGIS_${UP}_PG18_SYSTEM_ID=${SID}") ;;
  esac
  # billing 这个包里住着三个域，各用各的变量名，共用同一个库。
  case ",$EXTRAS," in *,billing,*)
    ENVS+=(
      "AEGIS_CHECKOUT_PG18_DSN=${APP_DSN}"
      "AEGIS_ORDER_RELEASE_PG18_DSN=${APP_DSN}"
      "AEGIS_ORDER_RELEASE_PG18_ADMIN_DSN=${ADMIN_DSN}"
      # settlement 还要求连接的 application_name 带上同一个 run ID。
      # 它是这套 fixture 护栏里最后一道：库名、标记、注释都可以在恢复一份
      # dump 时被一起带过来，而 application_name 只能由当次连接自己声明。
      "AEGIS_SETTLEMENT_PG18_DSN=${APP_DSN}&application_name=pandora-settlement-${RUN_ID}"
      # 超级用户连接：settlement 有一个子用例要预置「正常路径造不出来」的
      # 冲突源（同订单已存在佣金），得把触发器临时关掉才塞得进去。
      "AEGIS_SETTLEMENT_PG18_ADMIN_DSN=${ADMIN_DSN}"
      "AEGIS_SETTLEMENT_PG18_EXPECT_DB_NAME=${DB}"
      "AEGIS_SETTLEMENT_PG18_EXPECT_RUN_ID=${RUN_ID}"
    ) ;;
  esac

  # 结果判定：go test 退出 0 还不够。
  #
  # 这些测试缺环境变量时一律 t.Skip，而 go test 对跳过照样报 ok——环境
  # 接错一处，整个域就在绿灯下什么也没验。-run 一个都没匹配上同样是 ok。
  # 所以额外要求：没有任何 --- SKIP，且至少一个顶层 --- PASS。
  LOG="$LOG_DIR/$NAME.log"
  if env "${ENVS[@]}" GOMAXPROCS="${GOMAXPROCS:-1}" \
       timeout "${PANDORA_PG18_GATE_TIMEOUT:-600}" \
       go test -mod=readonly -p 1 -count=1 -v -run "${RUNFILTER:-PG18}" $PKGS 2>&1 \
       | tee "$LOG" | sed 's/^/    /'; then
    if grep -q -- '--- SKIP:' "$LOG"; then
      echo "    $NAME: 有用例被跳过，fixture 没接上" >&2
      SKIPPED+=("$NAME")
    elif ! grep -q '^--- PASS:' "$LOG"; then
      echo "    $NAME: 没有任何用例执行（测试过滤没匹配上）" >&2
      SKIPPED+=("$NAME")
    else
      PASSED+=("$NAME")
    fi
  else
    FAILED+=("$NAME")
  fi
done

echo
echo "================ 结果 ================"
[[ ${#PASSED[@]} -gt 0 ]] && printf '通过: %s\n' "${PASSED[*]}"
[[ ${#SKIPPED[@]} -gt 0 ]] && printf '跳过: %s\n' "${SKIPPED[*]}"
[[ ${#FAILED[@]} -gt 0 ]] && printf '失败: %s\n' "${FAILED[*]}"
# 跳过也是失败：门禁存在的意义就是真的连上库跑一遍。
if [[ ${#FAILED[@]} -gt 0 || ${#SKIPPED[@]} -gt 0 ]]; then
  exit 1
fi
echo "全部通过"
