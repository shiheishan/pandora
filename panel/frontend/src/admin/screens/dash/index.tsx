/**
 * [INPUT]: 依赖 react 的 useMemo，依赖 ../../../ui 的 Empty，依赖 ../../me 的 useAdminMe，依赖 ./api 的 useOverview / useNodeTraffic，依赖 ./model 的 dashboardAccess，依赖同目录各卡片组件与 ./Dash.module.css
 * [OUTPUT]: 默认导出 Dash 页面组件（登记表 React.lazy 的目标）
 * [POS]: admin/screens/dash 的入口：仪表盘（后台-01）。登录即可进（modules.ts 里 read 为 null），每张卡按自己接口的权限决定发不发请求、画不画；卡片各自加载、各自失败，互不牵连（DASH-01 局部失败）。概览与节点流量两条查询在这里取一次，KPI 与排行共用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
