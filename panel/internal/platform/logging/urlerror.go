package logging

import "net/url"

// StripURL 去掉出站请求错误（*url.Error）里 URL 的路径、查询串与用户信息，只留 scheme://host。
//
// net/http 的客户端错误会带完整请求 URL，而有的对端把凭证放在 URL 里：Telegram Bot API 的
// /bot<token>/、易支付查询接口的 ?key=、插件钩子地址里的令牌段。这类错误会进日志与库里的
// 失败原因，所以出站调用 client.Do 失败时先经过这里再往上返回。保留主机名便于排障；
// 错误链不变，errors.Is(err, context.DeadlineExceeded)、Timeout() 照常可用。
func StripURL(err error) error {
	// 只认 client.Do 直接返回的那一层：外面再包过的错误，换掉内层会丢掉外层的说明
	ue, ok := err.(*url.Error)
	if !ok {
		return err
	}
	safe := "<invalid url>"
	if u, perr := url.Parse(ue.URL); perr == nil && u.Host != "" {
		safe = u.Scheme + "://" + u.Host
	}
	return &url.Error{Op: ue.Op, URL: safe, Err: ue.Err}
}
