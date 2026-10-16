import type { AdminScreenProps } from '../index'
import { SignupGrowth } from '../users/SignupGrowth'
import { Commission } from './Commission'
import { Coupons } from './Coupons'
import { Gifts } from './Gifts'

/**
 * 地址：/marketing/coupons[/<券 id>]?status=active|paused、/marketing/gifts/<视图>[/<批次 id>]、
 * /marketing/commission/<review|withdraw|settings>?status=open|done|all、/marketing/signup
 */
export default function Marketing({ tab, rest }: AdminScreenProps) {
  if (tab === 'gifts') return <Gifts rest={rest} />
  if (tab === 'commission') return <Commission rest={rest} />
  if (tab === 'signup') return <SignupGrowth />
  return <Coupons rest={rest} />
}
