-- +goose Up
-- +goose StatementBegin

-- 让引导令牌可以绑定到一台「已经在面板上建好」的服务器。
--
-- 现状是反过来的：Agent 带令牌接入 → 创建 node → 顺手用 node 的 id
-- 建一条 servers 记录（ON CONFLICT (id) 里 id 就是 nodeID）。于是管理员
-- 在面板上手动新建的那条服务器永远是空壳——没有 Agent、没有心跳、
-- 没有节点，而机器接进来时又另起了一条。同一台物理机在列表里出现两次，
-- 谁也说不清该看哪条。
--
-- 加上这一列之后，签发令牌时可以指定「这个令牌是给哪台服务器用的」，
-- Agent 接入时就挂到那台上，不再新建。Agent 侧不用改：绑定关系在令牌
-- 里，它只管把令牌交上来。
ALTER TABLE bootstrap_tokens
  ADD COLUMN server_id uuid;

ALTER TABLE bootstrap_tokens
  ADD CONSTRAINT bootstrap_tokens_server_fk
  FOREIGN KEY (tenant_id, server_id) REFERENCES servers (tenant_id, id) ON DELETE CASCADE;

COMMENT ON COLUMN bootstrap_tokens.server_id IS
  '令牌绑定的服务器。非空时 Agent 接入后挂到这台已存在的服务器，而不是新建一条。';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE bootstrap_tokens DROP CONSTRAINT IF EXISTS bootstrap_tokens_server_fk;
ALTER TABLE bootstrap_tokens DROP COLUMN IF EXISTS server_id;
-- +goose StatementEnd
