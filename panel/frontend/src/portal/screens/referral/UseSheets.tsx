import { useId, useState } from 'react'
import { ApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { isUSDTMethod, payoutAccountHint, withdrawMethodLabel } from '../../../core/withdraw-methods'
import { Input, useToast } from '../../../ui'
import { Btn, Num, Sheet } from '../../kit'
import type { Commission } from '../../queries'
import { useRequestWithdrawal, useTransferCommission } from './api'
import { endsIntent, useIntentKey } from '../common/intent'
import { parseWithdrawAmount, payoutError, withdrawBlock, yuanText } from './model'
import css from './Referral.module.css'

// ---------------------------------------------------------------------------
// 使用佣金的两个弹层（样稿 #sh-transfer / #sh-withdraw）：转入余额与申请提现用同一个可用额
// 幂等键：断网与 5xx 保留键（重试要拿回同一结果）；成功或 4xx 业务拒绝即动作结束、丢弃键
// ---------------------------------------------------------------------------

/** 转入余额：确认一次，写明转了就不能提现；失败留在弹层里报错 */
export function TransferSheet({ open, onClose, summary }: { open: boolean; onClose: () => void; summary: Commission['summary'] }) {
  const toast = useToast()
  const transfer = useTransferCommission()
  const transferKey = useIntentKey()
  const [error, setError] = useState<string | null>(null)

  // 弹层关闭时清掉上一次的错误
  function close() {
    setError(null)
    onClose()
  }

  async function doTransfer() {
    const request = { amount: summary.available }
    try {
      await transfer.mutateAsync({ amount: summary.available, key: transferKey.keyFor(request) })
      transferKey.reset()
      toast(`已将 ${formatMoney(request.amount)} 转入余额`)
      close()
    } catch (e) {
      if (endsIntent(e)) transferKey.reset()
      setError(e instanceof Error ? e.message : '转入失败，请稍后重试')
    }
  }

  return (
    <Sheet
      open={open}
      onClose={close}
      title="转入余额"
      dismissible={!transfer.isPending}
      actions={
        <>
          <Btn tone="soft" onClick={close}>
            再想想
          </Btn>
          <Btn tone="hot" busy={transfer.isPending} onClick={() => void doTransfer()}>
            转入余额
          </Btn>
        </>
      }
    >
      <div className={css.amtShow}>
        <span className={css.cur}>¥</span>
        <Num>{yuanText(summary.available)}</Num>
      </div>
      <p className={css.shP}>全部可用佣金即时到账。转入后不能再提现，余额可以买套餐和续费。</p>
      {error && (
        <p className={css.error} role="alert">
          {error}
        </p>
      )}
    </Sheet>
  )
}

/** 申请提现：金额（带「全部」）+ 方式 + 收款账号；按钮写明金额 */
export function WithdrawSheet({ open, onClose, summary }: { open: boolean; onClose: () => void; summary: Commission['summary'] }) {
  const toast = useToast()
  const withdraw = useRequestWithdrawal()
  const withdrawKey = useIntentKey()
  const amountId = useId()
  const [method, setMethod] = useState(summary.withdraw_methods[0] ?? '')
  const [amount, setAmount] = useState('')
  const [payout, setPayout] = useState('')
  const [errors, setErrors] = useState<{ amount?: string; method?: string; payout?: string; form?: string }>({})

  // 后台改了开放方式后重新拉取，原先选的方式可能已经关了：退回第一种开放的方式
  const activeMethod = summary.withdraw_methods.includes(method) ? method : (summary.withdraw_methods[0] ?? '')

  // 按钮上写明金额；没填或填错时不算
  const parsed = parseWithdrawAmount(amount, { min: summary.min_withdraw, available: summary.available })
  const cents = 'cents' in parsed ? parsed.cents : null

  function submitWithdraw() {
    const checked = parseWithdrawAmount(amount, { min: summary.min_withdraw, available: summary.available })
    const detail = payout.trim()
    const payoutErr = payoutError(activeMethod, payout)
    const next = {
      amount: 'error' in checked ? checked.error : undefined,
      payout: payoutErr,
    }
    setErrors(next)
    if ('error' in checked || payoutErr) return
    const body = { amount: checked.cents, method: activeMethod, payout_detail: detail }
    withdraw.mutate(
      { body, key: withdrawKey.keyFor(body) },
      {
        onSuccess: () => {
          withdrawKey.reset()
          toast('提现申请已提交，等待审核')
          setAmount('')
          setPayout('')
          onClose()
        },
        onError: (e) => {
          if (endsIntent(e)) withdrawKey.reset()
          const apiErr = e instanceof ApiError ? e : undefined
          const methodErr = apiErr?.fields?.method
          const payoutFieldMsg = apiErr?.fields?.payout_detail
          if (methodErr || payoutFieldMsg) {
            setErrors({ method: methodErr, payout: payoutFieldMsg })
          } else {
            setErrors({ form: e.message || '提交失败，请稍后重试' })
          }
        },
      },
    )
  }

  // 弹层开着时数据刷新成被锁的状态（有一笔在途、不足最低额、没有开放方式）：整块换成一句说明
  const block = withdrawBlock(summary)
  if (block) {
    return (
      <Sheet open={open} onClose={onClose} title="申请提现" dismissible={!withdraw.isPending}>
        <p className={css.shP}>{block}</p>
        <Btn tone="soft" full className={css.submit} onClick={onClose}>
          知道了
        </Btn>
      </Sheet>
    )
  }

  return (
    <Sheet open={open} onClose={onClose} title="申请提现" dismissible={!withdraw.isPending}>
      <div className={css.fldHead}>
        <label htmlFor={amountId}>金额（元）</label>
        <button type="button" className={css.all} onClick={() => setAmount(yuanText(summary.available).replaceAll(',', ''))}>
          全部 {formatMoney(summary.available)}
        </button>
      </div>
      <Input
        id={amountId}
        mono
        inputMode="decimal"
        aria-label="提现金额"
        value={amount}
        error={errors.amount}
        onChange={(e) => setAmount(e.target.value.replace(/[^\d.]/g, ''))}
      />
      <p className={css.faint}>最低 {formatMoney(summary.min_withdraw)}，审核通过后打款。同一时间只能有一笔在处理。</p>
      <div className={css.shLbl}>收到哪里</div>
      {summary.withdraw_methods.length > 1 ? (
        <div className={css.methods} role="radiogroup" aria-label="收到哪里">
          {summary.withdraw_methods.map((m) => (
            <button key={m} type="button" role="radio" aria-checked={m === activeMethod} className={css.am} onClick={() => setMethod(m)}>
              {withdrawMethodLabel(m)}
            </button>
          ))}
        </div>
      ) : (
        <p className={css.oneMethod}>{withdrawMethodLabel(activeMethod)}</p>
      )}
      {errors.method && (
        <p className={css.error} role="alert">
          {errors.method}
        </p>
      )}
      <Input
        label="收款账号"
        mono={isUSDTMethod(activeMethod)}
        placeholder={payoutAccountHint(activeMethod)}
        maxLength={200}
        autoComplete="off"
        value={payout}
        error={errors.payout}
        onChange={(e) => setPayout(e.target.value)}
      />
      {isUSDTMethod(activeMethod) && <p className={css.faint}>USDT 按地址所在链打款，请确认链与地址一致，填错无法追回</p>}
      {errors.form && (
        <p className={css.error} role="alert">
          {errors.form}
        </p>
      )}
      <Btn tone="hot" full busy={withdraw.isPending} className={css.submit} onClick={submitWithdraw}>
        申请提现{cents !== null && <> <Num>{formatMoney(cents)}</Num></>}
      </Btn>
    </Sheet>
  )
}
