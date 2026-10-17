export type TokenGroup = 'neutral' | 'brand' | 'status' | 'overlay' | 'supplement'

export interface ColorToken {
  name: string
  light: string
  dark: string
  group: TokenGroup
  use: string
}

const c = (group: TokenGroup, name: string, light: string, dark: string, use: string): ColorToken => ({
  name,
  light,
  dark,
  group,
  use,
})

// ---------------------------------------------------------------------------
// 颜色：10-16 起默认是门户样稿 v5 的「森林绿」（另三套配色只记在 dev/prototype/PALETTES.md）。
// 浅色取样稿 :root；样稿没画深色，深色按同一色相自配。后台另有一套黑白灰（样稿 v4），
// 写在 ADMIN_OVERRIDES，不进这张表。
// ---------------------------------------------------------------------------
export const COLOR_TOKENS: readonly ColorToken[] = [
  c('neutral', '--bg', '#f4f5f2', '#0e1210', '页面底色'),
  c('neutral', '--surface', '#fff', '#161b18', '卡片、输入框、表格、菜单'),
  c('neutral', '--surface-2', '#f6f8f6', '#1a201c', '列表行悬停、次级底'),
  c('neutral', '--surface-3', '#eef1ee', '#202722', '分段控件底、菜单项悬停、中性标签、骨架块'),
  c('neutral', '--border', '#e4e8e5', '#262e29', '卡片描边、分隔线'),
  c('neutral', '--border-strong', '#d5dcd7', '#2f3832', '输入框和次按钮的描边'),
  c('neutral', '--border-hover', '#b9c3bc', '#46524a', '描边悬停、关闭态开关'),
  c('neutral', '--text', '#141715', '#e7ebe7', '正文、标题、Toast 底、后台主按钮'),
  c('neutral', '--text-2', '#6a706b', '#9ba59e', '辅助说明、未选中导航'),
  c('neutral', '--text-3', '#969b96', '#7a847d', '占位符、表头、时间戳、箭头'),

  c('brand', '--brand', '#2c5a3f', '#5cbf83', '门户主按钮、链接、选中图标、焦点框'),
  c('brand', '--brand-hover', '#244c35', '#72cc95', '悬停和按下'),
  c('brand', '--brand-ink', '#1f3d2b', '#a5dcb8', '浅底上的文字、链接悬停'),
  c('brand', '--brand-soft', '#dfe8e1', '#1d3125', '头像、序号圆、主卡光晕、聚焦外圈'),
  c('brand', '--brand-tint', '#f3f7f4', '#141b16', '选中行与套餐卡的浅底'),
  c('brand', '--on-brand', '#fff', '#06120b', '主题色实心底上的字'),
  c('brand', '--brand-deep', '#1f3d2b', '#3f9c66', '主按钮渐变的深端、推荐角标、选中金额格'),
  c('brand', '--brand-line', '#b4c8ba', '#2f4c3a', '主卡与选中项的描边'),
  c('brand', '--brand-mark', '#4cbb72', '#4cbb72', 'Logo 圆点、进度条与强调按钮渐变的亮端'),

  c('status', '--ok', '#1f7a4f', '#6fcf9b', '在线、已支付、已打款、生效中'),
  c('status', '--ok-soft', '#e3f3ea', '#15271e', '成功标签底'),
  c('status', '--ok-line', '#c9f0d8', '#1f3a2b', '成功描边'),
  c('status', '--warn', '#8a5a00', '#f0b35a', '待处理、维护中、7 天内到期、流量 ≥ 80%'),
  c('status', '--warn-soft', '#fbf1dc', '#2c2213', '警告标签底'),
  c('status', '--warn-line', '#f0d9b0', '#4a3a1c', '警告描边'),
  c('status', '--danger', '#a3283a', '#f07a86', '离线、欠费、失败、危险操作、流量 ≥ 95%'),
  c('status', '--danger-soft', '#f9e4e6', '#2c1519', '危险标签底'),
  c('status', '--danger-line', '#eec3c8', '#4d252b', '危险描边'),
  c('status', '--danger-tint', '#fdf3f3', '#221417', '危险区块极浅底'),
  c('status', '--info', '#34507c', '#93b1dc', '处理中、客服已回复、人工确认'),
  c('status', '--info-soft', '#e6ebf3', '#172031', '信息标签底'),
  c('status', '--info-line', '#cdd6e4', '#2a3a52', '信息描边'),

  c('overlay', '--on-ink', '#fff', '#0e1210', '墨色实心底上的字（后台主按钮）'),
  c('overlay', '--hdr', 'rgba(244,245,242,.9)', 'rgba(14,18,16,.88)', '半透明顶栏'),
  c('overlay', '--scrim', 'rgba(20,40,30,.28)', 'rgba(0,0,0,.55)', '弹窗遮罩'),
  c('overlay', '--shadow', '0 24px 48px -20px rgba(20,40,30,.5)', '0 24px 48px -20px rgba(0,0,0,.7)', '菜单、Toast、弹窗'),
  c('overlay', '--shade', '#14281e', '#000', '半透明分隔线、点阵底纹、阴影的基色（按比例混透明）'),
  c('overlay', '--lift', '0 0 0 1px rgba(20,40,30,.035),0 30px 60px -32px rgba(20,40,30,.22),0 6px 16px -8px rgba(20,40,30,.09)', '0 0 0 1px rgba(255,255,255,.04),0 30px 60px -32px rgba(0,0,0,.7),0 6px 16px -8px rgba(0,0,0,.4)', '门户卡片的浮起阴影'),

  c('supplement', '--on-danger', '#fff', '#1a0f12', '危险实心按钮上的字'),
  c('supplement', '--toast-ok', '#6fcf9b', '#1f7a4f', 'Toast 成功圆点（反色底）'),
  c('supplement', '--toast-danger', '#f07a86', '#b3263a', 'Toast 错误圆点（反色底）'),
  c('supplement', '--shadow-segment', '0 2px 6px -2px rgba(20,40,30,.25)', '0 2px 6px -2px rgba(0,0,0,.5)', '分段控件选中'),
  c('supplement', '--sidebar', '#151518', '#0a0a0b', '后台侧栏（两种模式下都是深底）'),
  c('supplement', '--sidebar-active', '#26262a', '#1f1f22', '侧栏当前菜单底'),
  c('supplement', '--sidebar-text', '#ecebe7', '#ecebe7', '侧栏字标、当前菜单'),
  c('supplement', '--sidebar-text-2', '#9a988f', '#9a988f', '侧栏未选中菜单'),
  c('supplement', '--sidebar-text-3', '#8a8880', '#8a8880', '侧栏 ADMIN 标记'),
  c('supplement', '--sidebar-brand', '#e46e52', '#e46e52', '侧栏 Logo 点与当前菜单圆点'),
]

