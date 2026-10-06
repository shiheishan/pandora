import { useMemo } from 'react'
import { Empty } from '../../../ui'
import { useAdminMe } from '../../me'
import { Activity } from './Activity'
import { useNodeTraffic, useOverview } from './api'
import css from './Dash.module.css'
import { Kpis } from './Kpis'
import { dashboardAccess } from './model'
import { RevenueTrend } from './RevenueTrend'
import { SystemStatus } from './SystemStatus'
import { Tasks } from './Tasks'
import { TrafficRank } from './TrafficRank'

// 仪表盘是无标签的落地页，不读登记表传入的 tab 与 rest（AdminScreenProps），所以不声明参数
export default function Dash() {
  const me = useAdminMe()
  const perms = useMemo<ReadonlySet<string>>(() => new Set(me.data?.permissions ?? []), [me.data])
  const can = dashboardAccess(perms)
  const overview = useOverview(can.overview)
  const nodeTraffic = useNodeTraffic(can.nodeTraffic)

  if (!Object.values(can).some(Boolean)) {
    return <Empty title="仪表盘上没有你能看的内容" description="当前账号没有任何仪表盘数据的读取权限，可以从左侧进入有权限的模块。" />
  }

  return (
    <div className={css.page}>
      <Tasks perms={perms} canTasks={can.tasks} canBacklog={can.backlog} />
      <Kpis perms={perms} overview={overview} traffic={nodeTraffic} canOverview={can.overview} canTraffic={can.nodeTraffic} />
      {(can.overview || can.system) && (
        <div className={css.row}>
          {can.overview && <RevenueTrend />}
          {can.system && <SystemStatus perms={perms} />}
        </div>
      )}
      {(can.activity || can.nodeTraffic || can.userTraffic) && (
        <div className={css.pair}>
          {can.activity && <Activity />}
          {(can.nodeTraffic || can.userTraffic) && <TrafficRank perms={perms} nodes={nodeTraffic} canNodes={can.nodeTraffic} canUsers={can.userTraffic} />}
        </div>
      )}
    </div>
  )
}
