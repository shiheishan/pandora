package service

import (
	"context"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

// Config 是 payment 的部署参数，app 从配置装配。
type Config struct {
	// PublicBaseURL 是门户对外地址（如 https://example.com），拼回调地址与回跳地址；末尾的 / 会去掉。为空时 CreateIntent 报错。
	PublicBaseURL string
	// AllowPrivateHosts 只在开发环境打开（装配时按 cfg 的环境传 env == development）：允许渠道地址是内网或 http。
	// 生产必须为 false：查单请求在拨号时按真实 IP 拦内网、环回、链路本地与元数据地址，防 SSRF。
	AllowPrivateHosts bool
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// ledgerPoster 是 ledger service 的记账面。
type ledgerPoster interface {
	Post(ctx context.Context, p ledgersvc.Posting) (uuid.UUID, error)
}

// emailDirectory 是 identity service 的批量取邮箱。
type emailDirectory interface {
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// siteReader 是 settings service 的站点设置（取时区算「今日成交」）。
type siteReader interface {
	Site(ctx context.Context) (settingssvc.Site, error)
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// sealer 封存与解封渠道凭据。
type sealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// Service 是 payment 模块对外的唯一入口。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	ledger   ledgerPoster
	identity emailDirectory
	settings siteReader
	audit    auditWriter
	sealer   sealer
	cfg      Config
	settler  Settler
	now      func() time.Time
	// adapter 把一条渠道记录构造成适配器实例；缺省是 buildAdapter，测试可替换。
	adapter func(p providerRecord) (provider.Provider, error)
}

// New 构造 payment service。
func New(r *repo.Repo, txm *tx.Manager, ledger *ledgersvc.Service, identity *identitysvc.Service,
	settings *settingssvc.Service, audit *auditsvc.Service, sl *crypto.Sealer, cfg Config) *Service {
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
	s := &Service{
		repo:     r,
		tx:       txm,
		ledger:   ledger,
		identity: identity,
		settings: settings,
		audit:    audit,
		sealer:   sl,
		cfg:      cfg,
		now:      time.Now,
	}
	s.adapter = s.buildAdapter
	return s
}

// UseSettler 注册订单结算回调；order 构造时调用。没注册时收款入账一律报错回滚。
func (s *Service) UseSettler(st Settler) { s.settler = st }
