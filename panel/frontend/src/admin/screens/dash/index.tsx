import { Activity } from './Activity'
import { useNodeTraffic, useOverview } from './api'
import css from './Dash.module.css'
import { Kpis } from './Kpis'
import { RevenueTrend } from './RevenueTrend'
import { SystemStatus } from './SystemStatus'
import { Tasks } from './Tasks'
import { TrafficRank } from './TrafficRank'

// 仪表盘是无标签的落地页，不读登记表传入的 tab 与 rest（AdminScreenProps），所以不声明参数
export default function Dash() {
  const overview = useOverview()
  const nodeTraffic = useNodeTraffic()

  return (
    <div className={css.page}>
      <Tasks />
      <Kpis overview={overview} traffic={nodeTraffic} />
      <div className={css.row}>
        <RevenueTrend />
        <SystemStatus />
      </div>
      <div className={css.pair}>
        <Activity />
        <TrafficRank nodes={nodeTraffic} />
      </div>
    </div>
  )
}
