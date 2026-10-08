import { isApiError } from '../../../core/api'

/** 换新链接超限（429「操作太频繁，请 9 分钟后再试」）→「刚换过，9 分钟后才能再换」 */
export function rotateWaitText(error: unknown): string | null {
  if (!isApiError(error, 'rate_limited')) return null
  const m = /(\d+)\s*分钟/.exec(error.message)
  return m ? `刚换过，${m[1]} 分钟后才能再换` : '刚换过，稍后才能再换'
}

/** 改名失败落到输入框下：服务端的 fields.label 优先，其余用错误文案 */
export function renameError(error: unknown): string {
  if (isApiError(error)) return error.fields?.label && error.status === 422 ? error.fields.label : error.message
  return error instanceof Error ? error.message : '没改成，请稍后再试'
}
