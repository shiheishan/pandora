import { ArrowUp01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { href } from '../../../core/router'
import { Button } from '../../../ui'
import type { TicketDetail } from './api'
import type { TicketControls } from './Controls'
import { CATEGORY_LABELS, CLOSED_REASON_LABELS, PRIORITY_LABELS, slaLines } from './model'
import { useNow } from './Queue'
import css from './Props.module.css'

export function Props({ t, controls }: { t: TicketDetail; controls: TicketControls }) {
  const now = useNow()
  const sla = slaLines(t, now)

  return (
    <aside className={css.props} aria-label="工单属性">
      <h4>处理</h4>
      <div className={css.pr}>
        <span>状态</span>
        <div className={css.fieldBox}>
          {controls.status}
          {t.closed_reason && (
            <div className={css.closedReason}>
              关闭原因：{CLOSED_REASON_LABELS[t.closed_reason]}
            </div>
          )}
        </div>
      </div>
      <div className={css.pr}>
        <span>指派</span>
        <div className={css.fieldBox}>{controls.assign}</div>
      </div>
      <div className={css.pr}>
        <span>优先级</span>
        <div className={css.v}>
          <i className={`${css.pd} ${css[`pd_${t.priority}`]}`} />
          <span>{PRIORITY_LABELS[t.priority]}</span>
        </div>
      </div>
      <div className={css.pr}>
        <span>分类</span>
        <div className={css.v}>
          <span>{CATEGORY_LABELS[t.category]}</span>
        </div>
      </div>

      <h4>时限</h4>
      <div className={css.sla}>
        {sla.map((l) => (
          <div key={l.label} className={css.slaRow}>
            <span className={css.slaLabel}>{l.label}</span>
            <span className={css[`sla_${l.tone}`]}>{l.text}</span>
          </div>
        ))}
      </div>

      <h4>用户</h4>
      <div className={css.pr}>
        <span>邮箱</span>
        <div className={css.v} title={t.user_email}>
          <span>{t.user_email}</span>
        </div>
      </div>
      <div className={css.pr}>
        <span>套餐</span>
        <div className={css.v} title={t.user_active_plan ?? '无在用套餐'}>
          <span>{t.user_active_plan ?? '无在用套餐'}</span>
        </div>
      </div>
      <div className={css.pr}>
        <span />
        <a className={css.link} href={href(`/users/list/${encodeURIComponent(t.user_id)}`)}>
          查看用户 →
        </a>
      </div>

      {t.related_order && (
        <>
          <h4>关联订单</h4>
          <div className={css.pr}>
            <span>订单</span>
            <a
              className={`${css.mono} ${css.link}`}
              href={href(`/billing/orders/${encodeURIComponent(t.related_order.id)}`)}
            >
              {t.related_order.order_no}
            </a>
          </div>
        </>
      )}

      <Button
        variant="secondary"
        className={css.escalate}
        disabled={controls.escalate.escalated}
        onClick={controls.escalate.open}
        icon={ArrowUp01Icon}
      >
        {controls.escalate.escalated ? '已升级到 L2' : '升级到 L2'}
      </Button>
      <p className={css.hint}>升级会把优先级提到至少「高」，并在对话里留一条系统记录。</p>
    </aside>
  )
}
