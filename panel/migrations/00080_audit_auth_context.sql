-- 审计记录的认证强度（M6）。
--
-- 后台审计页的「认证」一列要回答：做这件事的时候，操作者是凭一个普通会话，
-- 还是刚刚输过一遍密码（近期二次认证）。高危动作都挂了重认证，但此前审计只记
-- 「谁做了什么」，事后看不出那一刻的认证强度。
--
-- 由 audit.Write 从请求上下文的主体取值：主体就是本条记录的操作者且带会话时
-- 才写，系统任务、匿名与代他人记账的记录留空。历史记录一律为空，界面显示「—」。
--
-- 表是追加写 + 哈希链。新列纳入 entry_hash，但只在非空时参与计算：历史记录的
-- 哈希不受影响，新记录的认证强度被篡改会断链。

-- +goose Up
ALTER TABLE audit_events
  ADD COLUMN auth_context text NULL
    CONSTRAINT audit_events_auth_context_check CHECK (auth_context IN ('session', 'reauth'));

-- +goose Down
ALTER TABLE audit_events DROP COLUMN IF EXISTS auth_context;
