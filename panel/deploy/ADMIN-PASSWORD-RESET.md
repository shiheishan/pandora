# 管理员密码安全重置

管理员密码保存在 PostgreSQL 中。发布新二进制、执行迁移或重启服务都不会自动改变已有密码。

## 适用命令

- 首次初始化：`aegis-adminctl create --email <email> --password-stdin`
- 已有管理员忘记密码：`aegis-adminctl reset-password --email <email> --password-stdin`

`reset-password` 只接受已存在、状态为 active、拥有永久租户级 `iam.user.write` 与
`iam.role.write` 权限的管理员。它不会创建用户、激活停用账号、修改显示名或授予角色。

## 操作步骤

1. 通过已授权 SSH 公钥或云厂商控制台进入目标机。
2. 先核对当前运行二进制与可信发布清单的 SHA-256。
3. 加载目标环境的 `AEGIS_DATABASE_URL`，确认它指向预期租户数据库。
4. 从终端静默读取新密码，并只通过标准输入交给命令：

```bash
read -r -s -p 'New administrator password: ' PANDORA_NEW_PASSWORD
printf '\n'
printf '%s\n' "$PANDORA_NEW_PASSWORD" |
  /opt/aegispanel/bin/aegis-adminctl reset-password \
    --email '<administrator-email>' \
    --password-stdin
status=$?
unset PANDORA_NEW_PASSWORD
test "$status" -eq 0
```

不要把密码写入命令参数、环境文件、工单、发布日志或 shell 历史。

## 成功语义

密码 PHC、全部未吊销会话、全部 active 刷新令牌、专属客户端刷新凭据和审计事件在同一个数据库事务中提交。
成功后所有设备（包括执行改密时的当前浏览器）都必须重新登录；无需重启应用服务。

若数据库已经存在 `public.refresh_families`，但尚未安装并授权固定签名的
`app.revoke_user_refresh_families(uuid,uuid)` SECURITY DEFINER 函数，改密会整笔回滚。
这是故意的 fail-closed 行为；禁止向运行时账号授予该表的直接 UPDATE 权限。

## 验收

1. 新密码可以登录管理员自定义路径。
2. 旧密码返回统一认证失败。
3. 改密前签发的管理员和用户域访问令牌都返回 401。
4. 数据库 `user_passwords.rotated_at` 已前移，但任何日志中都没有明文密码。
5. 审计中存在 `adminctl.password_reset`，且目标用户 ID 与租户正确。

若任一项失败，停止后续发布并从数据库备份与发布清单检查目标环境是否选错；不要反复尝试
不同密码或使用直接 SQL 绕过事务和审计。
