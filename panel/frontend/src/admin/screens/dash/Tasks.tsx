import { formatCount } from '../../../core/format'
import { Skeleton } from '../../../ui'
import { useDashboardTasks } from '../../tasks'
import { useBacklog } from './api'
import css from './Dash.module.css'
import { taskCards, type TaskCard } from './model'
import { CardError, Dot, targetHref } from './parts'

export function Tasks() {
  const tasks = useDashboardTasks()
  const backlog = useBacklog()

  const loading = tasks.isPending || backlog.isPending
  const cards = taskCards(tasks.data?.items, backlog.data)
  const pending = cards.filter((c) => c.pending).length
  const summary = pending === 0 ? '眼下没有要处理的事' : `${pending} 项 · 点一下直接去处理`

  return (
    <section className={css.section} aria-labelledby="dash-tasks">
      <div className={css.sectionHead}>
        <h2 id="dash-tasks" className={css.sectionTitle}>
          需要处理
        </h2>
        {!loading && cards.length > 0 && <span className={css.hint}>{summary}</span>}
      </div>
      {tasks.isError && <CardError boxed what="待办计数" error={tasks.error} onRetry={() => void tasks.refetch()} />}
      {backlog.isError && <CardError boxed what="通知积压" error={backlog.error} onRetry={() => void backlog.refetch()} />}
      {(loading || cards.length > 0) && (
        <div className={css.taskGrid}>
          {loading
            ? Array.from({ length: 5 }, (_, i) => <Skeleton key={i} height={126} radius="var(--radius-lg)" />)
            : cards.map((card) => <TaskTile key={card.key} card={card} />)}
        </div>
      )}
    </section>
  )
}

function TaskTile({ card }: { card: TaskCard }) {
  return (
    <a className={css.task} href={targetHref(card.target)} title={card.hint}>
      <span className={css.taskHead}>
        <Dot tone={card.tone} />
        <span>{card.title}</span>
      </span>
      <span className={css.taskCount}>{formatCount(card.count)}</span>
      <span className={css.taskSub}>{card.sub}</span>
      <span className={css.taskAct}>{card.action} →</span>
    </a>
  )
}
