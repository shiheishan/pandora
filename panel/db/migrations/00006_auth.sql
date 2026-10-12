-- +goose Up
-- auth 模块：后台管理员名单。
-- 后台不分角色、没有权限码、没有临时提权：在这张表里、且账号 status = 'active' 的用户就是管理员，
-- 能做全部后台操作；不在表里的用户进不了后台。授予与取消只经 pandora admin create / grant / revoke。
-- 会话、refresh、快捷登录、凭据纪元、登录失败计数都在 Redis，库里不留副本。

CREATE TABLE admins (
  user_id    uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  -- 授予人；命令行授予时为空，授予人账号被删时置空
  granted_by uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS admins;
