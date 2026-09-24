# panel/frontend/src/admin/
> L2 | 父级: /panel/frontend/CLAUDE.md

管理后台入口（管理后台.dc.html 外壳）。登录态只看令牌：没有令牌是整页 LoginPage，有令牌是 Shell（深色侧栏 + 粘性顶栏 + 页头标签 + 模块内容区）；内容区按路由从 screens/ 登记表取懒加载的模块页，包在 shell/ScreenFrame 里。
reauth 的接线是本入口独有的：main.tsx 建 ReauthController 并作为 requestReauth 传给 api 客户端，常驻的 ReauthDialog 订阅它——任何页面的写请求被 403 reauth_required 拦下，都由这一个对话框接住，验证后 api.ts 用原幂等键重放，页面无感。用户取消时请求以 reauth_required 失败，页面应静默忽略。
路由是 hash：#/<模块>[/<标签>][/<rest>…]，未知模块或标签由 Shell replace 成规范地址（保留查询串），标签之后的段作为 rest 交给页面。
权限只看 GET v1/me 的 permissions，不硬编码角色：modules.ts 给每个模块（有标签的按标签）登记读权限码，侧栏、⌘K、页头标签只列可读的入口，缺省标签落在第一个可读标签上；直接输地址进入不可读的页面显示「无权限或不存在」，与接口 404 同一说法。me 未回来前导航不画、内容区显示骨架。

成员清单
index.html: 入口页，pandora-app=admin 标记、<html data-app="admin">（角色令牌切到墨色中性）
main.tsx: 建运行时与 ReauthController 并挂载 App；仅 DEV 把运行时挂到 window.__pandora 供浏览器里对假后端验证 reauth 重放，构建时整段裁掉
App.tsx: RuntimeProvider 包住 Root（按 useSignedIn 切 LoginPage / Shell）与常驻 ReauthDialog
modules.ts: 十个模块的标题、分组、标签（「欠费单」按保留规则 6 叫「挂账」）与读权限码（取自契约各模块主列表接口，含修订 R6），侧栏六组，⌘K 深链；canRead / visibleTabs / canReadModule 判权限，resolveRoute 拆出 module / tab / rest 并给规范地址，modulePath 拼链接，paletteItems 按权限筛选
reauth.ts: createReauthController：api 在 React 外、对话框在 React 内，用可订阅的小仓库接起来；并发请求共用一个待决 Promise
me.ts: GET v1/me 的 schema 与 useAdminMe（permissions 为 null 时归一为空数组；email / display_name / roles 是待补·后端字段，按可选）；identityLabels 按契约映射侧栏账户块文字，字段缺失时退回「管理员」与 user_id 前 8 位
LoginPage.tsx: 两栏登录页，左栏侧栏深底（< 960 收起），POST v1/auth/login（auth:false）成功写令牌，401 在密码框下内联显示
Shell.tsx: 外框编排：Sidebar、顶栏面包屑与 EventsCapsule、页头标题与可读的 ui/Tabs、内容区（按权限渲染 screens 登记的页面或「无权限或不存在」，me 读取失败给重试）；⌘K 全局快捷键、规范地址 replace（等 me 回来、保留查询串）、document.title
Sidebar.tsx: 字标与构建版本号（__APP_RELEASE__）、⌘K 入口、六组导航（只列可读模块，整组不可读就不画组名；工单 / 营销徽标取待补·后端的 GET v1/dashboard/tasks，只在有 ops.dashboard.read 时请求）、向上弹出的账户菜单（主题、改密码、打开门户 ../、退出）
EventsCapsule.tsx: 顶栏「实时事件」：持有后台唯一的 SSE 连接（需 ops.notification.read，4xx 时整块不渲染），事件同时驱动查询失效；可读事件流是待决 D-A-1，暂按 topic + op 生成通用条目，点击跳对应模块；describeEvent 为纯函数
CommandPalette.tsx: ⌘K 命令面板，原生 <dialog>，combobox + listbox，↑↓ / ↵ / Esc；只列当前权限可读的条目
ChangePasswordDialog.tsx: 修改我的密码：前端先拦 12 位且含字母数字（后端 12 位规则待补），POST v1/me/password（passwordCheck，401 显示在当前密码框），成功即清令牌回登录页（保留规则 4：后端已吊销全部会话）；passwordStrength 为四段强度条
ReauthDialog.tsx: 「敏感操作 · 需要重新认证」：api.reauth(password) 换新令牌后 resolve(true)，口令错在框内显示、不登出
*.module.css: 各组件同名样式；Sidebar.module.css 在侧栏内重定义 --surface / --border / --text 等令牌，让 ui/Menu 直接用在深底上
screens/: 十个模块的页面与懒加载登记表，三个前端会话各改各的目录；见 screens/CLAUDE.md
admin.test.ts: 路由规范化与 rest 子路由、读权限表、⌘K 筛选与隐藏、reauth 桥、身份文字映射、强度条、事件条目的纯逻辑测试；界面交互在浏览器里对 dev/mock-api 验收

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
