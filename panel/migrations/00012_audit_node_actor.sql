-- 审计主体增加 node。
--
-- 起因：Node Agent 引导接入时写审计，撞上 audit_events_actor_kind_check。
--
-- 为什么不复用已有的 'agent'：那个值在本项目里指的是客服（ticket_messages
-- 用 author_kind='agent' 表示客服回复），两者混用会让「谁做的」这一列
-- 失去区分力 —— 而审计表存在的全部意义就是回答这个问题（SEC-012）。
-- PRD 2.3 的角色矩阵也把 Node Agent 列为独立主体。

-- +goose Up

-- +goose StatementBegin
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_actor_kind_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_actor_kind_check
  CHECK (actor_kind IN ('user', 'admin', 'system', 'agent', 'node', 'plugin', 'anonymous'));

COMMENT ON COLUMN audit_events.actor_kind IS
  'agent = 客服人员；node = Node Agent 进程。两者是不同主体，不可混用。';
-- +goose StatementEnd

-- +goose Down
-- 回到 00011：CHECK 去掉 node，撤掉这一版加的列注释。
-- 审计是追加写证据，不删已有的 node 审计行（语句级触发器也会拒绝）；有这类行时旧 CHECK
-- 加不回去，拒绝回滚，需要先人工核对。没有就照常回退。
SET LOCAL lock_timeout = '5s';
-- 加回 CHECK 要扫一遍审计表，给足时间
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM audit_events WHERE actor_kind = 'node') THEN
    RAISE EXCEPTION '00012 Down refused: audit events by node actors exist and audit evidence is append-only';
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_actor_kind_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_actor_kind_check
  CHECK (actor_kind IN ('user', 'admin', 'system', 'agent', 'plugin', 'anonymous'));
COMMENT ON COLUMN audit_events.actor_kind IS NULL;
