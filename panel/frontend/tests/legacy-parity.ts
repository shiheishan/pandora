/**
 * [INPUT]: 无运行时依赖；被 tests/api-surface.test.ts 读取，对照 panel/web/{admin,portal}/index.html 的 v1 调用
 * [OUTPUT]: 对外提供 LegacyDomain 类型、legacyOnlyPaths 迁移清单
 * [POS]: tests 的“旧页独有操作”迁移清单：生产手写单页调用、React 尚未调用的每条 v1 路径都列在这里。
 *        放在 tests/ 而不是 src/：它没有运行时消费者，而 api-surface 会把 src/ 里每个 v1/ 字面量当成 React 的调用。
 *        它只会缩小：React 补上一条而没删登记，或旧页多出一条而没登记，契约测试都会失败；清单归零即具备切换条件
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
export type LegacyDomain = "admin" | "portal";

// 键是归一化路径（路径参数写成 {}，不含查询串），值是旧页里的用途，给迁移排期用。
// 迁完一页：删掉这一页的条目，测试会确认 React 真的调用了它们。
export const legacyOnlyPaths: Record<LegacyDomain, Record<string, string>> = {
  admin: {
    // 概览
    "v1/dashboard/backlog/notifications": "概览：通知投递积压",
    "v1/revenue/adjustments": "概览：收入调整列表与登记",
    "v1/revenue/adjustments/{}/reverse": "概览：冲销一笔收入调整",
    "v1/stats/timeseries": "概览与风控：注册、活跃时序",
    "v1/system/status": "概览：系统组件状态",
    // 订单与套餐
    "v1/orders/{}/mark-paid": "订单：手工标记已支付",
    "v1/plans/{}/pools": "套餐：读取与分配节点池",
    "v1/plans/{}/versions": "套餐：版本列表与新建版本",
    "v1/plans/{}/versions/{}": "套餐：单个版本",
    // 用户与风控
    "v1/ip-clusters": "风控：共享 IP 聚类",
    "v1/users/bulk/preview": "批量用户：按条件预览",
    "v1/users/bulk/export": "批量用户：导出",
    "v1/users/bulk/mail": "批量用户：群发邮件",
    "v1/traffic-resets/stats": "流量重置：统计",
    // 节点
    "v1/nodes/bootstrap-token": "节点：签发一键安装令牌",
    "v1/nodes/reality-keypair": "节点编辑：生成 REALITY 密钥对",
    "v1/nodes/{}/protocol": "节点：保存协议参数",
    "v1/nodes/{}/revoke-identity": "节点：吊销节点身份",
    "v1/nodes/{}/server-token": "节点：重新签发服务端令牌",
    "v1/nodes/{}/status": "节点：逐条生命周期状态（React 的启停走 nodes/status:batch，这条是停用/退役）",
    // 工单
    "v1/tickets/escalate": "工单：升级",
    // 设置、通知与插件
    "v1/settings/device-limit": "设置：全局设备数限制策略",
    "v1/settings/mail/test": "设置：SMTP 发送测试",
    "v1/settings/telegram/test": "设置：Telegram 发送测试",
    "v1/mail/templates/test": "通知模板：实发测试信",
    "v1/plugin-hooks/{}": "插件钩子：删除",
    // 账户
    "v1/me/password": "管理员修改自己的口令",
  },
  portal: {
    // 注册与登录
    "v1/site-config": "登录页：站点名与注册开关",
    "v1/auth/register/start": "注册第一步：邮箱与邀请码",
    "v1/auth/register/complete": "注册第二步：验证码与口令",
    "v1/auth/quick-login": "快捷登录链接的消费端",
    "v1/me/quick-login": "生成快捷登录链接",
    // 通知
    "v1/me/notifications": "通知中心列表",
    "v1/me/notifications/read-all": "通知中心：全部已读",
    "v1/me/notifications/{}/read": "通知中心：单条已读",
    "v1/me/notification-preferences": "通知偏好读写",
    // Telegram
    "v1/me/telegram": "Telegram 绑定状态与解绑",
    "v1/me/telegram/bind-code": "Telegram 绑定码",
    // 资金与工单
    "v1/me/topups": "余额充值",
    "v1/support/tickets/{}/withdraw": "工单撤回",
  },
};
