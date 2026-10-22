import { useEffect, useState } from 'react'
import { formatCount, relativeTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { RadioCards, SectionTitle } from '../../kit'
import { Button, Card, Empty, Input, QueryView, Select, Table, Tag, useToast, type TableColumn } from '../../../ui'
import { useFailure } from '../../actions'
import { DEVICE_WINDOWS, okSchema, useDevices, useFindUserByEmail, useInvalidateUsers, type DeviceMode, type DeviceWindow, type OnlineDevice } from './api'
import { devicePolicyBody, nearLimit, pips } from './model'
import signupCss from './Signup.module.css'

// 设计稿的「拒绝新设备 / 踢下最早的设备」按后端两种判定改写（契约后台-03 GET v1/devices）
const MODES: ReadonlyArray<[DeviceMode, string, string]> = [
  ['loose', '拒绝新设备', '节点本地判定，超限的新连接被拒；已在线的设备不受影响'],
  ['strict', '超限停止下发', '面板跨节点汇总，超出上限 + 宽容值后停止向该订阅下发'],
]

const MODE_OPTIONS = MODES.map(([value, title, description]) => ({ value, title, description }))

/**
 * 全局设备数策略与接近/超出上限的订阅（10-16 起放在安全模块）。
 * props:
 *   - now: 可选的基准时间；不传时组件内部每分钟自动刷新一次，用于计算「最近在线」相对时间。
 */
export function DevicePolicy({ now: propNow }: { now?: Date } = {}) {
  const [internalNow, setInternalNow] = useState(() => new Date())
  useEffect(() => {
    if (propNow) return
    const timer = setInterval(() => setInternalNow(new Date()), 60_000)
    return () => clearInterval(timer)
  }, [propNow])
  const now = propNow ?? internalNow

  const devices = useDevices()

  return (
    <div className={signupCss.dpStack}>
      <SectionTitle variant="group" hint="单个订阅的上限在用户详情「订阅与设备」里覆盖">
        全局设备数策略
      </SectionTitle>
      {devices.data ? (
        <PolicyForm
          key={`${devices.data.mode}-${devices.data.grace}-${devices.data.window_minutes}`}
          mode={devices.data.mode}
          grace={devices.data.grace}
          window={devices.data.window_minutes}
        />
      ) : devices.isError ? (
        <Card>
          <Empty bare title="策略读取失败" description="稍后重试。" />
        </Card>
      ) : (
        <Card>
          <p className={signupCss.dpMuted}>加载中…</p>
        </Card>
      )}
      <Card
        flush
        title="接近或超出上限的订阅"
        footer={
          <span className={signupCss.dpMuted}>
            在线按近 {devices.data?.window_minutes ?? 5} 分钟内的来源 IP 去重；统计生效中订阅里在线最多的 200 条。
          </span>
        }
      >
        <QueryView
          query={devices}
          rows={4}
          isEmpty={(d) => nearLimit(d.devices).length === 0}
          empty={<Empty bare title="没有接近上限的订阅" description="在线设备数达到上限的订阅会出现在这里。" />}
        >
          {(d) => <HotList rows={nearLimit(d.devices)} now={now} />}
        </QueryView>
      </Card>
    </div>
  )
}

function PolicyForm({ mode: saved, grace: savedGrace, window: savedWindow }: { mode: DeviceMode; grace: number; window: DeviceWindow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const [mode, setMode] = useState(saved)
  const [grace, setGrace] = useState(String(savedGrace))
  const [win, setWin] = useState<DeviceWindow>(savedWindow)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const dirty = mode !== saved || (mode === 'strict' && grace.trim() !== String(savedGrace)) || win !== savedWindow

  const save = async () => {
    const body = devicePolicyBody({ mode, grace, window: win }, savedWindow)
    if (!body) return setError('宽容值是 0 到 5 的整数')
    setBusy(true)
    try {
      await api.post('v1/settings/device-limit', okSchema, { body })
      toast('全局设备策略已保存')
      void invalidate()
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const strict = mode === 'strict'

  const footer = (
    <>
      <span className={signupCss.dpFooterStatus}>{dirty ? '有未保存的修改' : '当前策略已生效'}</span>
      <span className={signupCss.dpFooterSpacer} />
      <Button size="sm" variant="primary" busy={busy} disabled={!dirty} onClick={() => void save()}>
        保存策略
      </Button>
    </>
  )

  return (
    <Card footer={footer}>
      <p className={signupCss.dpFieldLabel}>超出上限时</p>
      <RadioCards
        label="超出上限时"
        columns={2}
        options={MODE_OPTIONS}
        value={mode}
        onChange={(value) => {
          setMode(value)
          setError(null)
        }}
      />
      <div className={signupCss.dpTwoCols}>
        <Input
          label="宽容值（0–5）"
          mono
          inputMode="numeric"
          value={grace}
          disabled={busy || !strict}
          error={error ?? undefined}
          hint={
            strict
              ? '在线设备超过「上限 + 宽容值」才算超限、停止下发。比如上限 3、宽容值 1，同时在线第 5 台才停；用来容忍换网络时旧连接还没断的那一会儿。'
              : '只对「超限停止下发」生效；「拒绝新设备」由节点按上限直接拒，不看宽容值。'
          }
          onChange={(e) => {
            setGrace(e.target.value)
            setError(null)
          }}
        />
        <Select
          label="设备识别窗口"
          value={String(win)}
          options={DEVICE_WINDOWS.map((m) => ({ value: String(m), label: `${m} 分钟` }))}
          disabled={busy}
          onChange={(e) => setWin(Number(e.target.value) as DeviceWindow)}
          hint={
            strict
              ? '多少分钟内出现过的不同来源 IP，算作同时在线的不同设备。窗口越长，换了网络的旧 IP 被多算得越久；超限停止下发的订阅，大约要一个窗口后才恢复。'
              : '多少分钟内出现过的不同来源 IP，算作同时在线的不同设备。窗口越长，换了网络的旧 IP 被多算得越久。'
          }
        />
      </div>
    </Card>
  )
}

function pipClass(p: 'on' | 'over' | 'off', exceeded: boolean): string | undefined {
  if (p === 'off') return signupCss.dpDot
  if (p === 'on') return `${signupCss.dpDot} ${signupCss.dpDotOn}`
  return exceeded ? `${signupCss.dpDot} ${signupCss.dpDotOverDanger}` : `${signupCss.dpDot} ${signupCss.dpDotOver}`
}

function HotList({ rows, now }: { rows: OnlineDevice[]; now: Date }) {
  const toast = useToast()
  const fail = useFailure()
  const find = useFindUserByEmail()
  // 这里只有 subscription_id 与邮箱：按邮箱找到用户再打开抽屉的「订阅」标签
  const open = async (row: OnlineDevice) => {
    try {
      const user = await find(row.email)
      if (user) navigate(`/users/list/${encodeURIComponent(user.id)}/sub`)
      else toast('找不到这个用户，可能已被删除', 'danger')
    } catch (e) {
      fail(e)
    }
  }
  const columns: TableColumn<OnlineDevice>[] = [
    {
      key: 'who',
      header: '订阅',
      render: (d) => (
        <>
          <span>{d.email || '—'}</span>
          <span className={signupCss.dpSubLine}>
            {d.plan || '—'}
            {d.overridden ? ' · 已单独覆盖上限' : ''}
          </span>
        </>
      ),
    },
    {
      key: 'count',
      header: '在线 / 上限',
      width: '120px',
      align: 'right',
      mono: true,
      render: (d) => (
        <>
          <span className={signupCss.dpDots} aria-hidden="true">
            {pips(d.online, d.limit).map((p, i) => (
              <span key={i} className={pipClass(p, d.exceeded)} />
            ))}
          </span>
          {formatCount(d.online)} / {formatCount(d.limit)}
        </>
      ),
    },
    {
      key: 'state',
      header: '状态',
      width: '92px',
      // exceeded 是后端按「上限 + 宽容值」算的；超了上限但还在宽容值内的单独标出来
      render: (d) =>
        d.exceeded ? (
          <Tag tone="danger" title="在线数超过「上限 + 宽容值」">
            已超限
          </Tag>
        ) : d.online > d.limit ? (
          <Tag tone="warn" title="在线数超过了上限，但没超过「上限 + 宽容值」，暂不处理">
            宽容内
          </Tag>
        ) : (
          <Tag tone="neutral" title="在线数到了上限，再多一台就超出">
            已满
          </Tag>
        ),
    },
    {
      key: 'seen',
      header: '最近在线',
      width: '76px',
      hideOnMobile: true,
      align: 'right',
      render: (d) => <span className={signupCss.dpMuted}>{d.last_seen_at ? relativeTime(d.last_seen_at, now) : '—'}</span>,
    },
  ]
  return <Table label="接近或超出上限的订阅" columns={columns} rows={rows} rowKey={(d) => d.subscription_id} onRowClick={(d) => void open(d)} />
}
