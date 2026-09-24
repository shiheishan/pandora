-- 共享 IP 聚类的人工复核记录（M1）。
--
-- 聚类本身是 00026 的视图 audit_ip_clusters：近 90 天同一来源 IP 关联到多个
-- 账号。它只是线索——学校、公司、家庭出口下多人共用 IP 很正常。风控卡片要给
-- 管理员两个处置：「标记为正常，30 天内不再提示」与「禁用聚类内账号」，这两个
-- 结论都得落在某处，下一次打开时才知道这个聚类已经看过了。
--
-- 定位用 source_ip_hash 而不是明文 IP：视图没有稳定 id，明文 IP 又只以密文存在
-- 审计表里。哈希就是聚类的分组键，处置接口拿它的 hex 当 key，不需要解密。
--
-- 每个聚类只留一行当前结论（UNIQUE(tenant_id, source_ip_hash)），重复标记只刷新
-- 有效期；历史由审计 risk.ip_cluster.* 承担，这张表不做追加日志。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE ip_cluster_reviews (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  source_ip_hash bytea NOT NULL CHECK (octet_length(source_ip_hash) > 0),
  decision       text NOT NULL CHECK (decision IN ('normal', 'disabled')),
  note           text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
  decided_by     uuid,
  decided_at     timestamptz(6) NOT NULL DEFAULT now(),
  -- 「标记为正常」有有效期（到期后重新提示）；「已禁用」是终局，不过期
  expires_at     timestamptz(6),
  CONSTRAINT ip_cluster_reviews_tenant_ip_key UNIQUE (tenant_id, source_ip_hash),
  CONSTRAINT ip_cluster_reviews_decided_by_fk
    FOREIGN KEY (tenant_id, decided_by) REFERENCES users (tenant_id, id),
  CONSTRAINT ip_cluster_reviews_normal_expires
    CHECK (decision <> 'normal' OR expires_at IS NOT NULL)
);

SELECT app.enable_tenant_rls('ip_cluster_reviews');
GRANT SELECT, INSERT, UPDATE ON ip_cluster_reviews TO aegis_app;
REVOKE DELETE, TRUNCATE ON ip_cluster_reviews FROM aegis_app;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS ip_cluster_reviews;
-- +goose StatementEnd