interface ScaleToken {
  name: string
  value: string
  use: string
}

const s = (name: string, value: string, use: string): ScaleToken => ({ name, value, use })

interface TypeToken {
  name: string
  size: string
  weight: number
  tracking: string
  sample: string
  spec: string
}

export const TYPE_SCALE: readonly TypeToken[] = [
  { name: 'display', size: '36px', weight: 600, tracking: '-0.025em', sample: '设计规范', spec: '36 / 600 / −2.5% · 仅用于营销和空状态' },
  { name: 'h1', size: '24px', weight: 600, tracking: '-0.015em', sample: '我的订阅', spec: '24 / 600 / −1.5% · 页面标题' },
  { name: 'h2', size: '18px', weight: 600, tracking: '-0.01em', sample: '订阅地址', spec: '18 / 600 · 区块标题' },
  { name: 'h3', size: '15px', weight: 600, tracking: '0', sample: '标准版 · 月付', spec: '15 / 600 · 卡片标题' },
  { name: 'body', size: '14px', weight: 400, tracking: '0', sample: '在已登录的设备上打开快捷登录生成链接。', spec: '14 / 400 / 行高 1.55 · 门户正文' },
  { name: 'body-sm', size: '13px', weight: 400, tracking: '0', sample: '门户会立即隐藏这条公告。', spec: '13 / 400 / 行高 1.5 · 后台正文' },
  { name: 'caption', size: '12px', weight: 400, tracking: '0', sample: '到期 2026-10-31 · 剩余 38 天', spec: '12 / 400 · 中文最小字号：标签、表头、说明' },
  { name: 'micro', size: '11px', weight: 500, tracking: '0', sample: '3 · VLESS · ADMIN', spec: '11 / 500 · 只用于数字和英文：角标、协议名' },
]

export const SPACE_SCALE: readonly ScaleToken[] = [
  s('--space-4', '4px', '图标与文字'),
  s('--space-8', '8px', '按钮组、标签组'),
  s('--space-12', '12px', '表单字段之间'),
  s('--space-16', '16px', '卡片之间、列表行左右、后台卡片内边距'),
  s('--space-20', '20px', '门户卡片内边距（含套餐卡）、标题与内容'),
  s('--space-24', '24px', '卡片内分组之间'),
  s('--space-32', '32px', '页面顶部'),
  s('--space-64', '64px', '页面底部'),
]

