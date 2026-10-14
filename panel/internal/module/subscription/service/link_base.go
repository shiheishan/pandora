package service

import "context"

// linkBase 取生成订阅链接用的地址前缀：后台填了订阅域名就随机取一个（pickLinkBase），没填用面板对外地址。
// 订阅域名走拉取同一份进程内快照（pull_extras.go），后台改动最多 30 秒后生效。
func (s *Service) linkBase(ctx context.Context, d *Delivery) (string, error) {
	ex, err := s.extras.get(ctx, d.Extras, s.repo, s.clock())
	if err != nil {
		return "", err
	}
	return pickLinkBase(ex.settings.LinkBaseURLs, d.PublicBaseURL), nil
}
