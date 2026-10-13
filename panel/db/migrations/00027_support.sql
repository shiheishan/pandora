-- +goose Up
-- support 模块：工单、工单消息、快捷回复。属主 support（docs/architecture.md 第 4.1 节）。
-- 依赖 00005_identity.sql 的 users、00013_order.sql 的 orders。
--
-- 与旧库的不同：
--   - 去掉租户列与 RLS；去掉没有写入方的 organization_id、related_subscription_id、related_node_id 与附件表。
--   - 关联订单在建单时把订单号一并存下（related_order_no）：订单号建单后不变，详情不再跨模块读订单表。
--   - 状态机守卫在 service 的转换表里（第 5.4 节）；库里只钉取值与各状态必须带的时间列。
--   - updated_at 由应用写（GORM autoUpdateTime 与 repo 的显式更新），不再挂 set_updated_at 触发器。
--   - 工单不删除；消息只追加（内部备注、系统消息都一样）。没有保留期，不建清理函数。

CREATE TABLE tickets (
  id                     uuid PRIMARY KEY DEFAULT uuidv7(),
  -- TK + 建单日期（UTC）+ 8 位 base32 随机串
  ticket_no              text NOT NULL CHECK (ticket_no ~ '^TK[0-9]{8}-[A-Z2-7]{8}$'),
  user_id                uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  subject                text NOT NULL CHECK (char_length(subject) BETWEEN 4 AND 120),
  category               text NOT NULL
                           CHECK (category IN ('general', 'billing', 'subscription', 'technical', 'account', 'abuse')),
  priority               text NOT NULL CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  status                 text NOT NULL DEFAULT 'open'
                           CHECK (status IN ('open', 'pending_user', 'pending_agent', 'escalated', 'resolved', 'closed')),
  -- 指派的管理员；是不是有效管理员由 service 经 auth 校验，库里只保证是个用户
  assigned_to            uuid REFERENCES users(id) ON DELETE SET NULL,
  related_order_id       uuid REFERENCES orders(id) ON DELETE RESTRICT,
  related_order_no       text CHECK (char_length(related_order_no) BETWEEN 1 AND 64),
  -- SLA：建单时按优先级算好截止时间，扫描任务只做「过期且未响应 → escalated」
  sla_first_response_due timestamptz NOT NULL,
  sla_resolution_due     timestamptz NOT NULL,
  first_responded_at     timestamptz,
  escalated_at           timestamptz,
  resolved_at            timestamptz,
  closed_at              timestamptz,
  -- 关闭原因分辨「用户关闭 / 撤回 / 客服关闭」，解决率统计靠它
  closed_reason          text CHECK (closed_reason IN ('user_closed', 'withdrawn', 'agent_closed')),
  closed_note            text CHECK (char_length(closed_note) <= 500),
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tickets_ticket_no_key UNIQUE (ticket_no),
  CONSTRAINT tickets_sla_order CHECK (sla_first_response_due <= sla_resolution_due),
  CONSTRAINT tickets_related_order_pair CHECK ((related_order_id IS NULL) = (related_order_no IS NULL)),
  -- 关闭态恰好带关闭时间与原因；重新打开时两者一起清掉
  CONSTRAINT tickets_closed_shape
    CHECK ((status = 'closed') = (closed_at IS NOT NULL) AND (status = 'closed') = (closed_reason IS NOT NULL)),
  CONSTRAINT tickets_closed_note_needs_closed CHECK (closed_note IS NULL OR status = 'closed'),
  CONSTRAINT tickets_resolved_has_time CHECK (status <> 'resolved' OR resolved_at IS NOT NULL),
  CONSTRAINT tickets_escalated_has_time CHECK (status <> 'escalated' OR escalated_at IS NOT NULL)
);
-- 门户「我的工单」按更新时间倒序
CREATE INDEX tickets_user_idx ON tickets (user_id, updated_at DESC);
-- 后台队列：按状态筛、升级优先、再按优先级与建单时间
CREATE INDEX tickets_queue_idx ON tickets (status, priority, created_at);
CREATE INDEX tickets_assigned_idx ON tickets (assigned_to) WHERE assigned_to IS NOT NULL;
CREATE INDEX tickets_order_idx ON tickets (related_order_id) WHERE related_order_id IS NOT NULL;
-- SLA 扫描：只看还没首次响应的
CREATE INDEX tickets_sla_idx ON tickets (sla_first_response_due) WHERE first_responded_at IS NULL;

-- 工单消息：只追加。内部备注只能由客服或系统写（用户读取路径在 SQL 里排除内部备注）。
CREATE TABLE ticket_messages (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  ticket_id     uuid NOT NULL REFERENCES tickets(id) ON DELETE RESTRICT,
  -- 作者：user、agent 必有，system 没有
  author_id     uuid REFERENCES users(id) ON DELETE RESTRICT,
  author_kind   text NOT NULL CHECK (author_kind IN ('user', 'agent', 'system')),
  body          text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 5000),
  internal_note boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ticket_messages_author_shape CHECK ((author_kind = 'system') = (author_id IS NULL)),
  CONSTRAINT ticket_messages_internal_only_from_staff CHECK (NOT internal_note OR author_kind IN ('agent', 'system'))
);
CREATE INDEX ticket_messages_ticket_idx ON ticket_messages (ticket_id, created_at, id);
CREATE INDEX ticket_messages_author_idx ON ticket_messages (author_id) WHERE author_id IS NOT NULL;

-- 快捷回复：后台共享的话术。标题是回复框上方标签上的字，超过 20 个字挤爆一行；正文上限与回复一致。
CREATE TABLE ticket_macros (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 20),
  body       text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 5000),
  sort_order int NOT NULL DEFAULT 0,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_macros_order_idx ON ticket_macros (sort_order, created_at, id);

-- 工单不删除（语句级，连 TRUNCATE 一起挡）；消息只追加
CREATE TRIGGER tickets_deny_delete
  BEFORE DELETE ON tickets FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();
CREATE TRIGGER tickets_deny_truncate
  BEFORE TRUNCATE ON tickets FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();
CREATE TRIGGER ticket_messages_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON ticket_messages
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- +goose Down
DROP TRIGGER IF EXISTS ticket_messages_append_only ON ticket_messages;
DROP TRIGGER IF EXISTS tickets_deny_truncate ON tickets;
DROP TRIGGER IF EXISTS tickets_deny_delete ON tickets;
DROP TABLE IF EXISTS ticket_macros;
DROP TABLE IF EXISTS ticket_messages;
DROP TABLE IF EXISTS tickets;