export const RADIUS_SCALE: readonly ScaleToken[] = [
  s('--radius-tag', '5px', '所有标签（统一方角，不用胶囊）'),
  s('--radius-sm', '7px', '后台控件、菜单项'),
  s('--radius-md', '9px', '门户按钮、输入框'),
  s('--radius-lg', '12px', '菜单、Toast、后台卡片'),
  s('--radius-xl', '14px', '门户卡片、弹窗'),
  s('--radius-pill', '999px', '余额、角标、头像、开关'),
]

// ---------------------------------------------------------------------------
// 角色令牌：规范「门户：朱砂用在哪里」「后台：保持中性」与控件高度表
// ---------------------------------------------------------------------------
interface RoleToken {
  name: string
  portal: string
  admin: string
  use: string
}

const r = (name: string, portal: string, admin: string, use: string): RoleToken => ({ name, portal, admin, use })

export const ROLE_TOKENS: readonly RoleToken[] = [
  r('--accent', 'var(--brand)', 'var(--brand)', '主按钮、开关、选中、进度'),
  r('--accent-hover', 'var(--brand-hover)', 'var(--brand-hover)', '主按钮悬停'),
  r('--on-accent', 'var(--on-brand)', 'var(--on-brand)', '主按钮上的字'),
  r('--focus-halo', 'var(--brand-soft)', 'transparent', '输入框聚焦外圈 3px（后台没有外圈）'),
  r('--focus-ring', 'var(--brand)', 'var(--info)', '键盘焦点外圈'),
  r('--link', 'var(--brand)', 'var(--info)', '文字链接'),
  r('--link-hover', 'var(--brand-ink)', 'var(--info)', '链接悬停'),
  r('--link-decoration', 'none', 'none', '链接下划线'),
  r('--link-underline', 'transparent', 'transparent', '下划线颜色'),
  r('--link-hover-decoration', 'underline', 'none', '链接悬停时的下划线（后台不加）'),
  r('--body-size', 'var(--fs-body)', 'var(--fs-body)', '正文字号'),
  r('--body-leading', 'var(--lh-body)', 'var(--lh-body-sm)', '正文行高 1.55 / 1.5'),
  r('--h-control', '50px', '32px', '门户主按钮与输入框（样稿 v5 50 高）/ 后台默认按钮与输入框'),
  r('--h-control-sm', '38px', '28px', '门户小按钮 / 后台工具栏与表格内'),
  r('--h-control-xs', '32px', '24px', '门户顶栏控件 / 后台紧凑按钮'),
  r('--pad-button', '22px', '12px', '按钮水平内边距（实心按钮 +2）'),
  r('--pad-input', '12px', '10px', '输入框水平内边距'),
  r('--h-dialog-action', '50px', '32px', '弹窗按钮高度'),
  r('--switch-knob-on', '#fff', '#fff', '开关选中时的圆钮'),
  r('--radius-control', '14px', 'var(--radius-sm)', '按钮、输入框圆角 14 / 7'),
  r('--radius-card', 'var(--radius-xl)', 'var(--radius-lg)', '卡片圆角 14 / 12'),
  r('--pad-card', 'var(--space-20)', 'var(--space-16)', '卡片内边距 20 / 16'),
  r('--control-border-width', '1px', '1px', '输入框边框宽度'),
  r('--control-bg', 'var(--bg)', 'var(--surface)', '输入框底色'),
  r('--popover-border', 'var(--border)', 'transparent', '菜单、弹窗、下拉列表的边框色（后台 --shadow 自带描边圈）'),
  r('--popover-shadow', 'var(--shadow)', 'var(--shadow)', '浮层阴影'),
  r('--label-size', 'var(--fs-caption)', 'var(--fs-body-sm)', '表单字段标签字号'),
  r('--label-color', 'var(--text-2)', 'var(--text-2)', '表单字段标签颜色'),
  r('--label-weight', 'var(--fw-regular)', 'var(--fw-regular)', '表单字段标签字重'),
  r('--font-ui', 'var(--font-system)', 'var(--font-sans)', '正文字体：门户系统字体 / 后台 Geist'),
  r('--button-weight', 'var(--fw-semibold)', 'var(--fw-medium)', '按钮字重'),
  r('--button-strong-weight', 'var(--fw-semibold)', 'var(--fw-medium)', '实心按钮字重'),
  r('--button-font-size', '15.5px', '13.5px', '按钮字号'),
  r('--button-font-size-sm', '14px', '13px', '小号按钮字号'),
  r('--pad-button-sm', '14px', '10px', '小号按钮水平内边距'),
  r('--button-primary-bg', 'linear-gradient(180deg, var(--brand), var(--brand-deep))', 'var(--accent)', '主按钮底'),
  r('--button-primary-hover-bg', 'linear-gradient(180deg, var(--brand), var(--brand-deep))', 'var(--accent-hover)', '主按钮悬停底'),
  r('--button-primary-border', 'color-mix(in srgb, var(--brand-deep) 55%, var(--shade))', 'transparent', '主按钮边框'),
  r('--button-primary-shadow', 'inset 0 1px 0 rgba(255, 255, 255, 0.22), 0 10px 22px -12px color-mix(in srgb, var(--shade) 90%, transparent)', 'none', '主按钮高光与投影'),
  r('--button-secondary-border', 'transparent', 'var(--border-strong)', '次按钮边框'),
  r('--button-secondary-shadow', '0 0 0 1px var(--border), 0 6px 14px -10px color-mix(in srgb, var(--shade) 40%, transparent)', 'none', '次按钮描边与投影（样稿 .btn.soft）'),
  r('--button-secondary-hover-bg', 'var(--surface)', 'var(--surface-2)', '次按钮悬停底'),
  r('--button-hover-filter', 'brightness(1.1)', 'none', '实心与次按钮悬停提亮'),
  r('--button-press', 'translateY(1px)', 'none', '按下位移'),
  r('--control-border-color', 'var(--border)', 'var(--border-strong)', '输入框边框色'),
  r('--dialog-radius', '26px', 'var(--radius-card)', '弹窗圆角（窄屏底部抽屉的上两角同值）'),
  r('--dialog-title-size', '18px', '16px', '弹窗标题字号'),
  r('--dialog-backdrop-filter', 'blur(3px)', 'none', '弹窗遮罩模糊'),
  r('--dialog-action-flex', '1', '0 1 auto', '弹窗底部按钮：门户平分整行'),
  r('--dialog-grow', '0', '1', '弹窗底部左右之间的弹性空白'),
  r('--segment-pad', '4px', '2px', '分段控件外框内边距'),
  r('--segment-radius', '14px', '8px', '分段控件外框圆角'),
  r('--segment-bg', 'color-mix(in srgb, var(--surface) 80%, transparent)', 'var(--bg)', '分段控件底'),
  r('--segment-ring', '0 0 0 1px var(--border)', '0 0 0 1px var(--border-strong)', '分段控件外框描边'),
  r('--segment-h', '36px', '24px', '分段选项高度'),
  r('--segment-option-pad', '18px', '10px', '分段选项水平内边距'),
  r('--segment-option-radius', '10px', '6px', '分段选项圆角'),
  r('--segment-font-size', '14px', '12.5px', '分段选项字号'),
  r('--segment-on-weight', 'var(--fw-semibold)', 'var(--fw-medium)', '选中项字重'),
  r('--segment-bg-sm', 'var(--bg)', 'var(--bg)', '小号分段底（样稿 .me-theme）'),
  r('--segment-h-sm', '30px', '22px', '小号分段选项高度'),
  r('--segment-option-pad-sm', '14px', '8px', '小号分段选项水平内边距'),
  r('--segment-font-size-sm', '13px', 'var(--fs-caption)', '小号分段选项字号'),
  r('--page-bg', 'radial-gradient(var(--border) 1px, transparent 1.3px) 0 0 / 16px 16px, var(--bg)', 'var(--bg)', '页面底：门户点阵底纹（样稿 body）'),
  r('--control-focus-border', 'var(--accent)', 'var(--text-3)', '输入框聚焦时的边框色'),
  r('--switch-on', 'var(--accent)', 'var(--ok)', '开关打开时的轨道色（后台绿）'),
  r('--switch-w', '32px', '30px', '开关轨道宽（高都是 18）'),
  r('--segment-on-bg', 'var(--surface)', 'var(--surface-3)', '分段控件选中项的底'),
  r('--tag-h', 'auto', '18px', '标签高度'),
  r('--tag-pad', '2px 8px', '0 6px', '标签内边距'),
  r('--tag-font-size', 'var(--fs-caption)', '11.5px', '标签字号'),
  r('--tag-weight', 'var(--fw-regular)', 'var(--fw-medium)', '标签字重'),
  r('--button-danger-bg', 'var(--danger)', 'var(--surface)', '危险按钮底（后台红描边白底）'),
  r('--button-danger-color', 'var(--on-danger)', 'var(--danger)', '危险按钮字色'),
  r('--button-danger-border', 'transparent', 'var(--danger-line)', '危险按钮边框'),
  r('--button-danger-hover-bg', 'var(--danger)', 'var(--danger-soft)', '危险按钮悬停底'),
]

