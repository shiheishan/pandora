// 后台页面专用组件套件（样稿 dev/prototype/admin-v4.html）。页面只从这里 import（'../../kit'）。
// 接口与用法见 .claude/rules/screens-admin.md「kit 组件」。
export { cx } from './cx'
export { DetailPage, type DetailPageProps, type DetailPageTabs } from './DetailPage'
export {
  Avatar,
  KvList,
  KvRow,
  SectionTitle,
  SideFact,
  SideFacts,
  StatusDot,
  type AvatarProps,
  type KvListProps,
  type KvRowProps,
  type SectionTitleProps,
  type SideFactProps,
  type StatusDotProps,
  type StatusTone,
} from './Facts'
export { Callout, CopyButton, DangerRow, type CalloutProps, type CopyButtonProps, type DangerRowProps } from './Notes'
export {
  RadioCards,
  SettingRow,
  nextRadioIndex,
  type RadioCardOption,
  type RadioCardsProps,
  type SettingRowProps,
} from './Settings'
export { TagInput, type TagInputProps } from './TagInput'
export { mergeTags, splitTagInput } from './tag-input'
export {
  BulkAction,
  BulkBar,
  Row,
  RowList,
  SaveBar,
  type BulkActionProps,
  type BulkBarProps,
  type RowListProps,
  type RowProps,
  type SaveBarProps,
} from './Rows'
export { PageActions, PageActionsProvider, PageCount, type PageActionsSlots } from './PageActions'
