import type { AdminScreenProps } from '../index'
import { CatalogTab } from './CatalogTab'
import { PacksTab } from './PacksTab'

export default function Plans({ tab, rest }: AdminScreenProps) {
  if (tab === 'packs') return <PacksTab />
  return <CatalogTab selected={rest[0] ?? null} />
}
