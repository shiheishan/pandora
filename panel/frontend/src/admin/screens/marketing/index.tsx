import type { AdminScreenProps } from '../index'
import { Commission } from './Commission'
import { Coupons } from './Coupons'
import { Gifts } from './Gifts'

export default function Marketing({ tab, rest }: AdminScreenProps) {
  if (tab === 'gifts') return <Gifts rest={rest} />
  if (tab === 'commission') return <Commission />
  return <Coupons />
}
