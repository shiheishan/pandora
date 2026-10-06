import { isApiError } from '../../../core/api'
import { Button, Empty } from '../../../ui'
import { useAppearance } from '../../queries'
import css from './common.module.css'

/** 契约 GET v1/appearance：slot 内容是服务端净化过的 HTML；与登录页 portal.login.notice 同一做法。 */
export function Slot({ name }: { name: string }) {
  const html = useAppearance().data?.slots[name]
  if (!html) return null
  return <div className={css.slot} dangerouslySetInnerHTML={{ __html: html }} />
}

/** 卡片内的加载失败：bare 空状态 + 重试；网络断开与服务端错误给不同的现状句。 */
export function LoadError({ error, onRetry, what = '内容' }: { error: unknown; onRetry: () => void; what?: string }) {
  const offline = isApiError(error, 'network_error')
  return (
    <Empty
      bare
      title={offline ? '网络连接失败' : `${what}加载失败`}
      description={offline ? '检查网络后重试。' : error instanceof Error && error.message ? error.message : '稍后重试。'}
      action={
        <Button size="sm" onClick={onRetry}>
          重试
        </Button>
      }
    />
  )
}
