-- 节点池限定用户组（D-B-3 方案 C，契约 R104）。
--
-- 用户能用某个节点池，要同时满足：订阅的套餐版本绑定了这个池（plan_node_pools），
-- 并且这个池没有限定用户组，或者用户所在的组在名单里。名单就是这张表：
-- 一个池在这里一行都没有 = 不限定；有行 = 只有这些组的用户能用，默认组
-- （users.user_group_id 为空）的用户一律用不了。
--
-- 为什么是一张关联表而不是 node_pools 上的数组列或 user_groups.policy：
-- 两头都要复合外键钉在同一个租户里，删组时要能被数据库挡住——名单被悄悄
-- 删空会让池对所有人开放，这正是限定要防的越权。数组元素上挂不了外键。
--
-- 外键两头的删除语义不同：
--   池被删，它的名单跟着删（CASCADE）——池都没了，名单没有意义；
--   组被删，只要还有池的名单引用它就拒绝（NO ACTION）。处理器先查一遍给出
--   写明池名的 409，这里是并发下的最后一道。用 NO ACTION 而不是 RESTRICT：
--   删租户时 user_groups 与本表在同一条语句里级联删除，NO ACTION 在语句末
--   检查，不会被自己的级联顺序卡住。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE node_pool_user_groups (
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  pool_id       uuid NOT NULL,
  user_group_id uuid NOT NULL,
  created_at    timestamptz(6) NOT NULL DEFAULT now(),
  PRIMARY KEY (pool_id, user_group_id),
  CONSTRAINT node_pool_user_groups_pool_fk
    FOREIGN KEY (tenant_id, pool_id) REFERENCES node_pools (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT node_pool_user_groups_group_fk
    FOREIGN KEY (tenant_id, user_group_id) REFERENCES user_groups (tenant_id, id)
);

-- 下发判定按 (租户, 池) 查名单，删组与用户组列表按 (租户, 组) 反查
CREATE INDEX idx_node_pool_user_groups_pool ON node_pool_user_groups (tenant_id, pool_id);
CREATE INDEX idx_node_pool_user_groups_group ON node_pool_user_groups (tenant_id, user_group_id);

SELECT app.enable_tenant_rls('node_pool_user_groups');
-- 名单只整体替换（删旧插新），没有 UPDATE
GRANT SELECT, INSERT, DELETE ON node_pool_user_groups TO aegis_app;

COMMENT ON TABLE node_pool_user_groups IS
  'R104：节点池的用户组限定名单。池在此无行 = 不限定；有行 = 只有名单内组的用户能用，默认组用户不能用。下发三处（节点用户列表、订阅下载、门户预览）共用 nodefabric.PoolAdmitsUserSQL。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS node_pool_user_groups;
-- +goose StatementEnd
