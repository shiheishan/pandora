import { Tag } from '../../../ui'
import { CLIENT_FORMATS } from './subscription'
import css from './subscription.module.css'

export function SubscriptionTemplates() {
  return (
    <section className={css.section} aria-label="订阅模板">
      <header className={css.sectionHead}>
        <h3 className={css.sectionTitle}>订阅模板</h3>
        <p className={css.sectionHint}>按客户端 User-Agent 选格式；认不出的客户端给通用 URI（base64）。</p>
      </header>
      <ul className={css.formatList}>
        {CLIENT_FORMATS.map((row) => (
          <li key={row.clients} className={css.formatRow}>
            <span className={css.formatClients}>{row.clients}</span>
            <span className={css.formatUa} title={row.ua}>{row.ua}</span>
            <span className={css.formatName}>{row.format}</span>
            <span className={css.formatTag}>{row.fallback ? <Tag tone="ok">默认</Tag> : null}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}
