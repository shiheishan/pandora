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
// 浅色取样稿 :root；样稿没画深色，深色按同一色相自配。后台没重做，变了值的键在
// ADMIN_OVERRIDES 里钉回原来的「纸白」，后台看起来不变。
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
  r('--accent', 'var(--brand)', 'var(--text)', '主按钮、开关、选中、进度'),
  r('--accent-hover', 'var(--brand-hover)', 'var(--text)', '主按钮悬停'),
  r('--on-accent', 'var(--on-brand)', 'var(--on-ink)', '主按钮上的字'),
  r('--focus-halo', 'var(--brand-soft)', 'transparent', '输入框聚焦外圈 3px（后台没有）'),
  r('--focus-ring', 'var(--brand)', 'var(--text)', '键盘焦点外圈'),
  r('--link', 'var(--brand)', 'var(--text)', '文字链接'),
  r('--link-hover', 'var(--brand-ink)', 'var(--text-2)', '链接悬停'),
  r('--link-decoration', 'none', 'underline', '链接下划线'),
  r('--link-underline', 'transparent', 'var(--border-hover)', '下划线颜色'),
  r('--body-size', 'var(--fs-body)', 'var(--fs-body-sm)', '正文字号 14 / 13'),
  r('--body-leading', 'var(--lh-body)', 'var(--lh-body-sm)', '正文行高 1.55 / 1.5'),
  r('--h-control', '50px', '32px', '门户主按钮与输入框（样稿 v5 50 高）/ 后台默认按钮与输入框'),
  r('--h-control-sm', '38px', '28px', '门户小按钮 / 后台工具栏与表格内'),
  r('--h-control-xs', '32px', '24px', '门户顶栏控件 / 后台紧凑按钮'),
  r('--pad-button', '22px', '12px', '按钮水平内边距（实心按钮 +2）'),
  r('--pad-input', '12px', '2px', '输入框水平内边距（后台只有下边线）'),
  r('--h-dialog-action', '50px', '32px', '弹窗按钮高度'),
  r('--switch-knob-on', '#fff', 'var(--bg)', '开关选中时的圆钮'),
  r('--radius-control', '14px', 'var(--radius-sm)', '按钮、输入框圆角 14 / 7'),
  r('--radius-card', 'var(--radius-xl)', 'var(--radius-lg)', '卡片圆角 14 / 12'),
  r('--pad-card', 'var(--space-20)', 'var(--space-16)', '卡片内边距 20 / 16'),
  r('--control-border-width', '1px', '0 0 1px', '输入框边框：四边 / 只有下边线'),
  r('--control-bg', 'var(--bg)', 'transparent', '输入框底色（门户样稿是页面底色的浅灰）'),
  r('--popover-border', 'var(--border)', 'var(--text)', '菜单、弹窗、下拉列表的边框色'),
  r('--popover-shadow', 'var(--shadow)', 'none', '浮层阴影'),
  r('--label-size', 'var(--fs-caption)', 'var(--fs-body-sm)', '表单字段标签字号'),
  r('--label-color', 'var(--text-2)', 'var(--text)', '表单字段标签颜色'),
  r('--label-weight', 'var(--fw-regular)', 'var(--fw-medium)', '表单字段标签字重'),
  r('--font-ui', 'var(--font-system)', 'var(--font-sans)', '正文字体：门户系统字体 / 后台 Geist'),
  r('--button-weight', 'var(--fw-semibold)', 'var(--fw-regular)', '按钮字重'),
  r('--button-strong-weight', 'var(--fw-semibold)', 'var(--fw-medium)', '实心按钮字重'),
  r('--button-font-size', '15.5px', '1em', '按钮字号（1em 即跟随上下文，与后台现在一致）'),
  r('--button-font-size-sm', '14px', '1em', '小号按钮字号（后台另有 admin 选择器压成 12）'),
  r('--pad-button-sm', '14px', 'calc(var(--pad-button) - 2px)', '小号按钮水平内边距'),
  r('--button-primary-bg', 'linear-gradient(180deg, var(--brand), var(--brand-deep))', 'var(--accent)', '主按钮底'),
  r('--button-primary-hover-bg', 'linear-gradient(180deg, var(--brand), var(--brand-deep))', 'var(--accent-hover)', '主按钮悬停底'),
  r('--button-primary-border', 'color-mix(in srgb, var(--brand-deep) 55%, var(--shade))', 'transparent', '主按钮边框'),
  r('--button-primary-shadow', 'inset 0 1px 0 rgba(255, 255, 255, 0.22), 0 10px 22px -12px color-mix(in srgb, var(--shade) 90%, transparent)', 'none', '主按钮高光与投影'),
  r('--button-secondary-border', 'transparent', 'var(--border-strong)', '次按钮边框'),
  r('--button-secondary-shadow', '0 0 0 1px var(--border), 0 6px 14px -10px color-mix(in srgb, var(--shade) 40%, transparent)', 'none', '次按钮描边与投影（样稿 .btn.soft）'),
  r('--button-secondary-hover-bg', 'var(--surface)', 'var(--surface-3)', '次按钮悬停底'),
  r('--button-hover-filter', 'brightness(1.1)', 'none', '实心与次按钮悬停提亮'),
  r('--button-press', 'translateY(1px)', 'none', '按下位移'),
  r('--control-border-color', 'var(--border)', 'var(--border-strong)', '输入框边框色'),
  r('--dialog-radius', '26px', 'var(--radius-card)', '弹窗圆角（窄屏底部抽屉的上两角同值）'),
  r('--dialog-title-size', '18px', '16px', '弹窗标题字号'),
  r('--dialog-backdrop-filter', 'blur(3px)', 'none', '弹窗遮罩模糊'),
  r('--dialog-action-flex', '1', '0 1 auto', '弹窗底部按钮：门户平分整行'),
  r('--dialog-grow', '0', '1', '弹窗底部左右之间的弹性空白'),
  r('--segment-pad', '4px', '3px', '分段控件外框内边距'),
  r('--segment-radius', '14px', 'var(--radius-md)', '分段控件外框圆角'),
  r('--segment-bg', 'color-mix(in srgb, var(--surface) 80%, transparent)', 'var(--surface-3)', '分段控件底'),
  r('--segment-ring', '0 0 0 1px var(--border)', 'none', '分段控件外框描边'),
  r('--segment-h', '36px', '30px', '分段选项高度'),
  r('--segment-option-pad', '18px', '14px', '分段选项水平内边距'),
  r('--segment-option-radius', '10px', 'var(--radius-sm)', '分段选项圆角'),
  r('--segment-font-size', '14px', 'var(--fs-body-sm)', '分段选项字号'),
  r('--segment-on-weight', 'var(--fw-semibold)', 'var(--fw-medium)', '选中项字重'),
  r('--segment-bg-sm', 'var(--bg)', 'var(--surface-3)', '小号分段底（样稿 .me-theme）'),
  r('--segment-h-sm', '30px', '24px', '小号分段选项高度'),
  r('--segment-option-pad-sm', '14px', '10px', '小号分段选项水平内边距'),
  r('--segment-font-size-sm', '13px', 'var(--fs-caption)', '小号分段选项字号'),
  r('--page-bg', 'radial-gradient(var(--border) 1px, transparent 1.3px) 0 0 / 16px 16px, var(--bg)', 'var(--bg)', '页面底：门户点阵底纹（样稿 body）'),
]

