-- 工单快捷回复（M2）。
--
-- 客服回复框上方一排标签，点一下把常用话术填进输入框，不自动发送。
-- 它是租户级的共享配置，不分客服个人：同一句「请先重启客户端」
-- 没必要每个人各存一份，改一处全员生效才是要的效果。
--
-- 长度约束同时写在库里：标题是标签上的字，超过 20 个字就挤爆一行；
-- 正文上限与客服回复本身一致（5000），填进去之后能原样发出。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE ticket_macros (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 20),
  body       text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 5000),
  sort_order int NOT NULL DEFAULT 0,
  created_by uuid,
  created_at timestamptz(6) NOT NULL DEFAULT now(),
  updated_at timestamptz(6) NOT NULL DEFAULT now(),
  CONSTRAINT ticket_macros_created_by_fk
    FOREIGN KEY (tenant_id, created_by) REFERENCES users (tenant_id, id)
);

CREATE INDEX idx_ticket_macros_order ON ticket_macros (tenant_id, sort_order, created_at);

CREATE TRIGGER trg_ticket_macros_updated_at BEFORE UPDATE ON ticket_macros
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('ticket_macros');
GRANT SELECT, INSERT, UPDATE, DELETE ON ticket_macros TO aegis_app;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS ticket_macros;
-- +goose StatementEnd
