import { useRuntime } from './runtime'
import css from './ShellLoader.module.css'

/** 登录状态还没定下来时两边入口共用的占位：恢复中画与外框加载同一个空白占位（接着外框的加载占位不跳），暂时连不上给一句话和重试。 */
export function SessionPending({ state }: { state: 'unknown' | 'unavailable' }) {
  const { session } = useRuntime()

  if (state === 'unknown') {
    return <div className={css.page} role="status" aria-label="正在恢复登录" />
  }

  return (
    <div className={css.page} role="alert">
      <div className={css.message}>暂时连不上服务器，还没确认你的登录状态</div>
      <button type="button" className={css.reload} onClick={() => session.retry()}>
        重试
      </button>
    </div>
  )
}
