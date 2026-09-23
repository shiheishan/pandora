-- +goose Up
-- 给节点一个短数字编号，取代后台里到处露出的 UUID。
--
-- 后台节点列表原来第一列显示 019fd0d5-03fe-7fd7-b478-63dd32948cdb 这样的
-- 完整 UUID。它在数据层是对的，在运维层是不能用的：报障要念一遍、比对
-- 两个节点要逐字看、写在工单和群里没人认得出是哪台。xboard 那边这一列是
-- 101、102、103，运维之间直接说「103 挂了」。
--
-- 所以 UUID 保留为主键不动，只额外给一个人看的编号。两者职责分开：
-- 接口和外键继续用 UUID，界面和人际沟通用 node_no。
--
-- 编号用一个全局序列而不是「租户内 max+1」：
--
--   max+1 在并发建节点时会撞号，要靠额外加锁才对，而锁的粒度一没选好
--   就会把批量建节点串行化。序列天生并发安全，代价只是删掉节点后号会
--   留空——但编号本来就只是个稳定标签，连续性没有意义，反而是「号被
--   复用」会让历史工单指向另一台机器。
--
-- 从 101 起步，避免个位数编号在日志和截图里和其它计数混淆。

-- +goose StatementBegin
CREATE SEQUENCE IF NOT EXISTS nodes_node_no_seq AS integer START WITH 101 MINVALUE 101;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS node_no integer;
-- +goose StatementEnd

-- 先给存量节点补号，按创建时间排，让编号顺序和建立顺序一致。
-- +goose StatementBegin
DO $$
DECLARE
	r record;
BEGIN
	FOR r IN SELECT id FROM nodes WHERE node_no IS NULL ORDER BY created_at, id LOOP
		UPDATE nodes SET node_no = nextval('nodes_node_no_seq') WHERE id = r.id;
	END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE nodes ALTER COLUMN node_no SET DEFAULT nextval('nodes_node_no_seq');
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE nodes ALTER COLUMN node_no SET NOT NULL;
-- +goose StatementEnd

-- 唯一性按租户算：编号是给人看的，跨租户重号不影响任何一方，
-- 而全局唯一会让某个租户的编号莫名其妙跳着走。
-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS nodes_tenant_node_no_unique ON nodes (tenant_id, node_no);
-- +goose StatementEnd

-- 列级权限：这张表在 00036 之后走列白名单，新增列必须显式授权，
-- 否则插入语句一旦带上它就 permission denied。
-- +goose StatementBegin
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
		GRANT SELECT (node_no), INSERT (node_no), UPDATE (node_no) ON nodes TO aegis_app;
	END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS nodes_tenant_node_no_unique;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE nodes DROP COLUMN IF EXISTS node_no;
-- +goose StatementEnd
-- +goose StatementBegin
DROP SEQUENCE IF EXISTS nodes_node_no_seq;
-- +goose StatementEnd
