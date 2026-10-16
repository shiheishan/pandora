import { ArrowRight01Icon, ArrowRight02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { formatCount } from '../../../core/format'
import { Skeleton } from '../../../ui'
import { useDashboardTasks } from '../../tasks'
import { useBacklog } from './api'
import css from './Dash.module.css'
import { taskBoard, taskCards } from './model'
import { CardError, Dot, GroupHead, targetHref, toneText } from './parts'

export function Tasks() {
  const tasks = useDashboardTasks()
  const backlog = useBacklog()

  const loading = tasks.isPending || backlog.isPending
  const board = taskBoard(taskCards(tasks.data?.items, backlog.data))

  return (
    <section className={css.tasks} aria-labelledby="dash-tasks">
      <GroupHead id="dash-tasks" title="待办" hint={!loading && tasks.data ? board.summary : undefined} />
      {tasks.isError && <CardError what="待办计数" error={tasks.error} onRetry={() => void tasks.refetch()} />}
      {backlog.isError && <CardError what="通知积压" error={backlog.error} onRetry={() => void backlog.refetch()} />}
      {loading ? (
        <Skeleton height={180} />
      ) : tasks.isError && !tasks.data ? null : board.hero === null ? (
        <div className={css.tasksEmpty}>
          <Dot tone="ok" />
          <span className={css.tasksEmptyTitle}>没有待处理的事</span>
          <span className={css.tasksEmptyHint}>新工单、离线节点、支付异常会出现在这里</span>
        </div>
      ) : (
        <div className={css.tasksBoard}>
          <a className={css.heroCard} href={targetHref(board.hero.target)} title={board.hero.hint}>
            <span className={css.heroHead}>
              <Dot tone={board.hero.tone} />
              <span className={`${css.heroUrgent} ${toneText(board.hero.tone) ?? ''}`}>最紧急</span>
              <span className={css.heroTitle}>{board.hero.title}</span>
              <span className={css.spacer} />
              <span className={css.heroAction}>
                {board.hero.action}
                <HugeiconsIcon icon={ArrowRight02Icon} size={13} aria-hidden="true" />
              </span>
            </span>
            <span className={css.heroBody}>
              <span className={`${css.heroCount} ${toneText(board.hero.tone) ?? ''}`}>{formatCount(board.hero.count)}</span>
              <span className={css.heroUnit}>{board.hero.unit}</span>
            </span>
            {board.hero.stats.length > 0 && (
              <span className={css.heroStats}>
                {board.hero.stats.map((st, i) => (
                  <span key={st.label} className={i > 0 ? `${css.heroStat} ${css.heroStatBorder}` : css.heroStat}>
                    <span className={css.heroStatLabel}>{st.label}</span>
                    <span className={css.heroStatValue}>{st.value}</span>
                  </span>
                ))}
              </span>
            )}
          </a>
          <div className={css.taskList}>
            {board.rest.map((c) => (
              <a
                key={c.key}
                className={c.pending ? css.taskItem : `${css.taskItem} ${css.taskItemMuted}`}
                href={targetHref(c.target)}
                title={c.hint ?? c.action}
              >
                <Dot tone={c.tone} />
                <span className={css.taskTitle}>{c.title}</span>
                <span className={css.taskSub}>{c.sub}</span>
                <span className={css.taskCount}>{formatCount(c.count)}</span>
                <HugeiconsIcon icon={ArrowRight01Icon} size={14} className={css.taskArrow} aria-hidden="true" />
              </a>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}