// ---------------------------------------------------------------------------
// 后台在全局令牌上的覆盖（10-16 设计稿）：卡片与页面同底、所有圆角归零；
// 全局颜色换成门户森林绿后，变了值的颜色键也在这里钉回原来的「纸白」（后台还没重做）。
// 只作用于 <html data-app="admin">，写在 roles.css 的 html[data-app='admin'] 两块里；
// 不进 COLOR_TOKENS（那是 Go 端与门户主题共用的键集），dark 为 null 表示深色沿用 tokens.css。
// 圆角另见 ADMIN_RADIUS。
// ---------------------------------------------------------------------------
export interface AdminOverride {
  name: string
  light: string
  dark: string | null
}

const o = (name: string, light: string, dark: string | null = null): AdminOverride => ({ name, light, dark })

export const ADMIN_OVERRIDES: readonly AdminOverride[] = [
  o('--bg', '#f5f4f0', '#111113'),
  o('--surface', '#f5f4f0', '#111113'),
  o('--surface-2', '#efede8', '#18181b'),
  o('--surface-3', '#e8e6e0', '#202023'),
  o('--border', '#e3e1da', '#2b2b2f'),
  o('--border-strong', '#d5d2ca', '#37373c'),
  o('--border-hover', '#c9c6bd', '#4a4a50'),
  o('--text', '#1c1c1f', '#ecebe7'),
  o('--text-2', '#62615c', '#9a988f'),
  o('--text-3', '#6e6c66', '#8a8880'),
  o('--brand', '#b9442b', '#e46e52'),
  o('--brand-hover', '#a33a23', '#ec8267'),
  o('--brand-ink', '#8c3019', '#f2a58f'),
  o('--brand-soft', '#f6e4de', '#3a231c'),
  o('--brand-tint', '#fbf3ef', '#201816'),
  o('--on-brand', '#fff', '#170d0a'),
  o('--warn', '#9a5c00'),
  o('--warn-soft', '#fbf0dc'),
  o('--danger', '#b3263a'),
  o('--danger-soft', '#f8e1e3'),
  o('--on-ink', '#fff', '#111113'),
  o('--hdr', 'rgba(245,244,240,.9)', 'rgba(17,17,19,.88)'),
  o('--scrim', 'rgba(20,20,24,.4)'),
  o('--shadow', '0 16px 36px -14px rgba(28,28,31,.25)', '0 16px 40px -12px rgba(0,0,0,.6)'),
  o('--shadow-segment', '0 1px 2px rgba(20,20,24,.08)', '0 1px 2px rgba(20,20,24,.08)'),
]

/** 后台所有圆角归零，不分深浅（只写在 html[data-app='admin'] 那一块） */
export const ADMIN_RADIUS: readonly string[] = RADIUS_SCALE.map((t) => t.name)

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
