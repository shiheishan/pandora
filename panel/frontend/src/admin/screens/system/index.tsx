import type { AdminScreenProps } from '../index'
import { ChannelsTab } from './ChannelsTab'
import { HooksTab } from './HooksTab'
import { TemplatesTab } from './TemplatesTab'

export default function System({ tab }: AdminScreenProps) {
  switch (tab) {
    case 'templates':
      return <TemplatesTab />
    case 'hooks':
      return <HooksTab />
    default:
      return <ChannelsTab />
  }
}
