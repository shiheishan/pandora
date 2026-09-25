/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 ApiError，依赖 ../../../core/format 的 formatMoney / formatDateTime，依赖 ../../../ui 的 Button / Card / ConfirmModal / Empty / Input / QueryView / Skeleton / StatStrip / useToast，依赖 ../../queries 的 useCommission / useSiteConfig，依赖 ../common 的 LoadError / copyText / useIntentKey / endsIntent，依赖 ./api 与 ./model
 * [OUTPUT]: 默认导出 Referral 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/referral 的入口：邀请返利（门户-06）。顶部邀请横幅（链接 /?invite= 与邀请码两个复制按钮），四格统计，左列「使用佣金」（全部转入余额 + 申请提现），右列「佣金记录」，左列下方补「邀请记录」（契约待补·前端，不展示 risk_flag）；可用佣金以账本为准（5.A D-F-1），两个写操作各一个幂等键
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { ApiError } from '../../../core/api'
import { formatDateTime, formatMoney } from '../../../core/format'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Skeleton, StatStrip, useToast } from '../../../ui'
import { useCommission, useSiteConfig, type Commission } from '../../queries'
import { LoadError } from '../common/Blocks'
import { copyText } from '../common/clients'
import { endsIntent, useIntentKey } from '../common/intent'
import { useInvite, useRequestWithdrawal, useTransferCommission } from './api'
import { commissionRecords, headline, inviteLink, inviteUsage, parseWithdrawAmount, withdrawBlock } from './model'
import css from './Referral.module.css'

