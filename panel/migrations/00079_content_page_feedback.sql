-- 帮助文章「有帮助」反馈（M5）。
--
-- 按（文章、版本、用户）一行：同一个人对同一版改主意就覆盖，不累计；
-- 文章出了新版本，旧反馈留在旧版本上，新版本从零开始——改写正文之后，
-- 旧版的「没帮助」不该继续算在新版头上。
--
-- 外键指向 content_pages 的 (tenant_id, slug, version) 唯一键：反馈的版本必须
-- 真实存在，内容页被级联删除时反馈跟着走。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE content_page_feedback (
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  page_slug    citext NOT NULL,
  page_version int NOT NULL,
  user_id      uuid NOT NULL,
  helpful      boolean NOT NULL,
  created_at   timestamptz(6) NOT NULL DEFAULT now(),
  updated_at   timestamptz(6) NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, page_slug, page_version, user_id),
  CONSTRAINT content_page_feedback_page_fk
    FOREIGN KEY (tenant_id, page_slug, page_version)
    REFERENCES content_pages (tenant_id, slug, version) ON DELETE CASCADE,
  CONSTRAINT content_page_feedback_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);

CREATE TRIGGER trg_content_page_feedback_updated_at BEFORE UPDATE ON content_page_feedback
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('content_page_feedback');
GRANT SELECT, INSERT, UPDATE ON content_page_feedback TO aegis_app;
REVOKE DELETE, TRUNCATE ON content_page_feedback FROM aegis_app;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS content_page_feedback;
-- +goose StatementEnd
