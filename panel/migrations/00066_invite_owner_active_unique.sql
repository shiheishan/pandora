-- +goose Up

-- MyInviteCode 是懒创建；数据库从此保证同一 owner 最多一条活动码。
-- 部署前若历史库已有重复活动码，应先由运维核对引用关系并禁用多余记录；
-- 不在结构迁移中静默修改业务数据。
CREATE UNIQUE INDEX invite_codes_one_active_per_owner
  ON invite_codes (tenant_id, owner_user_id)
  WHERE status = 'active' AND owner_user_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS invite_codes_one_active_per_owner;