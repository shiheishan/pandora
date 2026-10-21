import { Btn, Rows, Row, SectionTitle } from '../../kit'
import { Empty, Skeleton, Tag, useToast } from '../../../ui'
import { LoadError } from '../common/Blocks'
import { copyText } from '../common/clients'
import { shortDate } from '../common/traffic'
import { useCreateInviteCode, useInvite } from './api'
import { activeCodeCount, canCreateCode, codeStatusLabel, inviteLink } from './model'
import css from './InviteCodes.module.css'

/** 我的邀请码列表（样稿 .codes）：状态、注册人数、访问量，可在上限内再生成 */
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

  const aside =
    data === undefined ? (
      <Skeleton width={160} height={20} />
    ) : (
      <span className={css.aside}>
        <span className={css.count}>
          有效 {active}/{maxCodes}
        </span>
        <button type="button" className={css.actL} disabled={!canCreate || create.isPending} onClick={onCreate}>
          再生成一个
        </button>
      </span>
    )

  return (
    <section>
      <SectionTitle aside={aside}>我的邀请码</SectionTitle>
      {invite.isError ? (
        <Rows>
          <LoadError error={invite.error} what="邀请码" onRetry={() => void invite.refetch()} />
        </Rows>
      ) : invite.isPending || !data ? (
        <Skeleton height={120} />
      ) : data.codes.length === 0 ? (
        <Rows>
          <Empty bare title="还没有邀请码" description="稍后再试或联系客服。" />
        </Rows>
      ) : (
        <Rows className={css.codes}>
          {data.codes.map((c) => (
            <Row
              key={c.code}
              lead={<span className={css.cd}>{c.code}</span>}
              title={<Tag tone={c.status === 'active' ? 'ok' : 'neutral'}>{codeStatusLabel(c.status)}</Tag>}
              sub={`注册 ${c.invited} 人 · 访问 ${c.visits} 次`}
              value={<span className={css.date}>{shortDate(c.created_at)} 生成</span>}
              end={c.status === 'active' ? <Btn tone="soft" size="sm" onClick={() => void copyLink(c.code)}>复制链接</Btn> : undefined}
              className={c.status === 'active' ? undefined : css.off}
            />
          ))}
        </Rows>
      )}
      {data?.single_use && <p className={css.cap}>每个邀请码只能邀请 1 人，有人用它注册后就失效，可以再生成新的。</p>}
      <p className={css.cap}>访问次数按 IP 去重，只作参考。</p>
      {data && !canCreate && <p className={css.cap}>最多同时持有 {maxCodes} 个有效邀请码。</p>}
    </section>
  )
}
