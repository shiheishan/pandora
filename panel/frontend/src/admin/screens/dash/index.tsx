import { Activity } from './Activity'
import { useNodeTraffic, useOverview } from './api'
import css from './Dash.module.css'
import { Kpis } from './Kpis'
import { GroupHead } from './parts'
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
      <section className={css.group} aria-labelledby="dash-biz">
        <GroupHead id="dash-biz" title="经营" hint="收入 · 订阅 · 用户增长" />
        <Kpis overview={overview} traffic={nodeTraffic} />
        <div className={css.row}>
          <div className={css.wide}>
            <RevenueTrend />
          </div>
          <div className={css.narrow}>
            <Activity />
          </div>
        </div>
      </section>
      <section className={css.group} aria-labelledby="dash-ops">
        <GroupHead id="dash-ops" title="运行" hint="服务健康 · 节点流量" />
        <div className={css.row}>
          <div className={css.wide}>
            <TrafficRank nodes={nodeTraffic} />
          </div>
          <div className={css.narrow}>
            <SystemStatus />
          </div>
        </div>
      </section>
    </div>
  )
}
