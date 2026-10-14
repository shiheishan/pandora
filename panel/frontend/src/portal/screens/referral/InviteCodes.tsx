import { formatDateTime } from '../../../core/format'
import { Button, Card, Empty, Skeleton, Tag, useToast } from '../../../ui'
import { LoadError } from '../common/Blocks'
import { copyText } from '../common/clients'
import { useCreateInviteCode, useInvite } from './api'
import { activeCodeCount, canCreateCode, codeStatusLabel, inviteLink } from './model'
import css from './InviteCodes.module.css'

/** 我的邀请码列表：状态、注册人数、访问量，可在上限内再生成 */
export function InviteCodesCard() {
  const toast = useToast()
  const invite = useInvite()
  const create = useCreateInviteCode()

  async function copyLink(code: string) {
    const link = inviteLink(code, window.location.origin)
    if (await copyText(link)) toast('邀请链接已复制')
    else toast('复制失败，请手动选中复制', 'danger')
  }

  function onCreate() {
    create.mutate(undefined, {
      onSuccess: () => toast('已生成新的邀请码'),
      onError: (e) => toast(e instanceof Error ? e.message : '生成失败，请稍后重试', 'danger'),
    })
  }

  const data = invite.data
  const active = data ? activeCodeCount(data.codes) : 0
  const maxCodes = data?.max_codes ?? 0
  const canCreate = data ? canCreateCode(data.codes, data.max_codes) : false

  const extra =
    data === undefined ? (
      <Skeleton width={180} height={32} />
    ) : (
      <div className={css.headExtra}>
        <span className={css.count}>有效 {active}/{maxCodes}</span>
        <Button
          size="sm"
          busy={create.isPending}
          disabled={!canCreate}
          title={canCreate ? undefined : `最多同时持有 ${maxCodes} 个有效邀请码`}
          onClick={onCreate}
        >
          再生成一个
        </Button>
      </div>
    )

  return (
    <Card title="我的邀请码" extra={extra} flush>
      {invite.isError ? (
        <div className={css.bodyPad}>
          <LoadError error={invite.error} what="邀请码" onRetry={() => void invite.refetch()} />
        </div>
      ) : invite.isPending || !data ? (
        <div className={css.bodyPad}>
          <Skeleton height={120} radius="var(--radius-md)" />
        </div>
      ) : data.codes.length === 0 ? (
        <Empty bare title="还没有邀请码" description="稍后再试或联系客服。" />
      ) : (
        <ul className={css.list}>
          {data.codes.map((c) => (
            <li key={c.code} className={css.row}>
              <code className={css.code}>{c.code}</code>
              <Tag tone={c.status === 'active' ? 'ok' : 'neutral'}>{codeStatusLabel(c.status)}</Tag>
              <span className={css.meta}>注册 {c.invited} 人</span>
              <span className={css.meta}>访问 {c.visits} 次</span>
              <span className={css.date}>{formatDateTime(c.created_at).slice(0, 10)}</span>
              {c.status === 'active' && (
                <Button size="sm" variant="outline" onClick={() => void copyLink(c.code)}>
                  复制链接
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}
