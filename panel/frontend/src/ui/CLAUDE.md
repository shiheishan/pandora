# panel/frontend/src/ui/
> L2 | 父级: /panel/frontend/CLAUDE.md

自研组件库，门户与后台共用一套实现，不用任何现成 UI 库。组件只读令牌不写色值：颜色来自 styles/tokens.css，「门户用朱砂 40/9、后台用墨色 32/7」这类差异全部来自 styles/roles.css 的角色令牌，按 <html data-app> 切换，所以组件里没有 admin / portal 分支（只有后台 28/24 按钮用 12px 字这一处入口选择器）。
样式一律 CSS Modules（CSP 无 unsafe-inline，不用 CSS-in-JS）；React 的 style 属性只用于真正动态的尺寸（表格列宽、抽屉宽度、骨架尺寸）。
弹层用原生能力而不是自己造：Modal / Drawer 基于 <dialog>.showModal()，浏览器负责顶层渲染、焦点圈定、背景 inert 与关闭后焦点归还；Toast 容器是 popover，每条提示都重新推到顶层，所以弹窗开着时也在遮罩之上；Select 是原生 <select>。键盘交互按 WAI-ARIA 模式：Tabs（左右键、Home/End、漫游 tabindex）、Segmented（单选组方向键）、Menu（menu button：上下键、Esc 归还焦点、点外面关闭）。
页面只从 index.ts 引入；验收用 showcase（npm run dev:showcase）。

成员清单
index.ts: 公开出口，页面只从这里 import；cx 与 useModalDialog 是内部实现不导出
cx.ts: className 拼接
icons.tsx: IconCheck / IconChevronDown / IconClose，16px 线性单色（规范：图标只用线性单色）
Button.tsx: 六种 variant（primary 实心强调、outline、secondary、ghost、danger、link）× 四档 size（md / sm / xs 对应 --h-control 三档，dialog 为弹窗按钮）；busy 禁用并转圈；默认 type="button"
Field.tsx: 字段外壳与 useFieldIds：标签在上、说明或错误在下，生成 id 接好 htmlFor、aria-describedby、aria-invalid，错误行 role="alert"
control.module.css: Input / TextArea / Select 共用的输入框外观：聚焦边框转强调色加 3px --focus-halo 外圈，错误转危险色，select 换成跟随文字色的折线
Input.tsx: Input 与 TextArea；mono 用于邀请码、优惠码、IP
Select.tsx: 原生 select 加占位项
Switch.tsx: 原生 checkbox + role="switch"，32×18；选中底 --accent、圆钮 --switch-knob-on
Checkbox.tsx: 原生 checkbox 换外观，16px（表格里 14px），支持 indeterminate
Tag.tsx: Tag 八种配色（ok / warn / danger / info / neutral / brand / brandSolid / outline），圆角 5 不做胶囊；CountBadge 朱砂角标，0 不渲染、超过上限显示 99+
Card.tsx: 1px 描边无阴影，圆角与内边距随入口；tint 为套餐卡朱砂极浅底；flush 给贴边的表格与列表
Table.tsx: 语义化 <table>，columns 描述列（宽度、右对齐、mono）；可选行勾选（表头三态）、行点击、加载骨架、空状态；外层横向滚动，窄屏不挤压列
Tabs.tsx: 下划线标签页，当前项 600 字重 + 2px --accent 内阴影下划线
Segmented.tsx: 分段单选，--surface-3 底上浮起 --surface 与 --shadow-segment
useModalDialog.ts: 受控 open ↔ showModal()/close()；Esc 与点遮罩统一成 onClose（dismissible=false 时都不关）；打开时焦点给 [data-autofocus]，没有就给面板本身，避免第一个按钮上出现键盘焦点圈
Modal.tsx: Modal（宽 400 / 560 / 720，圆角随入口，内边距 22，可选 eyebrow 小字）与 ConfirmModal（先说后果、动词按钮、onConfirm 返回 Promise 时进行中不可关闭）；< 640 贴底成为底部抽屉、按钮通栏
Modal.module.css: <dialog> 铺满视口作遮罩层，::backdrop 用 --scrim；:root:has(dialog[open]) 锁住页面滚动
Drawer.tsx: 右侧抽屉（默认 480，用户详情传 560），头部标题/副标题/操作/关闭，可挂 toolbar（如 Tabs），内容滚动，底部操作区；< 640 全宽
Toast.tsx: ToastProvider 与 useToast(message, tone)；反色底 + 状态圆点，2.6 秒消失，role="status" 播报；门户 < 640 抬到底部标签栏之上
Menu.tsx: 下拉菜单，自己渲染触发按钮（调用方给内容与 className）；条目为普通项（hint、current、danger、disabled）、开关项（menuitemcheckbox，如深色模式）与分隔线，可带 header；可受控（open / onOpenChange，外部开关带 data-menu-toggle 免被点外关闭抢先）、可向上弹出（placement="top"）、menuClassName 改面板宽度
Skeleton.tsx: 骨架块，300ms 后才显现（CSS 动画延迟），aria-hidden
Empty.tsx: 空状态：一句现状 + 一句能做什么 + 最多一个次按钮；bare 用于表格与卡片内部
*.module.css: 各组件同名样式，只引用令牌
ui.test.tsx: 以 renderToStaticMarkup 核对角色与 aria 结构（按钮类型与进行中、字段接线、下拉占位、开关角色、角标、标签页漫游 tabindex、分段单选、表格语义与三态勾选、弹层标题关联与关闭按钮、菜单初始关闭），不引入 DOM 库；键盘与弹层开合在 showcase 用浏览器验收

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
