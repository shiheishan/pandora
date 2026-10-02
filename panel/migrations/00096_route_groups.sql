-- 路由组（NODE-012 的第三个范围）。
--
-- 00017 只有两个范围：全局（node_id 为空）与节点私有。真实运营里常见的是
-- 「一批节点共用一套分流」——香港几台走流媒体解锁、日本几台走中转——放全局
-- 会误伤别的节点，逐节点复制又会在改的时候漏掉几台。路由组就是这一层：
-- 运营建若干有名字的组，每组有自己的出站与规则，再把节点分进组里。
--
-- 范围从此是三选一：全局（两列都空）/ 组（group_id）/ 节点（node_id）。
-- 不新建两张「组出站 / 组规则」表，而是在原表上加 group_id：生效合并、
-- 引用校验与后台编辑器对三个范围是同一套读写，分三张表只会把同一份逻辑复制三遍。
--
-- 成员关系：一个节点可以在多个组里，组之间按 route_groups.sort_order 生效
-- （规则先排的组先匹配，出站同 tag 时先排的组胜出）。顺序挂在组上而不是成员行上，
-- 所有节点看到的组间顺序一致，排查时不必逐节点看。
--
-- 删组：组内出站与规则、成员行都随组级联删除；成员节点的 generation 由
-- 应用在同一事务里推进并在提交后通知（与全局发布同一做法），这里不放触发器——
-- 推进 generation 必须与发布锁、审计在同一处编排。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE route_groups (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name        text NOT NULL,
  description text NOT NULL DEFAULT '',
  -- 组间生效顺序：小的先生效，同值按名称再按 id 定序
  sort_order  int  NOT NULL DEFAULT 0,
  -- 组的元信息、出站与规则、成员任一变化都推进它，后台写接口拿它做乐观并发
  row_version bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT route_groups_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT route_groups_name_len CHECK (char_length(btrim(name)) BETWEEN 1 AND 64 AND name = btrim(name)),
  CONSTRAINT route_groups_description_len CHECK (char_length(description) <= 500),
  CONSTRAINT route_groups_row_version_positive CHECK (row_version > 0)
);
CREATE UNIQUE INDEX route_groups_tenant_name ON route_groups (tenant_id, lower(name));
CREATE TRIGGER route_groups_updated_at BEFORE UPDATE ON route_groups
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

CREATE TABLE route_group_members (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id   uuid NOT NULL,
  node_id    uuid NOT NULL,
  created_at timestamptz(6) NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, node_id),
  CONSTRAINT route_group_members_group_fk
    FOREIGN KEY (tenant_id, group_id) REFERENCES route_groups (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT route_group_members_node_fk
    FOREIGN KEY (tenant_id, node_id) REFERENCES nodes (tenant_id, id) ON DELETE CASCADE
);
-- 生效合并与节点详情按 (租户, 节点) 反查所在组
CREATE INDEX route_group_members_node_idx ON route_group_members (tenant_id, node_id);

-- 两张路由表加第三个范围
ALTER TABLE node_outbounds ADD COLUMN group_id uuid;
ALTER TABLE node_routes    ADD COLUMN group_id uuid;
ALTER TABLE node_outbounds
  ADD CONSTRAINT node_outbounds_group_fk
    FOREIGN KEY (tenant_id, group_id) REFERENCES route_groups (tenant_id, id) ON DELETE CASCADE,
  ADD CONSTRAINT node_outbounds_scope_check CHECK (node_id IS NULL OR group_id IS NULL);
ALTER TABLE node_routes
  ADD CONSTRAINT node_routes_group_fk
    FOREIGN KEY (tenant_id, group_id) REFERENCES route_groups (tenant_id, id) ON DELETE CASCADE,
  ADD CONSTRAINT node_routes_scope_check CHECK (node_id IS NULL OR group_id IS NULL);

-- 全局 tag 唯一索引原先只看 node_id 为空，组内出站也满足这个条件，
-- 不收窄就会让不同组、以及组与全局之间不能同名——而组覆盖全局同名出站正是要支持的
DROP INDEX IF EXISTS node_outbounds_global_tag;
CREATE UNIQUE INDEX node_outbounds_global_tag
  ON node_outbounds (tenant_id, tag) WHERE node_id IS NULL AND group_id IS NULL;
CREATE UNIQUE INDEX node_outbounds_group_tag
  ON node_outbounds (tenant_id, group_id, tag) WHERE group_id IS NOT NULL;
CREATE INDEX node_outbounds_group_idx ON node_outbounds (tenant_id, group_id) WHERE group_id IS NOT NULL;
CREATE INDEX node_routes_group_idx ON node_routes (tenant_id, group_id, priority) WHERE group_id IS NOT NULL;

SELECT app.enable_tenant_rls('route_groups');
SELECT app.enable_tenant_rls('route_group_members');
GRANT SELECT, INSERT, UPDATE, DELETE ON route_groups TO aegis_app;
-- 成员只整体替换（删旧插新），没有 UPDATE
GRANT SELECT, INSERT, DELETE ON route_group_members TO aegis_app;

COMMENT ON TABLE route_groups IS
  '路由组：有名字的一组出站与分流规则（node_outbounds / node_routes 的 group_id 范围），节点经 route_group_members 加入。生效顺序：规则 节点 → 组（按 sort_order）→ 全局；出站 全局 → 组 → 节点按 tag 覆盖。';
COMMENT ON TABLE route_group_members IS
  '路由组成员：一个节点可在多个组里，组间顺序取 route_groups.sort_order。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
-- 组范围的出站与规则在两范围的旧 schema 里没有落脚处，回滚即丢弃
DELETE FROM node_routes    WHERE group_id IS NOT NULL;
DELETE FROM node_outbounds WHERE group_id IS NOT NULL;
DROP INDEX IF EXISTS node_routes_group_idx;
DROP INDEX IF EXISTS node_outbounds_group_idx;
DROP INDEX IF EXISTS node_outbounds_group_tag;
DROP INDEX IF EXISTS node_outbounds_global_tag;
CREATE UNIQUE INDEX node_outbounds_global_tag
  ON node_outbounds (tenant_id, tag) WHERE node_id IS NULL;
ALTER TABLE node_routes    DROP CONSTRAINT IF EXISTS node_routes_scope_check,
                           DROP CONSTRAINT IF EXISTS node_routes_group_fk,
                           DROP COLUMN IF EXISTS group_id;
ALTER TABLE node_outbounds DROP CONSTRAINT IF EXISTS node_outbounds_scope_check,
                           DROP CONSTRAINT IF EXISTS node_outbounds_group_fk,
                           DROP COLUMN IF EXISTS group_id;
DROP TABLE IF EXISTS route_group_members;
DROP TABLE IF EXISTS route_groups;
-- +goose StatementEnd
