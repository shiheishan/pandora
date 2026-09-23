-- 00064: 区分「服务器删除级联静默」与「手工退役」
--
-- 背景：服务器删除会把名下节点 serving_status 置 retired（探针静默），
-- 但手工退役（SetNodeServingStatus → retired）也是同一个状态值。此前
-- 签发引导令牌与 Bootstrap 复活仅凭 serving_status='retired' 判定「静默态
-- 可重装覆盖」，导致手工退役的节点也能被同名重装绕过退役意图复活。
--
-- 修复：新增布尔标记，只有服务器删除级联置 true 的节点才允许重装覆盖；
-- 手工退役保持 false，重装时照旧拒绝「已退役节点不能重新引导」。

-- +goose Up
ALTER TABLE nodes
  ADD COLUMN silenced_by_server_delete boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN nodes.silenced_by_server_delete IS
  '服务器删除时级联静默探针的标记；为 true 时允许同名重装覆盖复活，手工退役不置此位。';

-- 列级权限：nodes 表在 00036 之后走列白名单，新增列必须显式授权，
-- 否则运行时代码一旦带上它就 permission denied（同 00061 的 node_no）。
-- +goose StatementBegin
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
		GRANT SELECT (silenced_by_server_delete), INSERT (silenced_by_server_delete), UPDATE (silenced_by_server_delete) ON nodes TO aegis_app;
	END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS silenced_by_server_delete;
