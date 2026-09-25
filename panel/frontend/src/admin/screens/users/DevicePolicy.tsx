/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/format 的 formatCount / relativeTime，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / Empty / Input / QueryView / Table / Tag / useToast，依赖 ../../actions 的 useCan / useFailure，依赖 ./api 的 useDevices / useFindUserByEmail / useInvalidateUsers / okSchema / DeviceMode / OnlineDevice，依赖 ./model 的 nearLimit / pips，依赖 ./Users.module.css 与 ./Ops.module.css
 * [OUTPUT]: 对外提供 DevicePolicy
 * [POS]: 用户页「设备策略」标签（#/users/devices）：左「全局设备数策略」（loose / strict 两种判定，strict 时给宽容值 0–5，POST v1/settings/device-limit 要 reauth），右「接近或超出上限的订阅」（GET v1/devices 里有上限且在线已到上限的，点行按邮箱找到用户、打开抽屉「订阅」标签）。契约后台-03：模式文案以后端为准；D-B-5 已决（5.A.2）不给「默认同时在线设备」滑块；R103 设备识别窗口可选 5 / 10 / 30 / 60 分钟，下拉旁说明代价（旧 IP 被多算更久、strict 超限约一个窗口后才恢复），改了才带 window_minutes（省略 = 不改）；右侧脚注跟着窗口变
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { formatCount, relativeTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Empty, Input, QueryView, Select, Table, Tag, useToast, type TableColumn } from '../../../ui'
import { useCan, useFailure } from '../../actions'
import { DEVICE_WINDOWS, okSchema, useDevices, useFindUserByEmail, useInvalidateUsers, type DeviceMode, type DeviceWindow, type OnlineDevice } from './api'
import { devicePolicyBody, nearLimit, pips } from './model'
import ops from './Ops.module.css'
import css from './Users.module.css'

// 设计稿的「拒绝新设备 / 踢下最早的设备」按后端两种判定改写（契约后台-03 GET v1/devices）
const MODES: ReadonlyArray<[DeviceMode, string, string]> = [
  ['loose', '拒绝新设备', '节点本地判定，超限的新连接被拒；已在线的设备不受影响'],
  ['strict', '超限停止下发', '面板跨节点汇总，超出上限 + 宽容值后停止向该订阅下发'],
]

export function DevicePolicy({ now }: { now: Date }) {
  const devices = useDevices()
  return (
    <div className={ops.splitDevices}>
      <Card
        title={
          <span className={css.stack}>
            全局设备数策略
            <span className={`${css.small} ${ops.titleNote}`}>单个订阅的上限在用户详情「订阅」标签里覆盖</span>
          </span>
        }
      >
        {devices.data ? (
          // 服务端值变了（保存后重拉）就重置草稿
          <PolicyForm key={`${devices.data.mode}-${devices.data.grace}-${devices.data.window_minutes}`} mode={devices.data.mode} grace={devices.data.grace} window={devices.data.window_minutes} />
        ) : devices.isError ? (
          <Empty bare title="策略读取失败" description="稍后重试。" />
        ) : (
          <p className={css.small}>加载中…</p>
        )}
      </Card>
      <Card flush title="接近或超出上限的订阅">
        <div className={ops.bare}>
          <QueryView
            query={devices}
            rows={4}
            isEmpty={(d) => nearLimit(d.devices).length === 0}
            empty={<Empty bare title="没有接近上限的订阅" description="在线设备数达到上限的订阅会出现在这里。" />}
          >
            {(d) => <HotList rows={nearLimit(d.devices)} now={now} />}
          </QueryView>
        </div>
        <p className={ops.cardFoot}>在线按近 {devices.data?.window_minutes ?? 5} 分钟内的来源 IP 去重；统计生效中、试用与宽限期订阅里在线最多的 200 条。</p>
      </Card>
    </div>
  )
}

function PolicyForm({ mode: saved, grace: savedGrace, window: savedWindow }: { mode: DeviceMode; grace: number; window: DeviceWindow }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const canWrite = can('iam.user.write')
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

  return (
    <div className={ops.formStack}>
      <fieldset className={ops.modeSet} disabled={!canWrite}>
        <legend className={css.fieldLabel}>超出上限时</legend>
        {MODES.map(([value, title, desc]) => (
          <label key={value} className={mode === value ? `${ops.modeOption} ${ops.modeOn}` : ops.modeOption}>
            <input type="radio" name="device-mode" value={value} checked={mode === value} onChange={() => setMode(value)} />
            <span className={css.stack}>
              <span className={ops.groupName}>{title}</span>
              <span className={css.small}>{desc}</span>
            </span>
          </label>
        ))}
      </fieldset>
      {mode === 'strict' && (
        <Input
          label="宽容值"
          mono
          inputMode="numeric"
          value={grace}
          disabled={!canWrite}
          error={error ?? undefined}
          hint="在线数超过「上限 + 宽容值」才停止下发，给换网、重连留余量（0–5）"
          onChange={(e) => {
            setGrace(e.target.value)
            setError(null)
          }}
        />
      )}
      <Select
        label="设备识别窗口"
        value={String(win)}
        disabled={!canWrite}
        options={DEVICE_WINDOWS.map((m) => ({ value: String(m), label: `${m} 分钟` }))}
        onChange={(e) => setWin(Number(e.target.value) as DeviceWindow)}
        hint={
          mode === 'strict'
            ? '窗口内出现过的来源 IP 都算在线。窗口越长，换了网络的旧 IP 被多算得越久；超限停止下发的订阅，大约要一个窗口后才恢复。'
            : '窗口内出现过的来源 IP 都算在线。窗口越长，换了网络的旧 IP 被多算得越久。'
        }
      />
      {canWrite && (
        <Button variant="primary" busy={busy} disabled={!dirty} onClick={() => void save()}>
          保存策略
        </Button>
      )}
    </div>
  )
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
        <span className={css.stack}>
          <span className={ops.ellipsis}>{d.email || '—'}</span>
          <span className={css.small}>
            {d.plan || '—'}
            {d.overridden ? ' · 已单独覆盖上限' : ''}
          </span>
        </span>
      ),
    },
    {
      key: 'pips',
      header: '在线',
      width: '120px',
      render: (d) => (
        <span className={css.pips} aria-hidden="true">
          {pips(d.online, d.limit).map((p, i) => (
            <span key={i} className={p === 'off' ? css.pip : `${css.pip} ${p === 'over' ? css.pipOver : css.pipOn}`} />
          ))}
        </span>
      ),
    },
    {
      key: 'count',
      header: '在线 / 上限',
      width: '84px',
      align: 'right',
      mono: true,
      render: (d) => (
        <span className={d.exceeded ? css.tone_danger : css.tone_warn}>
          {formatCount(d.online)}/{formatCount(d.limit)}
        </span>
      ),
    },
    {
      key: 'state',
      header: '状态',
      width: '92px',
      // exceeded 是后端按「上限 + 宽容值」算的；超了上限但还在宽容值内的单独标出来
      render: (d) => (d.exceeded ? <Tag tone="danger">已超限</Tag> : d.online > d.limit ? <Tag tone="warn">宽容内</Tag> : <Tag tone="warn">已满</Tag>),
    },
    { key: 'seen', header: '最近在线', width: '76px', align: 'right', render: (d) => <span className={css.muted}>{d.last_seen_at ? relativeTime(d.last_seen_at, now) : '—'}</span> },
  ]
  return <Table label="接近或超出上限的订阅" columns={columns} rows={rows} rowKey={(d) => d.subscription_id} onRowClick={(d) => void open(d)} />
}
