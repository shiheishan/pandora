// 后台页面专用组件套件（样稿 dev/prototype/admin-v4.html）。页面只从这里 import（'../../kit'）。
// 接口与用法见 .claude/rules/screens-admin.md「kit 组件」。只导出页面用到的；props 类型页面要用时再加。
export { cx } from './cx'
export { DetailPage } from './DetailPage'
export { Avatar, KvList, KvRow, SectionTitle, SideFact, SideFacts, StatusDot, type StatusTone } from './Facts'
export { Callout, CopyButton, DangerRow } from './Notes'
export { RadioCards, SettingRow } from './Settings'
export { TagInput } from './TagInput'
export { BulkAction, BulkBar, Row, RowList, SaveBar } from './Rows'
export { PageActions, PageActionsProvider, PageCount } from './PageActions'
export { useNow } from './useNow'
