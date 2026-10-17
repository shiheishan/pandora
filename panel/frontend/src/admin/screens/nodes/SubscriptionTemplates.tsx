import { Card, Table, Tag } from '../../../ui'
import { CLIENT_FORMATS } from './subscription'
import css from './subscription.module.css'

/** 订阅模板：只读表格，与后端 render.DetectFormat 同口径（数据在 subscription.ts） */
export function SubscriptionTemplates() {
  return (
    <Card title="订阅模板" subtitle="按客户端的 UA 认格式；认不出的给通用格式" flush>
      <Table
        label="订阅模板"
        rows={CLIENT_FORMATS}
        rowKey={(r) => r.clients}
        columns={[
          {
            key: 'clients',
            header: '客户端',
            render: (row) => (
              <span className={css.clients}>
                {row.clients}
                {row.fallback ? <Tag tone="neutral">默认</Tag> : null}
              </span>
            ),
          },
          {
            key: 'ua',
            hideOnMobile: true,
            header: 'UA 关键词',
            mono: true,
            render: (row) => (
              <span className={css.cell2} title={row.ua}>
                {row.ua}
              </span>
            ),
          },
          {
            key: 'format',
            header: '格式',
            render: (row) => <span className={css.cell2}>{row.format}</span>,
          },
        ]}
      />
    </Card>
  )
}
