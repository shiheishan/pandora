-- +goose Up
-- 报表展示调整与复式账本严格隔离。它只影响管理报表，不改变订单、支付、
-- ledger_transactions 或 ledger_entries。撤销通过追加一条反向记录完成。
-- +goose StatementBegin
CREATE TABLE revenue_report_adjustments (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  currency      app.currency_code NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount        app.minor_amount NOT NULL CHECK (amount <> 0 AND amount BETWEEN -1000000000000 AND 1000000000000),
  reason        text NOT NULL CHECK (length(btrim(reason)) BETWEEN 5 AND 500),
  -- 是否属于未来日期由服务按 tenant.timezone 判定，不能用数据库会话时区。
  effective_on  date NOT NULL,
  reversal_of   uuid,
  created_by    uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT revenue_report_adjustments_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT revenue_adjustments_creator_fk
    FOREIGN KEY (tenant_id, created_by)
    REFERENCES users (tenant_id, id) ON DELETE RESTRICT,

  CONSTRAINT revenue_adjustments_reversal_fk
    FOREIGN KEY (tenant_id, reversal_of)
    REFERENCES revenue_report_adjustments (tenant_id, id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX revenue_report_adjustments_one_reversal
  ON revenue_report_adjustments (tenant_id, reversal_of)
  WHERE reversal_of IS NOT NULL;
CREATE UNIQUE INDEX revenue_report_adjustments_idempotency
  ON revenue_report_adjustments (tenant_id, idempotency_key);
CREATE INDEX revenue_report_adjustments_series
  ON revenue_report_adjustments (tenant_id, currency, effective_on, created_at);

SELECT app.enable_tenant_rls('revenue_report_adjustments');
SELECT app.make_append_only('revenue_report_adjustments');

-- 迁移预演或第三方托管数据库可能尚未创建运行时角色。角色存在时立即
-- 收紧权限；不存在时由 deploy/configure-app-role.sql 在 bootstrap 阶段处理。
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'aegis_app') THEN
    REVOKE UPDATE, DELETE, TRUNCATE ON revenue_report_adjustments FROM aegis_app;
  END IF;
END $$;

COMMENT ON TABLE revenue_report_adjustments IS
  '仅用于管理报表展示的追加式调整；不属于账本真相，不得更新或删除。';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS revenue_report_adjustments;
-- +goose StatementEnd
