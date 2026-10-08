import { href, navigate } from '../../../core/router'
import { Card } from '../../../ui'
import { APP_UPDATE, guideApps, PROFILE_NAME_HINT, UPDATE_FALLBACK, UPDATE_GUIDE_PARAM } from '../common/app-update'
import { DEVICE_SAY, DEVICES, type Device } from '../common/clients'
import { Chips } from '../common/Flow'
import css from './Help.module.css'

/** 「iPhone / iPad 上怎么点更新」：西文结尾补一个空格 */
export const guideTitle = (d: Device) => `${DEVICE_SAY[d]}${/[A-Za-z]$/.test(DEVICE_SAY[d]) ? ' ' : ''}上怎么点更新`

/** 目录最上面的内置一组「在 App 里更新」：每种设备一篇，和帮助文章并列 */
export function UpdateGuideToc({ current }: { current: Device | null }) {
  return (
    <Card flush className={css.toc}>
      <nav aria-label="在 App 里更新">
        <section className={css.group}>
          <h3 className={css.groupTitle}>在 App 里更新</h3>
          <ul className={css.items}>
            {DEVICES.map((d) => (
              <li key={d}>
                <a className={css.item} href={href('/help', { [UPDATE_GUIDE_PARAM]: d })} aria-current={d === current ? 'page' : undefined}>
                  {guideTitle(d)}
                </a>
              </li>
            ))}
          </ul>
        </section>
      </nav>
    </Card>
  )
}

/**
 * 内置说明（不是后台写的文章）：这台设备上推荐的每个 App 三步——在哪个页面、点哪个按钮、看到什么算成功，
 * 末尾给退路。完成页与换新链接页「点一次更新」旁边链接到这里。
 */
export function UpdateGuide({ device }: { device: Device }) {
  return (
    <Card className={css.article}>
      <a className={css.back} href={href('/help')}>
        ← 全部文章
      </a>
      <article className={css.body} data-guide={device}>
        <h2 className={css.title}>在 App 里点一次「更新」</h2>
        <Chips
          label="你的设备"
          items={DEVICES.map((d) => ({ key: d, label: DEVICE_SAY[d] }))}
          selected={device}
          onSelect={(d) => navigate('/help', { query: { [UPDATE_GUIDE_PARAM]: d }, replace: true })}
        />
        <p className={css.para}>换了套餐、改了名字或者节点有变化时，在 App 里更新一次就能看到，不用重新添加。{PROFILE_NAME_HINT}</p>
        {guideApps(device).map((app) => {
          const s = APP_UPDATE[app]
          return s ? (
            <section key={app} className={css.guideApp} id={`guide-${app.replace(/\s+/g, '-')}`}>
              <h3 className={css.heading}>{app}</h3>
              <ol className={css.steps}>
                <li>{s.where}</li>
                <li>{s.tap}</li>
                <li>{s.ok}</li>
              </ol>
            </section>
          ) : null
        })}
        <p className={css.para}>{UPDATE_FALLBACK}</p>
      </article>
    </Card>
  )
}
