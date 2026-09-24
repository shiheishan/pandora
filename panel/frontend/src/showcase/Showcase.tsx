/**
 * [INPUT]: 依赖 ../core/theme 的 useTheme / setTheme，依赖 ../styles/design-tokens 的设计稿原值与 normalizeCssValue，依赖 ../ui 的 Segmented，依赖 ./ComponentsDemo 与 ./Showcase.module.css
 * [OUTPUT]: 对外提供 Showcase 组件
 * [POS]: showcase 的页面本体：按设计规范 02 颜色、03 字体、04 尺寸的顺序铺开令牌，并在浏览器里把算出的值与设计稿逐条核对；05 角色令牌，06 组件演示（ComponentsDemo）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type ReactNode } from 'react'
import { setTheme, useTheme, type Theme } from '../core/theme'
import {
  COLOR_TOKENS,
  RADIUS_SCALE,
  ROLE_TOKENS,
  SPACE_SCALE,
  TYPE_SCALE,
  normalizeCssValue,
  type ColorToken,
  type TokenGroup,
} from '../styles/design-tokens'
import { Segmented } from '../ui'
import { ComponentsDemo } from './ComponentsDemo'
import css from './Showcase.module.css'

type App = 'portal' | 'admin'

interface Mismatch {
  name: string
  expected: string
  actual: string
}

// ---------------------------------------------------------------------------
// 浏览器核对：custom property 的计算值会把 var() 代换掉，所以角色令牌拿
// 「它引用的那个令牌此刻的值」作期望。读 DOM 放在渲染里：主题和入口的切换
// 都在事件里先改 <html> 属性再触发渲染，这里读到的总是最新值。
// ---------------------------------------------------------------------------
function verify(theme: Theme, app: App): { checked: number; mismatches: Mismatch[] } {
  const computed = getComputedStyle(document.documentElement)
  const value = (name: string) => normalizeCssValue(computed.getPropertyValue(name))
  const expectations = [
    ...COLOR_TOKENS.map((t) => ({ name: t.name, expected: normalizeCssValue(t[theme]) })),
    ...[...SPACE_SCALE, ...RADIUS_SCALE].map((t) => ({ name: t.name, expected: t.value })),
    ...TYPE_SCALE.map((t) => ({ name: `--fs-${t.name}`, expected: t.size })),
    ...ROLE_TOKENS.map((t) => {
      const ref = /^var\((--[\w-]+)\)$/.exec(t[app])
      return { name: t.name, expected: ref ? value(ref[1]!) : normalizeCssValue(t[app]) }
    }),
  ]
  const mismatches = expectations
    .map(({ name, expected }) => ({ name, expected, actual: value(name) }))
    .filter((m) => m.actual !== m.expected)
  return { checked: expectations.length, mismatches }
}

function Section(props: { no: string; title: string; children: ReactNode }) {
  return (
    <section className={css.section}>
      <header className={css.sectionHead}>
        <span className="mono">{props.no}</span>
        <h2>{props.title}</h2>
      </header>
      {props.children}
    </section>
  )
}

const GROUPS: readonly (readonly [TokenGroup, string])[] = [
  ['brand', '品牌色 · 朱砂'],
  ['status', '状态色'],
  ['overlay', '反色、遮罩与浮层'],
  ['supplement', '规范补充：危险按钮字、Toast 圆点、后台侧栏'],
]

function Swatch({ token, theme }: { token: ColorToken; theme: Theme }) {
  const isShadow = token.name.startsWith('--shadow')
  return (
    <div className={css.swatch}>
      <div
        className={css.chip}
        style={isShadow ? { boxShadow: `var(${token.name})`, background: 'var(--surface)' } : { background: `var(${token.name})` }}
      />
      <div className={css.swatchText}>
        <span className="mono">{token.name}</span>
        <span className={`mono ${css.muted}`}>{token[theme]}</span>
        <span className={css.muted}>{token.use}</span>
      </div>
    </div>
  )
}

const TAGS = [
  ['在线', 'var(--ok-soft)', 'var(--ok)'],
  ['待审核', 'var(--warn-soft)', 'var(--warn)'],
  ['离线', 'var(--danger-soft)', 'var(--danger)'],
  ['处理中', 'var(--info-soft)', 'var(--info)'],
  ['已关闭', 'var(--surface-3)', 'var(--text-2)'],
  ['当前套餐', 'var(--brand-soft)', 'var(--brand-ink)'],
  ['推荐', 'var(--brand)', 'var(--on-brand)'],
] as const

export function Showcase() {
  const theme = useTheme()
  const [app, setAppState] = useState<App>(() =>
    document.documentElement.getAttribute('data-app') === 'admin' ? 'admin' : 'portal',
  )
  const setApp = (next: App) => {
    document.documentElement.setAttribute('data-app', next)
    setAppState(next)
  }
  const report = verify(theme, app)

  return (
    <div className={css.page}>
      <header className={css.top}>
        <div className={css.titleRow}>
          <span className={css.wordmark}>pandora</span>
          <span className={`mono ${css.muted}`}>SHOWCASE · 仅开发</span>
        </div>
        <h1 className={css.h1}>令牌与组件演示</h1>
        <div className={css.controls}>
          <Segmented
            label="主题"
            value={theme}
            onChange={setTheme}
            options={[
              { value: 'light', label: '浅色' },
              { value: 'dark', label: '深色' },
            ]}
          />
          <Segmented
            label="入口"
            value={app}
            onChange={setApp}
            options={[
              { value: 'portal', label: '门户' },
              { value: 'admin', label: '后台' },
            ]}
          />
          <span className={report.mismatches.length ? css.badBadge : css.okBadge} data-testid="token-report">
            {report.mismatches.length
              ? `${report.mismatches.length} / ${report.checked} 个令牌与设计稿不一致`
              : `${report.checked} 个令牌与设计稿一致`}
          </span>
        </div>
        {report.mismatches.length > 0 && (
          <ul className={css.mismatches}>
            {report.mismatches.map((m) => (
              <li key={m.name} className="mono">
                {m.name}：期望 {m.expected}，实际 {m.actual || '（未定义）'}
              </li>
            ))}
          </ul>
        )}
      </header>

      <Section no="02" title="颜色">
        <h3 className={css.h3}>中性色 · 浅色 / 深色</h3>
        <div className={css.table}>
          <div className={css.tableHead}>
            <span>令牌</span>
            <span>浅色</span>
            <span>深色</span>
            <span>用途</span>
          </div>
          {COLOR_TOKENS.filter((t) => t.group === 'neutral').map((t) => (
            <div key={t.name} className={css.tableRow}>
              <span className="mono">{t.name}</span>
              <span className={css.pair}>
                <i className={css.dot} style={{ background: t.light }} />
                <span className="mono">{t.light}</span>
              </span>
              <span className={css.pair}>
                <i className={css.dot} style={{ background: t.dark }} />
                <span className="mono">{t.dark}</span>
              </span>
              <span className={css.muted}>{t.use}</span>
            </div>
          ))}
        </div>
        {GROUPS.map(([group, title]) => (
          <div key={group} className={css.group}>
            <h3 className={css.h3}>{title}</h3>
            <div className={css.swatches}>
              {COLOR_TOKENS.filter((t) => t.group === group).map((t) => (
                <Swatch key={t.name} token={t} theme={theme} />
              ))}
            </div>
          </div>
        ))}
        <h3 className={css.h3}>标签配色</h3>
        <div className={css.tags}>
          {TAGS.map(([text, bg, fg]) => (
            <span key={text} className={css.tag} style={{ background: bg, color: fg }}>
              {text}
            </span>
          ))}
        </div>
      </Section>

      <Section no="03" title="字体">
        <div className={css.fontCards}>
          <div className={css.card}>
            <div className={css.fontSample}>Geist 永和九年</div>
            <div className={css.muted}>界面字体。中文回落 PingFang SC → 冬青黑 → Noto Sans SC → 微软雅黑。字重只用 400、500、600。</div>
          </div>
          <div className={css.card}>
            <div className={`mono ${css.fontSample}`}>PD-2609-3312</div>
            <div className={css.muted}>Geist Mono 只用于要逐字核对的内容。金额和流量用 Geist 加等宽数字：<span className="num">¥128.00 · 62 / 200 GB</span></div>
          </div>
        </div>
        <div className={css.table}>
          {TYPE_SCALE.map((t) => (
            <div key={t.name} className={css.typeRow}>
              <span className={`mono ${css.muted}`}>{t.name}</span>
              <span style={{ fontSize: `var(--fs-${t.name})`, fontWeight: t.weight, letterSpacing: t.tracking, lineHeight: 'var(--lh-heading)' }}>
                {t.sample}
              </span>
              <span className={css.muted}>{t.spec}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section no="04" title="间距 · 圆角 · 高度 · 阴影">
        <div className={css.fontCards}>
          <div className={css.card}>
            <h3 className={css.h3}>间距</h3>
            {SPACE_SCALE.map((t) => (
              <div key={t.name} className={css.scaleRow}>
                <span className="mono">{t.value}</span>
                <i className={css.bar} style={{ width: `calc(var(${t.name}) * 3)` }} />
                <span className={css.muted}>{t.use}</span>
              </div>
            ))}
          </div>
          <div className={css.card}>
            <h3 className={css.h3}>圆角</h3>
            <div className={css.radii}>
              {RADIUS_SCALE.map((t) => (
                <div key={t.name} className={css.radius}>
                  <i style={{ borderRadius: `var(${t.name})` }} />
                  <span className="mono">{t.value}</span>
                  <span className={css.muted}>{t.use}</span>
                </div>
              ))}
            </div>
          </div>
          <div className={css.card}>
            <h3 className={css.h3}>阴影：只给浮层用</h3>
            <div className={css.shadows}>
              <div>
                <i style={{ boxShadow: 'var(--shadow-segment)', borderRadius: 'var(--radius-sm)' }} />
                <span className={css.muted}>分段控件选中</span>
              </div>
              <div>
                <i style={{ boxShadow: 'var(--shadow), 0 0 0 1px var(--border)', borderRadius: 'var(--radius-lg)' }} />
                <span className={css.muted}>菜单、Toast、弹窗</span>
              </div>
              <div>
                <i style={{ background: 'var(--scrim)', borderRadius: 'var(--radius-lg)' }} />
                <span className={css.muted}>弹窗遮罩</span>
              </div>
            </div>
          </div>
        </div>
      </Section>

      <Section no="05" title={`角色令牌 · 当前入口：${app === 'portal' ? '门户' : '后台'}`}>
        <div className={css.roleDemo}>
          <span className={css.accentBlock} style={{ height: 'var(--h-control)' }}>
            主操作 · --h-control
          </span>
          <span className={css.ghostBlock} style={{ height: 'var(--h-control-sm)' }}>
            次操作 · --h-control-sm
          </span>
          <span className={css.ghostBlock} style={{ height: 'var(--h-control-xs)' }}>
            紧凑 · --h-control-xs
          </span>
          <span className={css.focusBlock}>聚焦的输入框</span>
          <a href="#top">文字链接</a>
        </div>
        <div className={css.table}>
          <div className={css.roleHead}>
            <span>令牌</span>
            <span>门户</span>
            <span>后台</span>
            <span>用途</span>
          </div>
          {ROLE_TOKENS.map((t) => (
            <div key={t.name} className={css.roleRow}>
              <span className="mono">{t.name}</span>
              <span className={`mono ${app === 'portal' ? '' : css.muted}`}>{t.portal}</span>
              <span className={`mono ${app === 'admin' ? '' : css.muted}`}>{t.admin}</span>
              <span className={css.muted}>{t.use}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section no="06" title={`组件 · 当前入口：${app === 'portal' ? '门户' : '后台'}`}>
        <ComponentsDemo />
      </Section>
    </div>
  )
}