export default function Referral() {
  const commission = useCommission()
  const summary = commission.data?.summary
  return (
    <div className={css.page}>
      <InviteBanner ratePercent={summary?.rate_percent} />
      {commission.isError ? (
        <Card>
          <LoadError error={commission.error} what="佣金概况" onRetry={() => void commission.refetch()} />
        </Card>
      ) : (
        <StatStrip
          label="邀请与佣金统计"
          items={
            summary && [
              { label: '邀请注册', value: summary.invitees },
              { label: '付费好友', value: summary.paid_invitees ?? '—' },
              { label: '累计佣金', value: summary.total_earned === undefined ? '—' : formatMoney(summary.total_earned, summary.currency) },
              { label: '可用佣金', value: formatMoney(summary.available, summary.currency) },
            ]
          }
        />
      )}
      <div className={css.grid}>
        <div className={css.useArea}>{summary ? <UseCard summary={summary} /> : !commission.isError && <Skeleton height={260} radius="var(--radius-lg)" />}</div>
        <div className={css.recordsArea}>
          <RecordsCard />
        </div>
        <div className={css.inviteesArea}>
          <InviteesCard />
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 邀请横幅：链接与邀请码各一个复制按钮；站点关闭注册或邀请码用满时说明链接暂时无效
// ---------------------------------------------------------------------------
function InviteBanner({ ratePercent }: { ratePercent: number | undefined }) {
  const toast = useToast()
  const invite = useInvite()
  const site = useSiteConfig()
  const code = invite.data?.invite.code
  const link = code ? inviteLink(code, window.location.origin) : ''
  const usage = invite.data ? inviteUsage(invite.data.invite) : null
  const closed = site.data?.registration_mode === 'closed'

  async function copy(text: string, what: string) {
    if (await copyText(text)) toast(`${what}已复制`)
    else toast('复制失败，请手动选中复制', 'danger')
  }

  return (
    <section className={css.banner} aria-label="我的邀请链接">
      <h2 className={css.headline}>{ratePercent === undefined ? <Skeleton width={220} height={22} /> : headline(ratePercent)}</h2>
      {invite.isError ? (
        <LoadError error={invite.error} what="邀请链接" onRetry={() => void invite.refetch()} />
      ) : (
        <div className={css.linkRow}>
          <code className={css.link}>{link || <Skeleton width={260} height={16} />}</code>
          <Button variant="primary" disabled={!code} onClick={() => void copy(link, '邀请链接')}>
            复制邀请链接
          </Button>
          <Button disabled={!code} onClick={() => code && void copy(code, '邀请码')}>
            邀请码 <span className={css.code}>{code ?? '········'}</span>
          </Button>
        </div>
      )}
      {(closed || usage) && (
        <p className={closed || usage?.exhausted ? css.bannerWarn : css.bannerNote}>{closed ? '站点暂停注册，邀请链接暂时无法使用。' : usage?.text}</p>
      )}
    </section>
  )
}

// ---------------------------------------------------------------------------
// 使用佣金：两个动作用同一个「可用佣金」（账本余额 − 在途提现），失败文案照后端
// 幂等键：断网与 5xx 保留键（重试要拿回同一结果）；成功或 4xx 业务拒绝即动作结束、丢弃键（common/intent）
// ---------------------------------------------------------------------------
function UseCard({ summary }: { summary: Commission['summary'] }) {
  const toast = useToast()
  const transfer = useTransferCommission()
  const withdraw = useRequestWithdrawal()
  const transferKey = useIntentKey()
  const withdrawKey = useIntentKey()
  const [confirming, setConfirming] = useState(false)
  const [transferError, setTransferError] = useState<string | null>(null)
  const [amount, setAmount] = useState('')
  const [payout, setPayout] = useState('')
  const [errors, setErrors] = useState<{ amount?: string; payout?: string; form?: string }>({})

  const money = (v: number) => formatMoney(v, summary.currency)
  const block = withdrawBlock(summary)

  async function doTransfer() {
    const request = { amount: summary.available }
    try {
      await transfer.mutateAsync({ amount: summary.available, key: transferKey.keyFor(request) })
      transferKey.reset()
      setConfirming(false)
      setTransferError(null)
      toast(`已将 ${money(request.amount)} 转入余额`)
    } catch (e) {
      if (endsIntent(e)) transferKey.reset()
      setConfirming(false)
      setTransferError(e instanceof Error ? e.message : '转入失败，请稍后重试')
    }
  }

  function submitWithdraw() {
    const parsed = parseWithdrawAmount(amount, { min: summary.min_withdraw, available: summary.available, currency: summary.currency })
    const detail = payout.trim()
    const next = { amount: 'error' in parsed ? parsed.error : undefined, payout: detail ? undefined : '请填写收款账号' }
    setErrors(next)
    if ('error' in parsed || !detail) return
    const body = { amount: parsed.cents, payout_detail: detail }
    withdraw.mutate(
      { body, key: withdrawKey.keyFor(body) },
      {
        onSuccess: () => {
          withdrawKey.reset()
          toast('提现申请已提交，等待审核')
          setAmount('')
          setPayout('')
        },
        onError: (e) => {
          if (endsIntent(e)) withdrawKey.reset()
          const fieldMsg = e instanceof ApiError ? e.fields.payout_detail : undefined
          setErrors(fieldMsg ? { payout: fieldMsg } : { form: e.message || '提交失败，请稍后重试' })
        },
      },
    )
  }

  return (
    <Card title="使用佣金" className={css.use}>
      <button type="button" className={css.transfer} disabled={summary.available <= 0} onClick={() => setConfirming(true)}>
        <span>全部转入余额</span>
        <span className={css.transferNote}>{summary.available > 0 ? `${money(summary.available)} · 即时到账` : '暂无可用佣金'}</span>
      </button>
      {summary.pending > 0 && <p className={css.hint}>另有 {money(summary.pending)} 在冻结期内，解冻后可用。</p>}
      {transferError && (
        <p className={css.error} role="alert">
          {transferError}
        </p>
      )}
      <hr className={css.divider} />
      <p className={css.caption}>申请提现 · 最低 {money(summary.min_withdraw)}，审核通过后打款</p>
      {block ? (
        <p className={css.blocked}>{block}</p>
      ) : (
        <>
          <div className={css.withdrawRow}>
            <Input
              mono
              inputMode="decimal"
              aria-label="提现金额（元）"
              placeholder="金额"
              value={amount}
              error={errors.amount}
              onChange={(e) => setAmount(e.target.value.replace(/[^\d.]/g, ''))}
            />
            <Input aria-label="收款账号" placeholder="支付宝账号 / 银行卡号" maxLength={200} autoComplete="off" value={payout} error={errors.payout} onChange={(e) => setPayout(e.target.value)} />
          </div>
          {errors.form && (
            <p className={css.error} role="alert">
              {errors.form}
            </p>
          )}
          <Button variant="outline" className={css.withdraw} busy={withdraw.isPending} onClick={submitWithdraw}>
            申请提现
          </Button>
        </>
      )}
      <ConfirmModal
        open={confirming}
        title={`将 ${money(summary.available)} 转入余额？`}
        body="转入后不可再提现。余额可用于购买套餐、续费与流量包。"
        confirmLabel="转入余额"
        onConfirm={doTransfer}
        onCancel={() => setConfirming(false)}
      />
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 佣金记录：三类记录合并倒序，先显示 8 条
// ---------------------------------------------------------------------------
const RECORDS_PREVIEW = 8

function RecordsCard() {
  const commission = useCommission(commissionRecords)
  const [all, setAll] = useState(false)
  return (
    <Card flush title="佣金记录" className={css.listCard}>
      <QueryView query={commission} rows={4} empty={<Empty bare title="还没有佣金记录" description="好友通过你的链接注册并付费后，佣金会记在这里。" />}>
        {(rows) => (
          <>
            <ul className={css.list}>
              {(all ? rows : rows.slice(0, RECORDS_PREVIEW)).map((r) => (
                <li key={r.key} className={css.record}>
                  <span className={css.recordMain}>
                    <span className={css.recordTitle}>
                      {r.title}
                      {r.ref && (
                        <>
                          {' · '}
                          <span className={css.ref}>{r.ref}</span>
                        </>
                      )}
                    </span>
                    <span className={css.recordMeta}>{r.meta}</span>
                  </span>
                  <span className={css[r.tone]}>
                    {r.amount > 0 ? '+' : '−'}
                    {formatMoney(Math.abs(r.amount), r.currency)}
                  </span>
                  <span className={css.recordStatus}>{r.status}</span>
                </li>
              ))}
            </ul>
            {!all && rows.length > RECORDS_PREVIEW && (
              <button type="button" className={css.more} onClick={() => setAll(true)}>
                显示全部 {rows.length} 条
              </button>
            )}
          </>
        )}
      </QueryView>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 邀请记录（契约待补·前端）：打码邮箱与绑定时间；risk_flag 是风控内部判定，不给邀请人看
// ---------------------------------------------------------------------------
function InviteesCard() {
  const invite = useInvite()
  return (
    <Card flush title="邀请记录" className={css.listCard}>
      <QueryView query={invite} rows={3} isEmpty={(d) => d.invitees.length === 0} empty={<Empty bare title="还没有好友通过你的链接注册" description="把邀请链接发给朋友，对方打开后注册会自动填上你的邀请码。" />}>
        {(d) => (
          <ul className={css.list}>
            {d.invitees.map((p, i) => (
              <li key={`${p.bound_at}-${i}`} className={css.invitee}>
                <span className={css.inviteeEmail}>{p.email}</span>
                <span className={css.recordMeta}>{formatDateTime(p.bound_at)} 注册</span>
              </li>
            ))}
          </ul>
        )}
      </QueryView>
    </Card>
  )
}
