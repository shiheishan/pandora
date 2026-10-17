import { PageHelpTip } from '../../PageHelp'
import { pageHelp } from '../../page-help'
import { useAdminMe } from '../../me'
import { useNodeTraffic, useOverview } from './api'
import css from './Dash.module.css'
import { Kpis } from './Kpis'
import { greeting } from './model'
import { Section } from './parts'
import { SystemStatus } from './SystemStatus'
import { Tasks } from './Tasks'
import { TrafficRank } from './TrafficRank'
import { Trend } from './Trend'

// 仪表盘是无标签的落地页，不读登记表传入的 tab 与 rest（AdminScreenProps），所以不声明参数
export default function Dash() {
  const overview = useOverview()
  const nodeTraffic = useNodeTraffic()
  const me = useAdminMe()
  const g = greeting(new Date(), me.data)
  const help = pageHelp('dash', null)

  return (
    <div className={css.page}>
      <div className={css.hello}>
        <div>
          <div className={css.date}>{g.date}</div>
          <h1 className={css.helloTitle}>{g.hello}</h1>
        </div>
        <span className={css.spacer} />
        {help && <PageHelpTip help={help} />}
      </div>
      <Tasks />
      <Kpis overview={overview} />
      <Trend />
      <Section id="dash-ops" title="节点与系统">
        <div className={css.two}>
          <TrafficRank nodes={nodeTraffic} nodesOnline={overview.data?.nodes} />
          <SystemStatus />
        </div>
      </Section>
    </div>
  )
}
