import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { Empty, QueryView, Segmented, Skeleton, useToast } from '../../../ui'
import { BigNumber, Btn, Eyebrow, LinkChip, Meta, Num, Row, Rows, cx } from '../../kit'
import { useAppearance, useCommission, useSiteConfig, type Commission } from '../../queries'
import { LoadError } from '../common/Blocks'
import { copyText } from '../common/clients'
import { shortDate } from '../common/traffic'
import { InviteCodesCard } from './InviteCodes'
import { useInvite } from './api'
import {
  commissionRecords,
  headlineParts,
  inviteLink,
  inviteUsage,
  shortHash,
  withdrawBlock,
  withdrawClosedHere,
  withdrawClosedHint,
  yuanText,
} from './model'
import { TransferSheet, WithdrawSheet } from './UseSheets'
import css from './Referral.module.css'

export default function Referral() {
  const commission = useCommission()
  const summary = commission.data?.summary
  const [view, setView] = useState<'rec' | 'who'>('rec')

  return (
    <div className={css.page}>
      <InviteCard summary={summary} />
      <div className={css.commArea}>
        <CommissionCard />
      </div>
      <div className={css.codeArea}>
        <InviteCodesCard />
      </div>
      <div className={css.tools}>
        <Segmented
          label="记录"
          value={view}
          onChange={setView}
          className={css.seg}
          options={[
            { value: 'rec', label: '佣金记录' },
            {
              value: 'who',
              label: (
                <>
                  邀请记录{summary && <em className={css.cnt}>{summary.invitees}</em>}
                </>
              ),
            },
          ]}
        />
      </div>
      {view === 'rec' ? <RecordsList /> : <InviteesList />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 邀请卡（样稿 .inv）：大号比例、三步、链接与邀请码复制、用量说明、三格统计
// ---------------------------------------------------------------------------
function InviteCard({ summary }: { summary: Commission['summary'] | undefined }) {
  const toast = useToast()
  const invite = useInvite()
  const site = useSiteConfig()
  const appearance = useAppearance()
  const siteName = appearance.data?.site_name?.trim() || 'Pandora'
  const code = invite.data?.invite.code
  const link = code ? inviteLink(code, window.location.origin) : ''
  const usage = invite.data ? inviteUsage(invite.data) : null
  const closed = site.data?.registration_mode === 'closed'
  const parts = summary ? headlineParts(summary.direct_rate_bp, summary.scope) : null

  async function copy(text: string, what: string) {
    if (await copyText(text)) toast(`${what}已复制`)
    else toast('复制失败，请手动选中复制', 'danger')
  }

  return (
    <section className={css.inv} aria-label="我的邀请链接">
      <Eyebrow>{siteName} · 邀请</Eyebrow>
      <h2 className={css.invH}>
        {parts === null ? (
          <Skeleton width={220} height={22} />
        ) : (
          <>
            {parts.rate !== null && (
              <Num className={css.invNum}>
                {parts.rate}
                <small>%</small>
              </Num>
            )}
            {parts.text}
          </>
        )}
      </h2>
      <ol className={css.how}>
        <li>
          <b>1</b>把链接发给朋友
        </li>
        <li>
          <b>2</b>朋友注册时自动填上你的邀请码
        </li>
        <li>
          <b>3</b>朋友付款，佣金记到你这里
        </li>
      </ol>
      {invite.isError ? (
        <LoadError error={invite.error} what="邀请链接" onRetry={() => void invite.refetch()} />
      ) : (
        <div className={css.linkRow}>
          <LinkChip code={link || '········'} className={css.linkChip} />
          <Btn tone="hot" disabled={!code} onClick={() => void copy(link, '邀请链接')}>
            复制邀请链接
          </Btn>
          <Btn tone="soft" disabled={!code} onClick={() => code && void copy(code, '邀请码')}>
            邀请码 <code className={css.code}>{code ?? '········'}</code>
          </Btn>
        </div>
      )}
      {(closed || usage) && (
        <p className={cx(css.use, (closed || usage?.exhausted) && css.useWarn)}>
          {closed ? '站点暂停注册，邀请链接暂时无法使用。' : usage?.text}
        </p>
      )}
      <dl className={css.stats}>
        <div>
          <dt>邀请注册</dt>
          <dd>
            <Num>{summary ? summary.invitees : '—'}</Num>
          </dd>
        </div>
        <div>
          <dt>付费好友</dt>
          <dd>
            <Num>{summary ? summary.paid_invitees : '—'}</Num>
          </dd>
        </div>
        <div>
          <dt>累计佣金</dt>
          <dd>
            <Num>{summary ? formatMoney(summary.total_earned) : '—'}</Num>
          </dd>
        </div>
      </dl>
    </section>
  )
}

// ---------------------------------------------------------------------------
// 佣金卡（样稿 .comm）：可用佣金大号数字，右列「转入余额」「申请提现」两个弹层
// ---------------------------------------------------------------------------
function CommissionCard() {
  // 与页面顶部同一个查询键，react-query 只发一次请求
  const commission = useCommission()
  const [sheet, setSheet] = useState<'transfer' | 'withdraw' | null>(null)

  if (commission.isError) {
    return <LoadError error={commission.error} what="佣金概况" onRetry={() => void commission.refetch()} />
  }
  if (commission.isPending || !commission.data) {
    return <Skeleton height={150} radius="var(--radius-lg)" />
  }

  const summary = commission.data.summary
  const closed = withdrawClosedHere(summary)
  // 说明一行：关提现写提示；否则写提现被锁的原因；都没了且没有可用佣金，写「暂无可用佣金」
  const note = closed
    ? withdrawClosedHint(summary)
    : (withdrawBlock(summary) ?? (summary.available <= 0 ? '暂无可用佣金' : null))

  return (
    <>
      <section className={css.comm} aria-label="可用佣金">
        <div className={css.lbl}>可用佣金</div>
        <BigNumber size="md" prefix="¥" value={yuanText(summary.available)} className={css.amt} />
        {summary.pending > 0 && (
          <Meta size="sm" className={css.meta}>
            另有{' '}
            <b>
              <Num>{formatMoney(summary.pending)}</Num>
            </b>{' '}
            在冻结期内，解冻后可用。
          </Meta>
        )}
        {note && <p className={css.note}>{note}</p>}
        <div className={css.acts}>
          <Btn tone="hot" disabled={summary.available <= 0} onClick={() => setSheet('transfer')}>
            转入余额
          </Btn>
          {!closed && (
            <Btn tone="soft" disabled={withdrawBlock(summary) !== null} onClick={() => setSheet('withdraw')}>
              申请提现
            </Btn>
          )}
        </div>
      </section>
      {/* 弹层放在卡片外，不占卡片的网格 */}
      <TransferSheet open={sheet === 'transfer'} onClose={() => setSheet(null)} summary={summary} />
      <WithdrawSheet open={sheet === 'withdraw'} onClose={() => setSheet(null)} summary={summary} />
    </>
  )
}

// ---------------------------------------------------------------------------
// 佣金记录：三类记录合并倒序，先显示 8 条
// ---------------------------------------------------------------------------
const RECORDS_PREVIEW = 8

function RecordsList() {
  const commission = useCommission(commissionRecords)
  const [all, setAll] = useState(false)
  const toast = useToast()

  async function copyHash(hash: string) {
    if (await copyText(hash)) toast('交易哈希已复制')
    else toast('复制失败，请手动选中复制', 'danger')
  }

  return (
    <QueryView query={commission} rows={4} empty={
        <Rows>
          <Empty bare title="还没有佣金记录" description="好友通过你的链接注册并付费后，佣金会记在这里。" />
        </Rows>
      }>
      {(rows) => {
        const shown = all ? rows : rows.slice(0, RECORDS_PREVIEW)
        return (
          <>
            <Rows className={css.rec}>
              {shown.map((r) => {
                const txHash = r.txHash
                const sub = txHash ? (
                  <>
                    {r.meta}
                    <span className={css.tx}>
                      交易哈希 <code className={css.txCode} title={txHash}>{shortHash(txHash)}</code>
                      <button type="button" className={css.txAct} onClick={() => void copyHash(txHash)}>
                        复制
                      </button>
                      {r.txUrl && (
                        <a className={css.txAct} href={r.txUrl} target="_blank" rel="noopener noreferrer">
                          在区块浏览器查看
                        </a>
                      )}
                    </span>
                  </>
                ) : (
                  r.meta
                )
                return (
                  <Row
                    key={r.key}
                    icon={r.icon}
                    iconStyle={r.icon === 'gift' && r.tone !== 'void' ? 'round' : 'roundMuted'}
                    title={
                      <>
                        <b>{r.title}</b>
                        {r.ref && <span className={css.ref}>{r.ref}</span>}
                      </>
                    }
                    sub={sub}
                    end={
                      <span className={css.oa}>
                        <Num className={r.tone === 'plus' ? css.plus : r.tone === 'void' ? css.void : undefined}>
                          {r.amount > 0 ? '+' : '−'}
                          {formatMoney(Math.abs(r.amount))}
                        </Num>
                        <small>{r.status}</small>
                      </span>
                    }
                    className={r.tone === 'void' ? css.voidRow : undefined}
                  />
                )
              })}
            </Rows>
            {!all && rows.length > RECORDS_PREVIEW && (
              <button type="button" className={css.more} onClick={() => setAll(true)}>
                显示全部 {rows.length} 条
              </button>
            )}
          </>
        )
      }}
    </QueryView>
  )
}

// ---------------------------------------------------------------------------
// 邀请记录：打码邮箱与绑定时间；risk_flag 是风控内部判定，不给邀请人看
// ---------------------------------------------------------------------------
function InviteesList() {
  const invite = useInvite()
  return (
    <QueryView query={invite} rows={3} isEmpty={(d) => d.invitees.length === 0} empty={
        <Rows>
          <Empty bare title="还没有好友通过你的链接注册" description="把邀请链接发给朋友，对方打开后注册会自动填上你的邀请码。" />
        </Rows>
      }>
      {(d) => (
        <>
          <Rows className={css.rec}>
            {d.invitees.map((p, i) => (
              <Row key={`${p.bound_at}-${i}`} icon="user" title={<span className={css.whoE}>{p.email}</span>} value={`${shortDate(p.bound_at)} 注册`} />
            ))}
          </Rows>
          <p className={css.cap}>只显示打码后的邮箱。</p>
        </>
      )}
    </QueryView>
  )
}
