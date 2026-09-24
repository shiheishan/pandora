# panel/frontend/src/portal/screens/
> L2 | 父级: /panel/frontend/src/portal/CLAUDE.md

门户十一个页面（含结账）。Shell 只认 index.ts 这张登记表：按路由取出页面的懒加载组件，传入 { rest }，外面包 shell/ScreenFrame（Suspense 骨架 + 错误边界）；页头标题与副标题仍由 Shell 按 pages.ts 画，页面只管内容区。
每个页面一个目录、构建出一个独立块，归门户前端会话；登记表与 Placeholder 不再改动，目录里加文件时由门户会话补本目录的 L2。
rest 是页面之后剩下的路径段，已解码（如 #/orders/<订单号>、#/tickets/<id>）；查询串（如 #/checkout?...）用 core/router 的 useHashLocation 读，外框规范化地址时保留查询串。

成员清单
index.ts: 页面登记表 SCREENS（十一个 React.lazy）与页面入参类型 PortalScreenProps { rest: string[] }
Placeholder.tsx: 占位页（第 2 阶段 Shell 的空状态），十一个页面都换成真页面后删除
overview/ subs/ plans/ checkout/ orders/ wallet/ referral/ tickets/ messages/ help/ account/: 各页面目录，index.tsx 默认导出页面组件，目前渲染 Placeholder

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
