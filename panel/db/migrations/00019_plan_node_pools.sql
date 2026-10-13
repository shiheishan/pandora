-- 套餐版本绑定的节点池：属主 catalog（版本快照的一部分），排在 node 的迁移之后是因为要引用 node_pools。
-- 订阅按开通时的 plan_version_id 取线路，所以只有草稿版本能改绑池；已发布版本的绑池随版本冻结，
-- 与权益、配额定义用同一个冻结守卫（00008 的 catalog_guard_version_child）。
-- 被已发布版本引用的池删不掉（外键 RESTRICT）。

-- +goose Up

CREATE TABLE plan_node_pools (
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id) ON DELETE CASCADE,
  pool_id         uuid NOT NULL REFERENCES node_pools(id) ON DELETE RESTRICT,
  PRIMARY KEY (plan_version_id, pool_id)
);

CREATE INDEX plan_node_pools_pool_idx ON plan_node_pools (pool_id);

CREATE TRIGGER plan_node_pools_guard
  BEFORE INSERT OR UPDATE OR DELETE ON plan_node_pools
  FOR EACH ROW EXECUTE FUNCTION catalog_guard_version_child();

-- +goose Down

DROP TABLE plan_node_pools;
