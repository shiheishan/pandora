-- 同机端口门禁的数据库兜底，与节点真实运行状态（w4deliver）。
--
-- 一、listen_l4：节点入站实际 bind 的 L4 协议（tcp / udp），由 node_type 与 protocol_config
--    推出的生成列。规则与 Go 侧 nodefabric.ListenL4 逐条相同（PG18 用例逐项对照），也与 pdnd
--    kernel/port_claims.go 的 inboundPortKey 同口径（面板单测读 pdnd 的用例表对照）：
--      - hysteria2 / hy2 / tuic / juicity 走 UDP；
--      - vless / vmess 的 network 为 xhttp-h3 或 mKCP（mkcp / kcp / m-kcp）走 UDP；
--      - trojan 的 network 为 mKCP 走 UDP；
--      - shadowsocks / ss 只有 network=udp 走 UDP；
--      - mieru 看 transport（大小写不敏感，缺省 TCP）；
--      - 其余（anytls、socks、http、naive、shadowtls 与上述的 TCP 传输）走 TCP。
--    同一个端口号上 TCP 与 UDP 可以各被一个节点占用（如 REALITY 走 TCP、hy2 走 UDP）。
--    STORED：PG18 的虚拟生成列不能建索引。心跳不写 node_type / protocol_config，重算出的值
--    不变，心跳仍是 HOT 更新（PG18 用例 heartbeat is a HOT update 照旧守着）。
--
-- 二、唯一部分索引 (tenant_id, server_id, server_port, listen_l4) WHERE 未退役：后台建、改、
--    克隆、迁移节点时在服务器行锁内先查（nodefabric.checkNodePortClaim，冲突回 409 并写明占用
--    者），索引兜住任何绕过那道检查的写。「未退役」与全仓统一判定一致：生命周期不是
--    retired / destroyed，且服务状态不是 retired。
--    存量里可能已有同机同端口同 L4 的节点（门禁上线前没人拦）。迁移不能因此失败：先查，
--    有冲突就不建唯一索引，改建同列的普通索引（后台查冲突照样走索引），并以 WARNING 列出
--    冲突清单（租户、服务器、端口/L4、节点 id）。后台节点列表对这些节点给出「端口与某节点
--    冲突」的提示（port_conflict_node）；管理员改掉端口或退役其一之后，下一个迁移（或手工
--    执行下方注释里的语句）再建唯一索引。Go 侧门禁在两种情况下行为相同。
--
-- 三、运行状态：pdnd 在 degraded 时经请求头上报机器可读原因（签名心跳的 X-Node-Runtime-Reason，
--    兼容通道 /status 的 X-Node-Runtime-Status 与 X-Node-Runtime-Reason）。
--      runtime_status    running / degraded；老节点不报时为 NULL
--      runtime_reason    原因码，如 port_in_use:443/tcp:<节点 id|other>、not_started、
--                        config_apply_failed、serving_cached_config；≤200 字节可打印 ASCII
--      runtime_state_at  状态或原因上一次变化的时刻（没变不改写）
--    三列都进变更通知的列清单：只在状态或原因真变化时才通知，后台与订阅缓存随之刷新；
--    心跳每次都写同样的值，不触发通知、不破坏 HOT。
--
-- 四、zz_notify_nodes_update 的列清单（00116）补上四个新列。PG18 用例按 information_schema
--    对照，漏列即红。

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.nodes
  ADD COLUMN listen_l4 text GENERATED ALWAYS AS (
    CASE
      WHEN lower(btrim(node_type)) IN ('hysteria2', 'hy2', 'tuic', 'juicity') THEN 'udp'
      WHEN lower(btrim(node_type)) IN ('vless', 'vmess')
       AND lower(btrim(protocol_config ->> 'network')) IN ('xhttp-h3', 'mkcp', 'kcp', 'm-kcp') THEN 'udp'
      WHEN lower(btrim(node_type)) = 'trojan'
       AND lower(btrim(protocol_config ->> 'network')) IN ('mkcp', 'kcp', 'm-kcp') THEN 'udp'
      WHEN lower(btrim(node_type)) IN ('shadowsocks', 'ss')
       AND lower(btrim(protocol_config ->> 'network')) = 'udp' THEN 'udp'
      WHEN lower(btrim(node_type)) = 'mieru'
       AND lower(protocol_config ->> 'transport') = 'udp' THEN 'udp'
      ELSE 'tcp'
    END
  ) STORED,
  ADD COLUMN runtime_status text
    CONSTRAINT nodes_runtime_status_check CHECK (runtime_status IN ('running', 'degraded')),
  ADD COLUMN runtime_reason text
    CONSTRAINT nodes_runtime_reason_check CHECK (octet_length(runtime_reason) <= 200),
  ADD COLUMN runtime_state_at timestamptz;

