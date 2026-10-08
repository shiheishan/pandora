import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Button, Card, ConfirmModal, Empty, Select, Skeleton, Tag, useToast } from '../../../ui'
import { useAppearance } from '../../queries'
import { LoadError, Slot } from '../common/Blocks'
import { copyText, importClients, protocolLabel, rateLabel, type ClientApp } from '../common/clients'
import { canRenew, liveSubscriptions, renewableExpired, usePlanTraffic, useRotateLink, useSubscriptionLinks, useSubscriptionNodes, useSubscriptions, type Subscription, type SubscriptionLink } from '../common/subscriptions'
import { UsageCard } from '../common/UsageCard'
import { fetchStats, isExpiredView, metaLabel } from './labels'
import css from './Subs.module.css'

export default function Subscriptions() {
  const subs = useSubscriptions()
  const { query } = useHashLocation()

  if (subs.isPending) {
    return (
      <div className={css.page}>
        <Card aria-busy="true">
          <Skeleton width={220} height={22} />
          <Skeleton height={40} radius="var(--radius-md)" />
        </Card>
        <div className={css.grid}>
          <Card aria-busy="true">
            <Skeleton height={200} />
          </Card>
          <Card aria-busy="true">
            <Skeleton height={200} />
          </Card>
        </div>
      </div>
    )
  }
  if (subs.isError) {
    return (
      <Card>
        <LoadError error={subs.error} onRetry={() => void subs.refetch()} what="订阅" />
      </Card>
    )
  }

  // 没有生效订阅时回落到能原地续费的已过期订阅：照常列出链接（只读）与续费入口（w5expiry）
  const liveNow = liveSubscriptions(subs.data)
  const live = liveNow.length > 0 ? liveNow : renewableExpired(subs.data)
  if (live.length === 0) {
    return (
      <div className={css.page}>
        <Slot name="portal.subscribe.notice" />
        <Empty
          title="您还没有生效中的订阅"
          description={subs.data.length > 0 ? '之前的订阅已到期或已取消，选购套餐后会生成新的订阅地址。' : '选购套餐后，这里会显示订阅地址、客户端导入方式和可用节点。'}
          action={
            <a className={css.linkButton} href={href('/plans')}>
              选购套餐
            </a>
          }
        />
      </div>
    )
  }

  const selected = live.find((s) => s.id === query.get('sub')) ?? live[0]!
  return <SubscriptionView key={selected.id} sub={selected} live={live} />
}

// ---------------------------------------------------------------------------
// 选中一条订阅后的整页
// ---------------------------------------------------------------------------
function SubscriptionView({ sub, live }: { sub: Subscription; live: Subscription[] }) {
  const links = useSubscriptionLinks()
  const link = links.data?.find((l) => l.subscription_id === sub.id)
  const { summary, resetAt, timeZone } = usePlanTraffic(sub)

  return (
    <div className={css.page}>
      <Slot name="portal.subscribe.notice" />
      <Card className={css.head}>
        <Header sub={sub} live={live} hasLink={link !== undefined} timeZone={timeZone} />
        <LinkBox sub={sub} link={link} loading={links.isPending} error={links.isError ? links.error : null} onRetry={() => void links.refetch()} />
      </Card>
      <div className={css.grid}>
        <ImportCard url={link?.url} />
        <NodesCard subscriptionId={sub.id} />
      </div>
      <UsageCard subscriptionId={sub.id} summary={summary} resetAt={resetAt} />
    </div>
  )
}

// 重置订阅链接会连节点密码一起换（2026-10-07）：旧链接与已导入的节点都失效；门户重置按用户限频
const ROTATE_LIMIT = '两次更换至少间隔 10 分钟，每天最多 5 次。'
const ROTATE_CONFIRM = `重置后旧链接和已导入的节点立即失效，你所有设备都要重新更新一次订阅。${ROTATE_LIMIT}`

