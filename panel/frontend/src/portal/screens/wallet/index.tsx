import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { Empty, QueryView, Skeleton } from '../../../ui'
import { Btn, BigNumber, GroupHeader, Hero, Meta, Row, Rows, SectionTitle } from '../../kit'
import { useBalance } from '../../queries'
import type { PortalScreenProps } from '../index'
import { GiftHistory, RedeemResult, RedeemSheet, type RedeemDone } from './Redeem'
import { ledgerByMonth, ledgerView } from './model'
import { TopupSheet } from './Topup'
import css from './Wallet.module.css'

const HISTORY_PREVIEW = 8

export default function Wallet({ rest }: PortalScreenProps) {
  const [topupOpen, setTopupOpen] = useState(false)
  const [done, setDone] = useState<RedeemDone | null>(null)

  if (rest[0] === 'redeem' && rest[1] === 'done' && done) return <RedeemResult info={done} />

  const redeemOpen = rest[0] === 'redeem'

  return (
    <div className={css.page}>
      <BalanceHero onTopup={() => setTopupOpen(true)} onRedeem={() => navigate('/wallet/redeem', { replace: true })} />
      <History />
      <GiftHistory />
      <TopupSheet open={topupOpen} onClose={() => setTopupOpen(false)} />
      <RedeemSheet
        open={redeemOpen}
        onClose={() => navigate('/wallet', { replace: true })}
        onDone={(r) => {
          setDone(r)
          navigate('/wallet/redeem/done', { replace: true })
        }}
      />
    </div>
  )
}

function BalanceHero({ onTopup, onRedeem }: { onTopup: () => void; onRedeem: () => void }) {
  const balance = useBalance()
  return (
    <Hero as="section" aria-label="账户余额">
      <div className={css.lbl}>账户余额</div>
      {balance.data ? (
        <BigNumber size="lg" prefix="¥" value={formatMoney(balance.data.balance).replace(/^¥/, '')} className={css.amt} />
      ) : balance.isError ? (
        <div className={css.amtDash}>—</div>
      ) : (
        <Skeleton width={160} height={56} />
      )}
      <Meta size="sm">买套餐、续费都能用，结账时自动先用。不能提现。</Meta>
      <div className={css.acts}>
        <Btn tone="hot" grow onClick={onTopup}>充值</Btn>
        <Btn tone="soft" grow onClick={onRedeem}>兑换卡</Btn>
      </div>
    </Hero>
  )
}

function History() {
  const balance = useBalance()
  const [all, setAll] = useState(false)
  return (
    <>
      <SectionTitle className={css.histTitle}>余额记录</SectionTitle>
      <QueryView
        query={balance}
        rows={3}
        isEmpty={(d) => d.history.length === 0}
        empty={<Empty bare title="还没有余额变动" description="充值、下单用余额、兑换卡、换套餐退回与佣金转入都会记在这里。" />}
      >
        {(d) => {
          const shown = all ? d.history : d.history.slice(0, HISTORY_PREVIEW)
          const groups = ledgerByMonth(shown)
          return (
            <>
              {groups.map((g) => (
                <div key={g.key}>
                  <GroupHeader>{g.label}</GroupHeader>
                  <Rows>
                    {g.rows.map((h) => {
                      const i = shown.indexOf(h)
                      const v = ledgerView(h)
                      return (
                        <Row
                          key={`${h.at}-${i}`}
                          icon={v.icon}
                          iconStyle={v.out ? 'roundMuted' : 'round'}
                          title={v.title}
                          sub={v.sub}
                          value={v.amount}
                          valueTone={v.out ? 'num' : 'plus'}
                        />
                      )
                    })}
                  </Rows>
                </div>
              ))}
              {!all && d.history.length > HISTORY_PREVIEW && (
                <button type="button" className={css.more} onClick={() => setAll(true)}>
                  显示全部 {d.history.length} 条
                </button>
              )}
            </>
          )
        }}
      </QueryView>
    </>
  )
}
