import type { AdminScreenProps } from '../index'
import { AnnounceTab } from './AnnounceTab'
import { KbTab } from './KbTab'
import { ThemeTab } from './ThemeTab'

export default function Content({ tab, rest }: AdminScreenProps) {
  switch (tab) {
    case 'kb':
      return <KbTab rest={rest} />
    case 'theme':
      return <ThemeTab />
    default:
      return <AnnounceTab rest={rest} />
  }
}
