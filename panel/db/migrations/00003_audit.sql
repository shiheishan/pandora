-- +goose Up
-- audit 模块：审计事件（只追加）与保留期清理函数。
--
-- 与旧表相比去掉了租户列、哈希链列、审批单与 ASN 列；actor_id 不对用户表建外键：
-- audit 不依赖任何业务模块，用户删除后审计照样保留。
-- 来源 IP 两份：ip_hash 是 HMAC（子密钥 pandora/hmac/ip/v1），用于按 IP 关联；
-- ip_enc 是封存的明文，后台查看时解开。
CREATE TABLE audit_events (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  -- user / admin：有操作者；system：后台任务与命令行；anonymous：未登录的 HTTP 请求
  actor_kind     text NOT NULL CHECK (actor_kind IN ('user', 'admin', 'system', 'anonymous')),
  actor_id       uuid,
  -- 操作者就是本次请求的登录主体时，记主体的 audience 与认证强度
  actor_audience text CHECK (actor_audience IN ('public', 'admin')),
  auth_context   text CHECK (auth_context IN ('session', 'reauth')),
  action         text NOT NULL CHECK (length(action) BETWEEN 1 AND 100),
  resource_type  text CHECK (length(resource_type) BETWEEN 1 AND 64),
  resource_id    uuid,
  outcome        text NOT NULL DEFAULT 'success'
                   CHECK (outcome IN ('success', 'failure', 'denied', 'partial')),
  data           jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object'),
  request_id     text CHECK (length(request_id) <= 64),
  ip_hash        bytea,
  ip_enc         bytea,
  user_agent     text CHECK (length(user_agent) <= 512),
  CONSTRAINT audit_events_actor_present
    CHECK ((actor_kind IN ('system', 'anonymous')) = (actor_id IS NULL)),
  CONSTRAINT audit_events_audience_needs_actor
    CHECK (actor_audience IS NULL OR actor_id IS NOT NULL),
  CONSTRAINT audit_events_auth_needs_audience
    CHECK (auth_context IS NULL OR actor_audience IS NOT NULL)
);

-- 后台列表与导出的排序（occurred_at DESC, id DESC），保留期清理按时间删
CREATE INDEX audit_events_time_idx ON audit_events (occurred_at DESC, id DESC);
-- 按操作者翻（搜索框按邮箱命中的操作者、用户画像）
CREATE INDEX audit_events_actor_idx ON audit_events (actor_id, occurred_at DESC)
  WHERE actor_id IS NOT NULL;
-- 动作前缀筛选（LIKE 'order.%'）
CREATE INDEX audit_events_action_idx ON audit_events (action text_pattern_ops, occurred_at DESC);
-- 某个对象的审计（搜索框按对象 id 精确匹配）
CREATE INDEX audit_events_resource_idx ON audit_events (resource_id, occurred_at DESC)
  WHERE resource_id IS NOT NULL;

-- 只追加：应用不能改、不能删；清理只走下面的函数
CREATE TRIGGER audit_events_append_only
  BEFORE UPDATE OR DELETE ON audit_events
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 保留期清理：删 p_before 之前的记录，每次最多 p_batch_limit 行，由定时任务循环调用。
-- 截止时间不晚于 400 天前（产品定的保留期）：调用方传更晚的时间也只删 400 天以前的，
-- 不能借它删近期记录。SECURITY DEFINER 以迁移角色执行；session_replication_role = replica
-- 只在本函数执行期间让追加写触发器不触发，对其它语句照旧生效。
-- +goose StatementBegin
CREATE FUNCTION purge_audit_events(p_before timestamptz, p_batch_limit int)
RETURNS int
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
SET session_replication_role = replica
AS $$
DECLARE
  v_cutoff  timestamptz;
  v_deleted int;
BEGIN
  IF p_before IS NULL THEN
    RAISE EXCEPTION '截止时间不能为空' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 10000 THEN
    RAISE EXCEPTION '每批删除行数必须在 1 到 10000 之间' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  v_cutoff := least(p_before, now() - interval '400 days');

  DELETE FROM public.audit_events a
   USING (SELECT id
            FROM public.audit_events
           WHERE occurred_at < v_cutoff
           ORDER BY occurred_at
           LIMIT p_batch_limit) d
   WHERE a.id = d.id;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;
-- +goose StatementEnd

-- 运行角色的 EXECUTE 由部署时的角色配置授予（运行角色不是本函数的属主）
REVOKE ALL ON FUNCTION purge_audit_events(timestamptz, int) FROM PUBLIC;

-- +goose Down
DROP FUNCTION IF EXISTS purge_audit_events(timestamptz, int);
DROP TABLE IF EXISTS audit_events;
