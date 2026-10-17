import { type KeyboardEvent } from 'react'
import { href, navigate } from '../../../core/router'
import { Btn, cx, Icon, IconDisc } from '../../kit'
import { APP_UPDATE, guideApps, PROFILE_NAME_HINT, UPDATE_FALLBACK, UPDATE_GUIDE_PARAM, thisDevice, type GuideText } from '../common/app-update'
import { DEVICE_SAY, DEVICES, type Device } from '../common/clients'
import css from './Help.module.css'

/** 目录最上面：一张卡片链到当前设备的内置「在 App 里点一次更新」 */
export function UpdateGuideToc({ current }: { current: Device | null }) {
  const device = current ?? thisDevice()
  return (
    <a className={css.guideCard} href={href('/help', { [UPDATE_GUIDE_PARAM]: device })} aria-current={current ? 'page' : undefined}>
      <IconDisc name="renew" tone="solid" />
      <span className={css.guideText}>
        <b>在 App 里点一次更新</b>
        <small>换了链接、节点不对时先做这个</small>
      </span>
      <Icon name="chev" size={18} className={css.guideChev} />
    </a>
  )
}

/**
 * 内置说明（不是后台写的文章）：这台设备上推荐的每个 App 三步——在哪个页面、点哪个按钮、看到什么算成功，
 * 末尾给退路。完成页与换新链接页「点一次更新」旁边链接到这里。
 * 页面上唯一的 <article>（浏览器测试靠它找 level 3 标题，所以 h3 只放 App 名）。
 */
export function UpdateGuide({ device }: { device: Device }) {
  // 按渲染顺序记下已经说明过的按钮名：同一篇里每个带说明的按钮只在第一次出现时补那半句
  const explained = new Set<string>()

  // 左右方向键切到上一个 / 下一个设备并聚焦（radio 组的惯例）
  function onDevKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    const radios = e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]')
    const current = Array.from(radios).indexOf(e.target as HTMLButtonElement)
    if (current < 0) return
    const step = e.key === 'ArrowRight' ? 1 : radios.length - 1
    const nextIndex = (current + step) % radios.length
    const next = DEVICES[nextIndex]
    if (!next) return
    e.preventDefault()
    radios[nextIndex]?.focus()
    navigate('/help', { query: { [UPDATE_GUIDE_PARAM]: next }, replace: true })
  }

  return (
    <article className={css.ha} data-guide={device}>
      <a className={css.back} href={href('/help')}>
        ‹ 全部文章
      </a>
      <div className={css.meta}>内置指南</div>
      <h2 className={css.title}>在 App 里点一次「更新」</h2>
      <div className={css.dev} role="radiogroup" aria-label="你的设备" onKeyDown={onDevKeyDown}>
        {DEVICES.map((d) => (
          <button
            key={d}
            type="button"
            role="radio"
            aria-checked={d === device}
            className={css.devBtn}
            onClick={() => navigate('/help', { query: { [UPDATE_GUIDE_PARAM]: d }, replace: true })}
          >
            {DEVICE_SAY[d]}
          </button>
        ))}
      </div>
      <p className={css.p}>换了套餐、改了名字或者节点有变化时，在 App 里更新一次就能看到，不用重新添加。</p>
      <p className={css.hint}>{PROFILE_NAME_HINT}</p>
      {guideApps(device).map((app) => {
        const s = APP_UPDATE[app]
        return s ? (
          <section key={app} className={css.ga} id={`guide-${app.replace(/\s+/g, '-')}`}>
            <h3 className={css.gaTitle}>{app}</h3>
            <ol className={css.steps}>
              <li>
                <span>
                  <GuideLine text={s.where} explained={explained} />
                </span>
              </li>
              <li>
                <span>
                  <GuideLine text={s.tap} explained={explained} />
                </span>
              </li>
              <li>
                <span>
                  <GuideLine text={s.ok} explained={explained} />
                </span>
              </li>
            </ol>
          </section>
        ) : null
      })}
      <p className={cx(css.p, css.fallback)}>{UPDATE_FALLBACK}</p>
      <div className={css.fb}>
        <span>还是不行？</span>
        <Btn tone="soft" size="sm" href={href('/tickets/new')}>
          提交工单
        </Btn>
      </div>
    </article>
  )
}

/**
 * 一步的文字：App 自己的按钮名、页面名照它的中文界面原样写，做成按钮样式（用户 10-08 定的例外）；
 * 带说明的按钮在这一篇里第一次出现时，后面补半句「（就是更新你添加的那条链接）」
 */
function GuideLine({ text, explained }: { text: GuideText; explained: Set<string> }) {
  if (typeof text === 'string') return <>{text}</>
  return (
    <>
      {text.map((part, i) => {
        if (typeof part === 'string') return <span key={i}>{part}</span>
        const note = part.note && !explained.has(part.button) ? part.note : null
        if (note) explained.add(part.button)
        return (
          <span key={i}>
            <kbd className={css.kbd}>{part.button}</kbd>
            {note && `（${note}）`}
          </span>
        )
      })}
    </>
  )
}
