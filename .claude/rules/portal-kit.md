---
paths:
  - "panel/frontend/src/portal/**"
---

# 门户视觉套件（src/portal/kit）

门户 10-16 起按样稿 `panel/frontend/dev/prototype/portal-v5.html`（森林绿，逐页过审）换外观。样稿是目标：写页面时对着样稿的 markup 与 CSS，
能用套件的一律用套件，页面自己的 `*.module.css` 只写本页独有的布局与间距。页面只从 `src/portal/kit/index.ts` import（`'../../kit'`）。

## 什么时候用 kit、什么时候用 ui/

- 外框（`Shell.tsx`）已经画好：左侧窄导航 / 手机底部标签栏、顶部 Logo、内容列（720，`pages.ts` 的 `WIDE_PAGES` 是 1040）、页标题 h1、「‹ 返回」、进场动效、点阵底纹。页面**不要**再包 `.col`、不画 h1、不画 Logo。
  - 标题与返回：`usePageHead(标题, 返回落点)`（`portal/head.tsx`）。
  - 标题行右侧的控件（样稿 `.h-row`，如选购页的「按时间 / 一次性」）：`<PageHeadAside>…</PageHeadAside>`（`portal/head.tsx`，portal 进外框的插槽）；不要经 usePageHead 传 ReactNode。
- 按钮：页面一律用 kit 的 `Btn`（能渲染成链接，有 hot 色）。`ui/Button` 经角色令牌在门户里与 `Btn` 同样外观，只在 ui 组件内部出现（`ConfirmModal`、`QueryView` 的重试、`Pager`）或必须用 ui 组件的地方。
- 弹层：kit 的 `Sheet`（= `ui/Modal` + 右上角圆形 ×）。只要「确认 / 取消」两个按钮的简单确认用 `ui/ConfirmModal`。不自己造弹层，不用 `position: fixed` 的 div 冒充。
- 分段控件（样稿 `.seg`）：`ui/Segmented`（radiogroup，方向键可用）。`size="md"` = `.seg`，`size="sm"` = `.me-theme`；角色令牌已把它变成样稿的样子。选项里要带计数（样稿订单页 `.seg em`）就把 label 写成 ReactNode。
- 输入框、下拉、开关、复选：`ui/Input` / `TextArea` / `Select` / `Switch` / `Checkbox`，表单字段 `ui` 的 `Field` 写法照旧。门户里输入框 50 高、圆角 14、页面底色（角色令牌）。
- 底部提示：`ui/useToast`（959 以下自动抬到标签栏上方）。加载、出错、空：`ui/QueryView`、`ui/Skeleton`、`ui/Empty`。
- 状态小标签（样稿 `.st`）：`ui/Tag`（ok / warn / danger / info / neutral / brand / brandSolid）。档位名胶囊（样稿 `.chip`）：kit 的 `Chip`。

## 组件一览（样稿类 → 组件）

| 组件 | 样稿 | 用途与要点 |
|---|---|---|
| `Icon({ name, size=20, className, label })` | `svg.i` | Hugeicons 描边 1.7。`name` 是下面的语义名；没有 `label` 时 aria-hidden，有则 `role="img"` |
| `ICONS`、`type IconName` | `#i-*` | 语义名 → Hugeicons 组件的表（`kit/icons.ts`） |
| `Btn({ tone='primary', size='md', grow, full, href?, busy?, … })` | `.btn` `.hot` `.soft` `.danger` `.sm` `.grow` `.full` | tone：primary 竖向渐变（默认）、hot 横向渐变（每屏最主要的一个动作，如「添加到 App」「充值」）、soft 白底、danger 红。给 `href` 渲染 `<a>`，否则 `<button type="button">`；`busy` 时禁用并转圈。md 50 高、sm 38 高 |
| `Hero({ tone='live', pill, pillTone, as, … })` | `.hero` `.hero.off` `.hero.empty` `.pill` `.pill.bad` `.balance` | 主卡：我的套餐每份一张（`as="article"`，浏览器测试按 article 找卡片）、钱包余额卡同款。off = 已到期，empty = 没有套餐。**不带外边距**，页面给 |
| `Eyebrow` | `.eyebrow` | 档位名上方的小字（站点名 · 档位） |
| `Sticker({ as='div', size='lg' })` | `.tag` | 档位名白底贴纸（serif 900）。卡片标题要能被读屏当标题时 `as="h2"`；md 是确认页 `.ck-item .tag` 的 20px |
| `BigNumber({ value, unit, prefix, size='xl' })` | `.num.big` + `.unit`、`.balance .amt`、`.comm .amt` | 大数字一行。xl 72（手机 60）、lg 60、md 46；`prefix` 是「¥」 |
| `Meta({ size='md' })` | `.meta` | 卡片里的灰字说明，`<b>` 是正文色 600；sm 是 13.5px |
| `Bar({ value, low, label })` | `.bar` `.bar.low` | 进度条，value 0–1；进场从 0 长出、尊重减少动效；给 `label` 才是 progressbar |
| `LinkChip({ label, code, action })` | `.linkchip` | 链接尾号 + 右侧动作（传 `<button>`，自动是主题色 600 文字） |
| `Rows` / `Row(props)` | `.rows` `.row` `.ri` `.ico` `.rt small` `.rv` `.rv.num` `.rv.plus` `chev` `.row.static` | 白底列表卡与行。`href` → 链接行、`onClick` → 按钮行、都不给 → 静态行。`icon` + `iconStyle`（square=`.ri` / round=`.ico` / roundMuted=`.ico.out`）或 `lead`（头像等）；`title`、`sub`、`value` + `valueTone`（text / num / plus 入账 / action 主题色文字）、`end`（分段控件、小按钮）、`chevron`（链接与按钮行默认有） |
| `IconDisc({ name, tone })` | `.ico`、`.hg .ico` | 单独用的圆形图标（brand / muted / solid） |
| `SectionTitle({ aside, as='h2' })` | `h3`、`.h-row` + `h3` | 分区小标题（「这一份」「余额记录」）；`aside` 放右侧动作 |
| `GroupHeader({ aside })` | `.group-h` | 按月分组的组头 |
| `Note({ tag, tagTone, sub, go, href?/onClick? })` | `.note` `.k.warn` `.k.danger` `.go` | 横条提醒（待支付、重要公告）；手机上隐藏 sub |
| `Chip({ tone='soft' })` | `.chip` `.chip.rec` | 档位名小胶囊（菜单头、「我的」资料卡）；solid = 推荐 |
| `Avatar({ name, size='sm', solid })` | `.avatar` `.acct .avatar` `.me-card .avatar` | 首字头像 34 / 48 / 52 |
| `Num({ as })` | `.num` | 展示字体（Courier Prime）数字：金额、流量、尾号 |
| `Sheet({ open, onClose, title, actions, size=440, dismissible })` | `.sheet` `.sh-card` `.sh-top` `.x` | 弹层，原生 `<dialog>`；遮罩模糊、圆角 26、手机（< 640）贴底；`actions` 放 `Btn`，门户里平分整行 |
| `cx(...)` | | 拼 className |

