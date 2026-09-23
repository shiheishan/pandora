-- +goose Up

-- 流量重置日志（对标 Xboard traffic-reset）。
--
-- 重置这件事以前发生得静悄悄：续费重置、周期滚动、礼品卡清零、
-- 管理员手动重置，四条路都直接改 quota_balances.consumed，改完不留痕。
-- 结果是用户问「我的流量怎么突然回来了/没了」时无从查起，
-- 客服只能猜。
--
-- 这张表就是那个痕迹。它是追加写的：重置记录一旦写下就不该被修改，
-- 否则「谁在什么时候把谁的流量清了」这件事本身就不可信。

CREATE TABLE traffic_reset_logs (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  metric          text NOT NULL,
  -- 为什么重置。四条来路各自的含义：
  --   renewal      续费时按套餐重新计量
  --   cycle_roll   按日/按月配额到点自动滚动
  --   manual       管理员在后台点的
  --   gift_card    用户兑换了带「重置流量」的礼品卡
  reason          text NOT NULL
                  CHECK (reason IN ('renewal','cycle_roll','manual','gift_card')),
  -- 重置前后的已用量。留 before 是为了能回答「我到底用了多少」——
  -- 只记 after 的话，重置完就再也查不到那个数字了。
  consumed_before bigint NOT NULL,
  consumed_after  bigint NOT NULL DEFAULT 0,
  -- manual 必须有操作人；其它来路是系统行为，留空
  actor_id        uuid REFERENCES users(id),
  note            text CHECK (note IS NULL OR length(note) <= 500),
  created_at      timestamptz(6) NOT NULL DEFAULT now(),
  CHECK (reason <> 'manual' OR actor_id IS NOT NULL)
);

CREATE INDEX idx_traffic_reset_logs_user
  ON traffic_reset_logs (tenant_id, user_id, created_at DESC);
CREATE INDEX idx_traffic_reset_logs_sub
  ON traffic_reset_logs (tenant_id, subscription_id, created_at DESC);
CREATE INDEX idx_traffic_reset_logs_reason
  ON traffic_reset_logs (tenant_id, reason, created_at DESC);

ALTER TABLE traffic_reset_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY traffic_reset_logs_tenant ON traffic_reset_logs
  USING (tenant_id = app.current_tenant_id());
GRANT SELECT, INSERT ON traffic_reset_logs TO aegis_app;

-- +goose StatementBegin
CREATE TRIGGER traffic_reset_logs_append_only
  BEFORE UPDATE OR DELETE ON traffic_reset_logs
  FOR EACH ROW EXECUTE FUNCTION app.deny_mutation();
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES ('metering.reset.read',  'metering', '查看流量重置记录与统计', false),
       ('metering.reset.write', 'metering', '手动重置用户流量', true)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
  FROM roles r
 CROSS JOIN (VALUES ('metering.reset.read'), ('metering.reset.write')) AS p(code)
 WHERE r.is_system AND r.code IN ('tenant_admin', 'platform_admin')
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER IF EXISTS traffic_reset_logs_append_only ON traffic_reset_logs;
DROP TABLE IF EXISTS traffic_reset_logs;
DELETE FROM role_permissions
 WHERE permission_code IN ('metering.reset.read','metering.reset.write');
DELETE FROM permissions
 WHERE code IN ('metering.reset.read','metering.reset.write');