COMMENT ON COLUMN public.nodes.listen_l4 IS
  '入站实际 bind 的 L4（tcp/udp），与 nodefabric.ListenL4、pdnd inboundPortKey 同口径；同机端口门禁的键之一';
COMMENT ON COLUMN public.nodes.runtime_reason IS
  'pdnd 上报的降级原因码（port_in_use:<端口>/<tcp|udp>:<节点 id|other>、not_started、config_apply_failed、serving_cached_config）';

-- +goose StatementBegin
DO $$
DECLARE
  v_conflicts text;
BEGIN
  SELECT string_agg(format('tenant=%s server=%s port=%s/%s nodes=[%s]',
                           c.tenant_id, c.server_id, c.server_port, c.listen_l4, c.node_ids), '; ')
    INTO v_conflicts
    FROM (SELECT tenant_id, server_id, server_port, listen_l4,
                 string_agg(id::text, ',' ORDER BY created_at, id) AS node_ids
            FROM public.nodes
           WHERE server_id IS NOT NULL AND server_port IS NOT NULL
             AND status NOT IN ('retired', 'destroyed') AND serving_status <> 'retired'
           GROUP BY tenant_id, server_id, server_port, listen_l4
          HAVING count(*) > 1) c;
  IF v_conflicts IS NULL THEN
    CREATE UNIQUE INDEX nodes_listen_claim_unique
      ON public.nodes (tenant_id, server_id, server_port, listen_l4)
      WHERE status NOT IN ('retired', 'destroyed') AND serving_status <> 'retired';
  ELSE
    -- 存量冲突：不让迁移失败。建同列普通索引供门禁与后台提示查找；冲突清理完后执行
    --   DROP INDEX public.nodes_listen_claim_lookup;
    --   CREATE UNIQUE INDEX nodes_listen_claim_unique ON public.nodes
    --     (tenant_id, server_id, server_port, listen_l4)
    --     WHERE status NOT IN ('retired', 'destroyed') AND serving_status <> 'retired';
    RAISE WARNING 'nodes: existing same-server port claims, unique index nodes_listen_claim_unique NOT created; resolve in the admin node list then create it: %', v_conflicts;
    CREATE INDEX nodes_listen_claim_lookup
      ON public.nodes (tenant_id, server_id, server_port, listen_l4)
      WHERE status NOT IN ('retired', 'destroyed') AND serving_status <> 'retired';
  END IF;
END $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS zz_notify_nodes_update ON public.nodes;

CREATE TRIGGER zz_notify_nodes_update
  AFTER UPDATE ON public.nodes
  FOR EACH ROW
  WHEN (
    ROW(
       OLD.id, OLD.tenant_id, OLD.name, OLD.pool_id, OLD.template_id, OLD.provider_id,
       OLD.provider_resource_id, OLD.region, OLD.availability_zone, OLD.status,
       OLD.public_ipv4, OLD.public_ipv6, OLD.private_ipv4, OLD.hostname,
       OLD.desired_config_version, OLD.hard_fault, OLD.hard_fault_reason, OLD.weight,
       OLD.canary_group, OLD.cost_center, OLD.monthly_cost, OLD.cost_currency,
       OLD.entered_status_at, OLD.retired_at, OLD.destroyed_at, OLD.created_at, OLD.node_type,
       OLD.server_host, OLD.server_port, OLD.protocol_config, OLD.traffic_rate,
       OLD.server_token_hash, OLD.display_name, OLD.sort_order, OLD.kernel, OLD.server_id,
       OLD.serving_status, OLD.protocol_schema_version, OLD.config_validated_at,
       OLD.row_version, OLD.config_source_generation, OLD.desired_effective_release_id,
       OLD.desired_effective_generation, OLD.node_no, OLD.silenced_by_server_delete,
       OLD.country_code, OLD.server_token_issued_at, OLD.server_token_issued_by,
       OLD.listen_l4, OLD.runtime_status, OLD.runtime_reason, OLD.runtime_state_at)
    IS DISTINCT FROM
    ROW(
       NEW.id, NEW.tenant_id, NEW.name, NEW.pool_id, NEW.template_id, NEW.provider_id,
       NEW.provider_resource_id, NEW.region, NEW.availability_zone, NEW.status,
       NEW.public_ipv4, NEW.public_ipv6, NEW.private_ipv4, NEW.hostname,
       NEW.desired_config_version, NEW.hard_fault, NEW.hard_fault_reason, NEW.weight,
       NEW.canary_group, NEW.cost_center, NEW.monthly_cost, NEW.cost_currency,
       NEW.entered_status_at, NEW.retired_at, NEW.destroyed_at, NEW.created_at, NEW.node_type,
       NEW.server_host, NEW.server_port, NEW.protocol_config, NEW.traffic_rate,
       NEW.server_token_hash, NEW.display_name, NEW.sort_order, NEW.kernel, NEW.server_id,
       NEW.serving_status, NEW.protocol_schema_version, NEW.config_validated_at,
       NEW.row_version, NEW.config_source_generation, NEW.desired_effective_release_id,
       NEW.desired_effective_generation, NEW.node_no, NEW.silenced_by_server_delete,
       NEW.country_code, NEW.server_token_issued_at, NEW.server_token_issued_by,
       NEW.listen_l4, NEW.runtime_status, NEW.runtime_reason, NEW.runtime_state_at)
    OR (NEW.last_heartbeat_at IS NOT NULL
        AND (OLD.last_heartbeat_at IS NULL
             OR NEW.last_heartbeat_at - OLD.last_heartbeat_at >= interval '90 seconds'))
  )
  EXECUTE FUNCTION app.notify_change();

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS zz_notify_nodes_update ON public.nodes;

