import { href } from '../../../core/router'
import { UPDATE_GUIDE_PARAM, thisDevice } from './app-update'
import { flowCss } from './Flow'

/**
 * 「点一次更新」旁边的链接：去帮助中心里这台设备那一段（#/help?update=<设备>），
 * 那里按 App 写了在哪个页面、点哪个按钮、看到什么算成功。
 */
export function UpdateHelpLink() {
  const device = thisDevice()
  return (
    <a className={flowCss.textButton} href={href('/help', { [UPDATE_GUIDE_PARAM]: device })} id="link-update-help">
      不知道点哪？看看你的 App 在哪点更新 ›
    </a>
  )
}
