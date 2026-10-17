import { SignupSettingsPanel } from './SignupForm'

/**
 * 注册试用与邀请（10-16 设计稿「营销 › 注册与邀请」）：试用套餐与时长、邀请码上限与有效期、邮件链接登录。
 * 无 props；读写 GET/POST v1/settings/signup，只提交本组件改过的字段。
 */
export function SignupGrowth() {
  return <SignupSettingsPanel section="growth" />
}
