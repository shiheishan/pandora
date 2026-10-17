import { Icon, Avatar, Chip, Num, Btn, Rows, Row } from '../../kit'
import {
  usePortalMe,
  displayName,
  useActivePlanName,
  useUnreadCount,
  useCommissionAvailable,
  usePendingOrderCount,
} from '../../queries'
import { href } from '../../../core/router'
import { useTheme, setTheme } from '../../../core/theme'
import { formatMoney } from '../../../core/format'
import { signOut, useRuntime } from '../../../shell/runtime'
import { Segmented } from '../../../ui'
import { useTickets } from '../tickets/api'
import css from './Me.module.css'

// 手机「我的」页（#/me）：资料卡、三格快捷入口、邀请横幅、帮助与账号安全、外观、退出登录。
// 桌面没有这一页：外框把 #/me 落回我的套餐，桌面端这些入口都在左侧导航里。
export default function Me() {
  const runtime = useRuntime()
  const me = usePortalMe()
  const name = displayName(me.data)
  const email = me.data?.email ?? ''
  const plan = useActivePlanName()
  const pending = usePendingOrderCount()
  const unread = useUnreadCount()
  const available = useCommissionAvailable().data?.available
  const theme = useTheme()
  // 「N 张客服已回复」：与工单列表的「客服已回复」标签同口径（status = pending_user，用户回复或关闭前一直算）；0 张只写标题
  const replied = useTickets().data?.filter((t) => t.status === 'pending_user').length ?? 0

  return (
    <>
      {/* 资料卡：进账号安全 */}
      <a className={css.card} href={href('/account')}>
        <Avatar name={name || email} size="lg" solid />
        <span className={css.who}>
          <b>
            {name}
            {plan && <Chip className={css.chip}>{plan}</Chip>}
          </b>
          <small>{email}</small>
        </span>
        <Icon name="chev" size={18} className={css.chev} />
      </a>

      {/* 三格快捷入口：订单 / 消息 / 工单 */}
      <nav aria-label="快捷入口" className={css.tiles}>
        <a className={css.tile} href={href('/orders')}>
          <Icon name="orders" />
          <b>订单</b>
          {pending != null && pending > 0 && (
            <small className={css.warn}>{pending} 笔待支付</small>
          )}
        </a>
        <a className={css.tile} href={href('/messages')}>
          <Icon name="bell" />
          <b>消息</b>
          {unread > 0 && <small className={css.hot}>{unread} 条未读</small>}
        </a>
        <a className={css.tile} href={href('/tickets')}>
          <Icon name="chat" />
          <b>工单</b>
          {replied > 0 && <small className={css.hot}>{replied} 张客服已回复</small>}
        </a>
      </nav>

      {/* 邀请横幅：进邀请返利 */}
      <a className={css.ref} href={href('/referral')}>
        <span className={css.refText}>
          <b>邀请好友，拿返利</b>
          {available != null && (
            <small>
              可用佣金 <Num className={css.refNum}>{formatMoney(available)}</Num>
            </small>
          )}
        </span>
        <span className={css.refGo}>
          去看看 <Icon name="chev" size={16} />
        </span>
      </a>

      {/* 分组行 */}
      <Rows className={css.rows}>
        <Row href={href('/help')} icon="help" title="帮助中心" />
        <Row href={href('/account')} icon="shield" title="账号安全" sub="密码、登录的设备、Telegram" />
        <Row
          icon="moon"
          title="外观"
          end={
            <Segmented
              size="sm"
              label="外观"
              value={theme}
              onChange={setTheme}
              options={[
                { value: 'light', label: '浅色' },
                { value: 'dark', label: '深色' },
              ]}
            />
          }
        />
      </Rows>

      <Btn tone="soft" full className={css.out} onClick={() => void signOut(runtime)}>
        退出登录
      </Btn>
    </>
  )
}
