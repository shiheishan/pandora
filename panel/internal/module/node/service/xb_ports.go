package service

// Xboard 补齐（#18 流量上限、#47 时段倍率、#48 ECH）要的两个端口与注入方法。
//
// 装配：app 里 `nodeSvc.UseSiteSettings(settingsSvc)`、`nodeSvc.UseECHSealer(sealer)`。

import (
	"context"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// SiteSettings 是站点设置的读取面（时区），由 settings service 实现。节点流量周期与时段倍率按站点时区算，
// 与 subscription 的 dayOf + siteLocation 同一口径。
type SiteSettings interface {
	Site(ctx context.Context) (settingssvc.Site, error)
}

// ECHSealer 封存与解封 ECH 私钥（*crypto.Sealer 实现）。AAD 用 echKeyAAD(节点 id)。
type ECHSealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

var (
	_ SiteSettings = (*settingssvc.Service)(nil)
	_ ECHSealer    = (*crypto.Sealer)(nil)
)

// UseSiteSettings 只在启动装配时调用（不并发安全）。
func (s *Service) UseSiteSettings(site SiteSettings) { s.site = site }

// UseECHSealer 只在启动装配时调用（不并发安全）。
func (s *Service) UseECHSealer(sealer ECHSealer) { s.echSealer = sealer }

// siteLoc 是站点时区；读设置失败返回错误，时区名无效时退回 UTC（站点设置写入时已校验，这里只兜底）。
// 与 subscription/service/pull.go 的 siteLocation 同口径。
func (s *Service) siteLoc(ctx context.Context) (*time.Location, error) {
	if s.site == nil {
		return nil, errSitePortMissing
	}
	site, err := s.site.Site(ctx)
	if err != nil {
		return nil, err
	}
	if loc, err := time.LoadLocation(site.Timezone); err == nil && site.Timezone != "" {
		return loc, nil
	}
	return time.UTC, nil
}

// errSitePortMissing 是启动时没装配站点设置（装配漏了）。
var errSitePortMissing = errs.Newf(errs.Unavailable, "节点模块未装配站点设置")