function Header({ sub, live, hasLink, timeZone }: { sub: Subscription; live: Subscription[]; hasLink: boolean; timeZone: string | undefined }) {
  const toast = useToast()
  const rotate = useRotateLink()
  const [confirming, setConfirming] = useState(false)

  async function doRotate() {
    try {
      await rotate.mutateAsync(sub.id)
      toast('订阅地址已更换')
    } catch (error) {
      toast(isApiError(error, 'not_found') ? '这条订阅已不可用，请刷新页面' : error instanceof Error ? error.message : '更换失败', 'danger')
    } finally {
      setConfirming(false)
    }
  }

  return (
    <div className={css.headRow}>
      <div className={css.headTitle}>
        {live.length > 1 ? (
          <Select
            aria-label="切换订阅"
            className={css.switcher}
            value={sub.id}
            options={live.map((s) => ({ value: s.id, label: s.plan_name }))}
            onChange={(e) => navigate('/subs', { query: { sub: e.target.value }, replace: true })}
          />
        ) : (
          <h2 className={css.planName}>{sub.plan_name}</h2>
        )}
        {sub.status === 'grace' && <Tag tone="warn">宽限期</Tag>}
        {sub.status === 'past_due' && <Tag tone="danger">待续费</Tag>}
        {isExpiredView(sub) && <Tag tone="danger">已过期</Tag>}
        <span className={css.meta}>{metaLabel(sub, new Date(), timeZone)}</span>
      </div>
      <div className={css.headActions}>
        {hasLink && !isExpiredView(sub) && (
          <button type="button" className={css.quietAction} disabled={rotate.isPending} onClick={() => setConfirming(true)}>
            更换订阅地址
          </button>
        )}
        {canRenew(sub) && <a href={href('/checkout', { renew: sub.id })}>续费</a>}
      </div>
      <ConfirmModal
        open={confirming}
        title="更换订阅地址？"
        body={ROTATE_CONFIRM}
        confirmLabel="更换"
        tone="danger"
        onConfirm={doRotate}
        onCancel={() => setConfirming(false)}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 订阅地址 + 拉取统计（契约待补·前端）
// ---------------------------------------------------------------------------
function LinkBox({ sub, link, loading, error, onRetry }: { sub: Subscription; link: SubscriptionLink | undefined; loading: boolean; error: unknown; onRetry: () => void }) {
  const toast = useToast()
  const rotate = useRotateLink()

  if (loading) return <Skeleton height={40} radius="var(--radius-md)" />
  if (error) return <LoadError error={error} onRetry={onRetry} what="订阅地址" />

  async function copy(url: string) {
    const ok = await copyText(url)
    toast(ok ? '订阅地址已复制' : '复制失败，请长按地址手动复制', ok ? 'ok' : 'danger')
  }

  async function regenerate() {
    try {
      await rotate.mutateAsync(sub.id)
      toast('已生成新的订阅地址')
    } catch (e) {
      toast(e instanceof Error ? e.message : '生成失败', 'danger')
    }
  }

  const stats = link ? fetchStats(link, sub.device_limit) : null
  const expired = isExpiredView(sub) || link?.expired === true
  return (
    <div className={css.linkBox}>
      <div className={css.label}>订阅地址</div>
      <div className={css.linkRow}>
        <code className={css.url} data-empty={link ? undefined : ''}>
          {link ? link.url : '这条订阅还没有可用的订阅地址'}
        </code>
        {link ? (
          <Button variant="primary" className={css.copy} onClick={() => void copy(link.url)}>
            复制
          </Button>
        ) : expired ? null : (
          <Button variant="primary" className={css.copy} busy={rotate.isPending} onClick={() => void regenerate()}>
            重新生成
          </Button>
        )}
      </div>
      {expired ? (
        <div className={css.hint}>订阅已过期，节点暂停服务。续费后此链接自动恢复，无需重新导入；过期期间不能更换订阅地址。</div>
      ) : (
        <div className={css.hint}>订阅地址请勿分享。泄露后点击「更换订阅地址」，旧地址和已导入的节点会立即失效。{ROTATE_LIMIT}</div>
      )}
      {stats && (
        <div className={css.stats} data-leak={stats.leak ? '' : undefined}>
          {stats.text}
          {stats.leak && <span className={css.leak}>可能已泄露，建议更换订阅地址</span>}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 一键导入：唤起客户端深链；v2rayN 没有 scheme，复制地址并提示手动添加
// ---------------------------------------------------------------------------
function ImportCard({ url }: { url: string | undefined }) {
  const toast = useToast()
  const site = useAppearance().data?.theme?.branding.site_name || 'Pandora'
  const clients = url ? importClients(url, site) : []

  async function manual(client: ClientApp) {
    if (!url) return
    const ok = await copyText(url)
    toast(ok ? `已复制订阅地址，在 ${client.name} 里「添加订阅」粘贴即可` : '复制失败，请手动复制订阅地址', ok ? 'ok' : 'danger')
  }

  return (
    <Card flush title="一键导入" extra={<span className={css.cardNote}>唤起客户端并自动添加订阅</span>} className={css.listCard}>
      {url ? (
        <ul className={css.list}>
          {clients.map((c) => (
            <li key={c.name}>
              {c.href ? (
                <a className={css.clientRow} href={c.href} onClick={() => toast(`正在唤起 ${c.name}…`)}>
                  <span className={css.clientName}>{c.name}</span>
                  <span className={css.clientOs}>{c.platforms}</span>
                  <span className={css.clientGo}>导入</span>
                </a>
              ) : (
                <button type="button" className={css.clientRow} onClick={() => void manual(c)}>
                  <span className={css.clientName}>{c.name}</span>
                  <span className={css.clientOs}>{c.platforms}</span>
                  <span className={css.clientGo}>复制</span>
                </button>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <Empty bare title="暂时无法导入" description="先在上方生成订阅地址，再回来一键导入。" />
      )}
      <div className={css.cardFoot}>
        没有您的客户端？复制订阅地址手动添加，或查看<a href={href('/help')}>客户端教程</a>
      </div>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 可用节点：保留规则 3，只有名称 / 协议 / 倍率；订阅不在可用状态时接口回 404
// ---------------------------------------------------------------------------
function NodesCard({ subscriptionId }: { subscriptionId: string }) {
  const nodes = useSubscriptionNodes(subscriptionId)
  const unavailable = nodes.isError && isApiError(nodes.error, 'not_found')

  return (
    <Card flush title="可用节点" extra={nodes.data ? <span className={css.cardNote}>{nodes.data.count} 个 · 客户端内可测速</span> : undefined} className={css.listCard}>
      {nodes.isPending ? (
        <div className={css.listSkeleton}>
          <Skeleton height={16} />
          <Skeleton height={16} />
          <Skeleton width="60%" height={16} />
        </div>
      ) : unavailable ? (
        <Empty bare title="当前没有可用节点" description="订阅待续费或已暂停时节点不会下发，续费后自动恢复。" />
      ) : nodes.isError ? (
        <LoadError error={nodes.error} onRetry={() => void nodes.refetch()} what="节点" />
      ) : nodes.data.nodes.length === 0 ? (
        <Empty bare title="这个套餐暂时没有节点" description="节点上线后会自动出现在订阅里，可以稍后在客户端里更新订阅。" />
      ) : (
        <ul className={css.list}>
          {nodes.data.nodes.map((n, i) => {
            const rate = rateLabel(n.traffic_rate)
            return (
              <li key={`${n.name}-${i}`} className={css.nodeRow}>
                <span className={css.nodeName}>{n.name}</span>
                <span className={css.nodeProto}>{protocolLabel(n.protocol)}</span>
                <span className={css.nodeRate}>{rate && <Tag tone={n.traffic_rate > 1 ? 'warn' : 'ok'}>{rate}</Tag>}</span>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}