// ---------------------------------------------------------------------------
// 后台在全局令牌上的覆盖（样稿 v4：Vercel 式黑白灰）：后台没有品牌色，
// --brand 系是墨色（与主按钮同色），蓝色用 --info（链接、焦点圈、进行中）。
// 只作用于 <html data-app="admin">，写在 roles.css 的 html[data-app='admin'] 两块里；
// 不进 COLOR_TOKENS（那是 Go 端与门户主题共用的键集）。
// 圆角另见 ADMIN_RADIUS。
// ---------------------------------------------------------------------------
export interface AdminOverride {
  name: string
  light: string
  dark: string
}

const o = (name: string, light: string, dark: string): AdminOverride => ({ name, light, dark })

export const ADMIN_OVERRIDES: readonly AdminOverride[] = [
  o('--bg', '#fafafa', '#000'),
  o('--surface', '#fff', '#0a0a0a'),
  o('--surface-2', '#f4f4f4', '#161616'),
  o('--surface-3', '#ebebeb', '#1f1f1f'),
  o('--border', '#ebebeb', '#1f1f1f'),
  o('--border-strong', '#e0e0e0', '#2e2e2e'),
  o('--border-hover', '#c9c9c9', '#454545'),
  o('--text', '#171717', '#ededed'),
  o('--text-2', '#666', '#a1a1a1'),
  o('--text-3', '#737373', '#8f8f8f'),
  o('--brand', '#171717', '#ededed'),
  o('--brand-hover', '#383838', '#fff'),
  o('--brand-ink', '#171717', '#ededed'),
  o('--brand-soft', '#ebebeb', '#1f1f1f'),
  o('--brand-tint', '#f4f4f4', '#161616'),
  o('--on-brand', '#fff', '#0a0a0a'),
  o('--brand-deep', '#171717', '#ededed'),
  o('--brand-line', '#c9c9c9', '#454545'),
  o('--ok', '#1a8a4f', '#3ecf8e'),
  o('--ok-soft', '#e8f7ee', 'rgba(62,207,142,.12)'),
  o('--ok-line', '#b8e6cb', 'rgba(62,207,142,.35)'),
  o('--warn', '#a35200', '#f5a524'),
  o('--warn-soft', '#fff4d6', 'rgba(245,165,36,.12)'),
  o('--warn-line', '#f5d9a3', 'rgba(245,165,36,.35)'),
  o('--danger', '#d93036', '#ff5f56'),
  o('--danger-soft', '#fff0f0', 'rgba(255,95,86,.1)'),
  o('--danger-line', '#f7c5c7', 'rgba(255,95,86,.35)'),
  o('--danger-tint', '#fff7f7', 'rgba(255,95,86,.06)'),
  o('--info', '#0070f3', '#3291ff'),
  o('--info-soft', '#ebf4ff', 'rgba(50,145,255,.12)'),
  o('--info-line', '#c2dcfc', 'rgba(50,145,255,.35)'),
  o('--on-ink', '#fff', '#0a0a0a'),
  o('--hdr', 'rgba(250,250,250,.85)', 'rgba(0,0,0,.85)'),
  o('--scrim', 'rgba(0,0,0,.18)', 'rgba(0,0,0,.5)'),
  o('--shadow', '0 0 0 1px #ebebeb,0 12px 32px -12px rgba(0,0,0,.18)', '0 0 0 1px #2e2e2e,0 16px 40px -12px rgba(0,0,0,.8)'),
  o('--shadow-segment', 'none', 'none'),
  o('--shade', '#000', '#000'),
]

/** 后台圆角（只写在 html[data-app='admin'] 浅色块，深浅色相同） */
export const ADMIN_RADIUS: readonly { name: string; value: string }[] = [
  { name: '--radius-tag', value: '4px' },
  { name: '--radius-sm', value: '7px' },
  { name: '--radius-md', value: '10px' },
  { name: '--radius-lg', value: '12px' },
  { name: '--radius-xl', value: '12px' },
  { name: '--radius-pill', value: '999px' },
]

/** 比较 CSS 值时忽略空白、大小写与小数点前的 0：rgba(20, 20, 24, 0.4) ≡ rgba(20,20,24,.4)。 */
export function normalizeCssValue(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/\s*,\s*/g, ',')
    .replace(/\s+/g, ' ')
    .replace(/(^|[^\d.])0\.(\d)/g, '$1.$2')
    .replace(/'/g, '"')
}
