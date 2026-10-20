import { ArrowRight01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { formatCount } from '../../../core/format'
import { Card } from '../../../ui'
import { useDashboardTasks } from '../../tasks'
import css from './Dash.module.css'
import { todoBoard } from './model'
import { CardError, PanelSkeleton, Section, targetHref } from './parts'

export function Tasks() {
  const { data, isPending, isError, error, refetch } = useDashboardTasks()
  const board = todoBoard(data?.items, data?.as_of)
  const gridClass = board.cards.length === 4 ? css.todo4 : css.todo3

  return (
    <Section id="dash-todo" title="要处理" sub={data ? board.summary : undefined}>
      <Card flush>
        {isPending ? (
          <PanelSkeleton rows={3} height={36} />
        ) : isError && !data ? (
          <CardError what="待办计数" error={error} onRetry={() => void refetch()} />
        ) : (
          <div className={`${gridClass} ${css.todo}`}>
            {board.cards.map((card) => (
              <a
                key={card.key}
                className={card.hot ? `${css.tc} ${css.tcHot}` : css.tc}
                href={targetHref(card.target)}
              >
                <span className={css.k}>{card.title}</span>
                <span className={css.n}>{formatCount(card.count)}</span>
                <span className={css.m}>{card.meta}</span>
                <span className={css.go}>
                  {card.action}
                  <HugeiconsIcon icon={ArrowRight01Icon} size={14} strokeWidth={1.8} aria-hidden="true" />
                </span>
              </a>
            ))}
          </div>
        )}
      </Card>
    </Section>
  )
}