套件之外、样稿里只有一个页面用的类（`.copies`、`.low-tip`、`.plans`、`.pend`、`.tk`、`.au-*` …）写在各页面自己的 module.css 里；第二个页面也要用时再提进 kit，并改本文件。

## 图标语义名（样稿 i-* → Hugeicons Pro stroke-rounded）

subs Layers01 · plans ShoppingBag01 · wallet Wallet01 · orders Invoice01 · gift Gift · bell Notification01 · chat BubbleChat · help HelpCircle ·
shield Shield01 · user User · chev ArrowRight01 · moon Moon02 · chart ChartHistogram · swap ArrowDataTransferHorizontal · edit PencilEdit01 ·
link Link01 · plus PlusSign · card CreditCard · in ArrowDownLeft01 · out ArrowUpRight01 · reset Refresh · renew CalendarAdd01 · search Search01 ·
key Key01 · desk Computer · phone SmartPhone01 · send Telegram · mail Mail01 · check Tick02 · updown UnfoldMore · logout Logout01

- 新图标先在 `kit/icons.ts` 加语义名再用。Pro 图标要授权、仓库公开：只经 npm 包在构建时引入，不把 SVG 路径抄进仓库。

## 令牌对照（样稿变量 → 我们的令牌）

| 样稿 | 令牌 |
|---|---|
| `--bg` | `--bg` |
| `#fff`（底） | `--surface`；白字用 `--on-brand` |
| `--paper-2` / `--paper-3` | `--surface-2` / `--surface-3` |
| `--ink` / `--text-2` / `--text-3` | `--text` / `--text-2` / `--text-3` |
| `--act` | `--brand` |
| `--deep` | `--brand-deep`（按钮渐变深端、实心角标底）；浅底上的深绿字用 `--brand-ink` |
| `--tint` / `--tint-line` / `--mark` | `--brand-soft` / `--brand-line` / `--brand-mark` |
| `--warn` `--warn-bg` / `--danger` `--danger-bg` | `--warn` `--warn-soft` / `--danger` `--danger-soft` |
| `--lift` | `--lift` |
| `--line`（8%）、`--dot`（7%） | `--border`（深色模式下阴影色的 8% 看不见，`--border` 浅色值与它几乎一样） |
| `rgba(var(--sh), a)`（阴影） | `color-mix(in srgb, var(--shade) a×100%, transparent)` |
| `rgba(255,255,255,a)`（半透明白底） | `color-mix(in srgb, var(--surface) a×100%, transparent)` |
| `--tw` / `--serif-zh` / `--sans` | `--font-display` / `--font-serif` / `--font-ui` |
| 按钮 `--btn` / `--btn-border` / `--btn-glow` | 角色令牌 `--button-primary-bg` / `--button-primary-border` / `--button-primary-shadow` |
| `--hot`、`--bar` | 写在 `kit/Btn.module.css`、`kit/Hero.module.css` 里（品牌三色渐变） |
| `body` 点阵底纹 | 角色令牌 `--page-bg`（外框 `.app` 已用） |

- 不写死色值：样稿里写死的 `#fff` 一律 `--surface`，深色模式才成立。语义绿 `#e4f1e8 / #23603c`（已支付、成功）用 `--ok-soft / --ok`。
- 断点只有 `max-width: 959px`（手机细节都写在这里）与 `639px`。
- 动效（rise 进场、进度条）都要在 `prefers-reduced-motion: reduce` 下关掉。

## 角色令牌（ui/ 组件在门户里的样子）

`styles/roles.css` 的 `:root[data-app='portal']` 与 `design-tokens.ts` 的 `ROLE_TOKENS` 同源：控件 50 / 38 高、圆角 14、按钮 `--button-*`（渐变、描边、投影、悬停提亮、按下位移）、
输入框 `--control-border-color` 与 `--control-bg`、弹窗 `--dialog-*`（圆角 26、标题 18、遮罩模糊、按钮平分）、分段 `--segment-*`、页面底 `--page-bg`。
后台块的同名键取值让后台外观不变；改这些键要两块、`ROLE_TOKENS` 一起改（`tests/tokens.test.ts`）。
