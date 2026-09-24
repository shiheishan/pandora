-- 节点国家与服务端令牌签发记录（M8）。
--
-- country_code：后台节点表格第一列的国旗。由管理员在新建 / 编辑节点时填写，
-- 不从 IP 反查——节点地址常是域名或中转，反查出来的是机房所在地而不是线路
-- 落地点。只进管理端响应，门户任何接口都不输出（保留规则 3：用户看不到节点
-- 的国家和负载）。
--
-- server_token_issued_at / _by：服务端令牌只存哈希，签发后谁也看不到原文；
-- 身份 tab 至少要能回答「什么时候、谁签的」，判断现在跑的是不是那一枚。

-- +goose Up
ALTER TABLE nodes
  ADD COLUMN country_code char(2) NULL
    CONSTRAINT nodes_country_code_check CHECK (country_code ~ '^[A-Z]{2}$'),
  ADD COLUMN server_token_issued_at timestamptz NULL,
  ADD COLUMN server_token_issued_by uuid NULL REFERENCES users(id) ON DELETE SET NULL;

-- 列级权限：nodes 表走列白名单（同 00061 的 node_no、00064 的静默标记），
-- 新增列必须显式授权，否则运行时代码一旦带上它就 permission denied。
-- +goose StatementBegin
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
		GRANT SELECT (country_code, server_token_issued_at, server_token_issued_by),
		      INSERT (country_code, server_token_issued_at, server_token_issued_by),
		      UPDATE (country_code, server_token_issued_at, server_token_issued_by)
		   ON nodes TO aegis_app;
	END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE nodes
  DROP COLUMN IF EXISTS server_token_issued_by,
  DROP COLUMN IF EXISTS server_token_issued_at,
  DROP COLUMN IF EXISTS country_code;
