/**
 * [INPUT]: 依赖 ../../../ui 的 Skeleton，依赖 ../../modules 的 Permissions，依赖 ./api 的 useTasks / useBacklog，依赖 ./model 的 taskCards / formatCount，依赖 ./parts，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 Tasks
 * [POS]: 仪表盘第一块「需要处理」：GET v1/dashboard/tasks（待补·后端，与侧栏徽标同一个接口）的各条目 + 冻结契约的通知积压明细，卡片点一下直达处理页（目标页不可读时卡片不可点）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Skeleton } from '../../../ui'
import type { Permissions } from '../../modules'
import { useBacklog, useTasks } from './api'
import css from './Dash.module.css'
import { formatCount, taskCards, type TaskCard } from './model'
import { CardError, Dot, isForbidden, targetHref } from './parts'

export function Tasks({ perms, canTasks, canBacklog }: { perms: Permissions; canTasks: boolean; canBacklog: boolean }) {
  const tasks = useTasks(canTasks)
  const backlog = useBacklog(canBacklog)

  const tasksHidden = !canTasks || (tasks.isError && isForbidden(tasks.error))
  const backlogHidden = !canBacklog || (backlog.isError && isForbidden(backlog.error))
  if (tasksHidden && backlogHidden) return null

  const loading = (!tasksHidden && tasks.isPending) || (!backlogHidden && backlog.isPending)
  const cards = taskCards(tasks.data?.items, backlog.data, perms)
  const pending = cards.filter((c) => c.pending).length
  // 只读账号可能一张卡都点不进去，这时不说「点一下直接去处理」
  const summary = pending === 0 ? '眼下没有要处理的事' : cards.some((c) => c.pending && c.target) ? `${pending} 项 · 点一下直接去处理` : `${pending} 项`

  return (
    <section className={css.section} aria-labelledby="dash-tasks">
      <div className={css.sectionHead}>
        <h2 id="dash-tasks" className={css.sectionTitle}>
          需要处理
        </h2>
        {!loading && cards.length > 0 && <span className={css.hint}>{summary}</span>}
      </div>
      {!tasksHidden && tasks.isError && <CardError boxed what="待办计数" error={tasks.error} onRetry={() => void tasks.refetch()} />}
      {!backlogHidden && backlog.isError && <CardError boxed what="通知积压" error={backlog.error} onRetry={() => void backlog.refetch()} />}
      {(loading || cards.length > 0) && (
        <div className={css.taskGrid}>
          {loading
            ? Array.from({ length: 5 }, (_, i) => <Skeleton key={i} height={126} radius="var(--radius-lg)" />)
            : cards.map((card) => <TaskTile key={card.key} card={card} perms={perms} />)}
        </div>
      )}
    </section>
  )
}

function TaskTile({ card, perms }: { card: TaskCard; perms: Permissions }) {
  const body = (
    <>
      <span className={css.taskHead}>
        <Dot tone={card.tone} />
        <span>{card.title}</span>
      </span>
      <span className={css.taskCount}>{formatCount(card.count)}</span>
      <span className={css.taskSub}>{card.sub}</span>
      {card.target && <span className={css.taskAct}>{card.action} →</span>}
    </>
  )
  return card.target ? (
    <a className={css.task} href={targetHref(card.target, perms)} title={card.hint}>
      {body}
    </a>
  ) : (
    <div className={css.task} title={card.hint}>
      {body}
    </div>
  )
}
