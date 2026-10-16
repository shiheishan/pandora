import { SignupSettingsPanel } from './SignupForm'

/**
 * 注册防护（10-16 设计稿「安全 › 注册防护」）：人机验证、邮箱后缀白名单、Gmail 别名、注册 IP 限额。
 * 无 props；读写 GET/POST v1/settings/signup，只提交本组件改过的字段。
 */
export function SignupSecurity() {
  return <SignupSettingsPanel section="security" />
}