CREATE TRIGGER zz_notify_nodes_update
  AFTER UPDATE ON public.nodes
  FOR EACH ROW
  WHEN (
    ROW(
       OLD.id, OLD.tenant_id, OLD.name, OLD.pool_id, OLD.template_id, OLD.provider_id,
       OLD.provider_resource_id, OLD.region, OLD.availability_zone, OLD.status,
       OLD.public_ipv4, OLD.public_ipv6, OLD.private_ipv4, OLD.hostname,
       OLD.desired_config_version, OLD.hard_fault, OLD.hard_fault_reason, OLD.weight,
       OLD.canary_group, OLD.cost_center, OLD.monthly_cost, OLD.cost_currency,
       OLD.entered_status_at, OLD.retired_at, OLD.destroyed_at, OLD.created_at, OLD.node_type,
       OLD.server_host, OLD.server_port, OLD.protocol_config, OLD.traffic_rate,
       OLD.server_token_hash, OLD.display_name, OLD.sort_order, OLD.kernel, OLD.server_id,
       OLD.serving_status, OLD.protocol_schema_version, OLD.config_validated_at,
       OLD.row_version, OLD.config_source_generation, OLD.desired_effective_release_id,
       OLD.desired_effective_generation, OLD.node_no, OLD.silenced_by_server_delete,
       OLD.country_code, OLD.server_token_issued_at, OLD.server_token_issued_by)
    IS DISTINCT FROM
    ROW(
       NEW.id, NEW.tenant_id, NEW.name, NEW.pool_id, NEW.template_id, NEW.provider_id,
       NEW.provider_resource_id, NEW.region, NEW.availability_zone, NEW.status,
       NEW.public_ipv4, NEW.public_ipv6, NEW.private_ipv4, NEW.hostname,
       NEW.desired_config_version, NEW.hard_fault, NEW.hard_fault_reason, NEW.weight,
       NEW.canary_group, NEW.cost_center, NEW.monthly_cost, NEW.cost_currency,
       NEW.entered_status_at, NEW.retired_at, NEW.destroyed_at, NEW.created_at, NEW.node_type,
       NEW.server_host, NEW.server_port, NEW.protocol_config, NEW.traffic_rate,
       NEW.server_token_hash, NEW.display_name, NEW.sort_order, NEW.kernel, NEW.server_id,
       NEW.serving_status, NEW.protocol_schema_version, NEW.config_validated_at,
       NEW.row_version, NEW.config_source_generation, NEW.desired_effective_release_id,
       NEW.desired_effective_generation, NEW.node_no, NEW.silenced_by_server_delete,
       NEW.country_code, NEW.server_token_issued_at, NEW.server_token_issued_by)
    OR (NEW.last_heartbeat_at IS NOT NULL
        AND (OLD.last_heartbeat_at IS NULL
             OR NEW.last_heartbeat_at - OLD.last_heartbeat_at >= interval '90 seconds'))
  )
  EXECUTE FUNCTION app.notify_change();

DROP INDEX IF EXISTS public.nodes_listen_claim_unique;
DROP INDEX IF EXISTS public.nodes_listen_claim_lookup;

ALTER TABLE public.nodes
  DROP COLUMN IF EXISTS runtime_state_at,
  DROP COLUMN IF EXISTS runtime_reason,
  DROP COLUMN IF EXISTS runtime_status,
  DROP COLUMN IF EXISTS listen_l4;
