package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	btcpay "github.com/aegispanel/aegis/internal/module/payment/provider/btcpay"
	coinpayments "github.com/aegispanel/aegis/internal/module/payment/provider/coinpayments"
	"github.com/aegispanel/aegis/internal/module/payment/provider/epay"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// credentials 是渠道凭据明文（封存前 JSON 编码）。
type credentials struct {
	MerchantID    string `json:"merchant_id,omitempty"`
	Key           string `json:"key,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// providerRecord 是读出并解封后的一条渠道。
type providerRecord struct {
	ID                                    uuid.UUID
	Code, Adapter, DisplayName            string
	Enabled, AcceptingNew, HasCredentials bool
	Currencies                            []string
	BaseURL, SubmitPath, APIPath          string
	Methods                               []string
	DefaultMethod                         string
	AllowPrivateHost                      bool
	MinAmount                             decimal.Decimal
	FeeFixed, FeePercent                  decimal.Decimal
	NotifyURLs                            []string // 验签可接受的回调地址，adapterFor 填
	Creds                                 credentials
}

func credentialsAAD(id uuid.UUID) []byte {
	return []byte("payment_provider:" + id.String())
}

func (s *Service) sealCredentials(id uuid.UUID, c credentials) ([]byte, error) {
	plain, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return s.sealer.Seal(plain, credentialsAAD(id))
}

func (s *Service) openCredentials(id uuid.UUID, sealed []byte) (credentials, error) {
	if len(sealed) == 0 {
		return credentials{}, nil
	}
	plain, err := s.sealer.Open(sealed, credentialsAAD(id))
	if err != nil {
		return credentials{}, errors.New("渠道凭据无法解密")
	}
	var c credentials
	if err := json.Unmarshal(plain, &c); err != nil {
		return credentials{}, errors.New("渠道凭据格式无效")
	}
	return c, nil
}

func (s *Service) record(m model.Provider) (providerRecord, error) {
	creds, err := s.openCredentials(m.ID, m.CredentialsSealed)
	if err != nil {
		return providerRecord{}, err
	}
	return providerRecord{
		ID:               m.ID,
		Code:             m.Code,
		Adapter:          m.Adapter,
		DisplayName:      m.DisplayName,
		Enabled:          m.Enabled,
		AcceptingNew:     m.AcceptingNew,
		HasCredentials:   len(m.CredentialsSealed) > 0,
		Currencies:       m.Currencies,
		BaseURL:          m.BaseURL,
		SubmitPath:       m.SubmitPath,
		APIPath:          m.APIPath,
		Methods:          m.Methods,
		DefaultMethod:    m.DefaultMethod,
		AllowPrivateHost: m.AllowPrivateHost,
		MinAmount:        m.MinAmount,
		FeeFixed:         m.FeeFixed,
		FeePercent:       m.FeePercent,
		Creds:            creds,
	}, nil
}

func (s *Service) loadProvider(ctx context.Context, code string) (providerRecord, error) {
	m, ok, err := s.repo.ProviderByCode(ctx, code)
	if err != nil {
		return providerRecord{}, err
	}
	if !ok {
		return providerRecord{}, ErrProviderNotFound
	}
	return s.record(m)
}

func (s *Service) buildAdapter(p providerRecord) (provider.Provider, error) {
	switch p.Adapter {
	case AdapterEpay:
		return epay.New(epay.Config{
			BaseURL:          p.BaseURL,
			MerchantID:       p.Creds.MerchantID,
			Key:              p.Creds.Key,
			SubmitPath:       p.SubmitPath,
			APIPath:          p.APIPath,
			DefaultMethod:    p.DefaultMethod,
			AllowPrivateHost: s.cfg.AllowPrivateHosts && p.AllowPrivateHost,
		})
	case AdapterBTCPay:
		return btcpay.New(btcpay.Config{
			BaseURL:          p.BaseURL,
			StoreID:          p.Creds.MerchantID,
			APIKey:           p.Creds.Key,
			WebhookSecret:    p.Creds.WebhookSecret,
			AllowPrivateHost: s.cfg.AllowPrivateHosts && p.AllowPrivateHost,
		})
	case AdapterCoinPayments:
		return coinpayments.New(coinpayments.Config{
			BaseURL:          p.BaseURL,
			ClientID:         p.Creds.MerchantID,
			ClientSecret:     p.Creds.Key,
			NotifyURLs:       p.NotifyURLs,
			AllowPrivateHost: s.cfg.AllowPrivateHosts && p.AllowPrivateHost,
		})
	default:
		return nil, ErrProviderNotFound
	}
}

func (s *Service) adapterFor(ctx context.Context, code string, forNew bool) (providerRecord, provider.Provider, error) {
	p, err := s.loadProvider(ctx, code)
	if err != nil {
		return providerRecord{}, nil, err
	}
	if !p.Enabled {
		return providerRecord{}, nil, ErrProviderDisabled
	}
	if forNew && !p.AcceptingNew {
		return providerRecord{}, nil, ErrProviderPaused
	}
	// 只有 CoinPayments 的回调签名里带回调地址；别的渠道不多读一次设置
	if p.Adapter == AdapterCoinPayments {
		if p.NotifyURLs, err = s.notifyURLCandidates(ctx, p.Code); err != nil {
			return providerRecord{}, nil, err
		}
	}
	ad, err := s.adapter(p)
	if errors.Is(err, ErrProviderNotFound) {
		// 线下渠道没有适配器：对回调与查单就是「没有这个渠道」
		return providerRecord{}, nil, err
	}
	if err != nil {
		return providerRecord{}, nil, errs.Wrap(errs.Internal, err)
	}
	return p, ad, nil
}

func providerMethods(p providerRecord) []string {
	var out []string
	if len(p.Methods) > 0 {
		out = append(out, p.Methods...)
	} else if p.DefaultMethod != "" {
		out = []string{p.DefaultMethod}
	}
	return slices.DeleteFunc(out, func(m string) bool { return !slices.Contains(EpayMethods, m) })
}

func resolveMethod(p providerRecord, requested string) (string, error) {
	allowed := providerMethods(p)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		if len(allowed) == 0 {
			if p.Adapter == AdapterEpay {
				return "", errs.Invalid(map[string]string{"method": "该渠道没有可用的支付方式"})
			}
			return "", nil
		}
		if slices.Contains(allowed, p.DefaultMethod) {
			return p.DefaultMethod, nil
		}
		return allowed[0], nil
	}
	if !slices.Contains(allowed, requested) {
		return "", errs.Invalid(map[string]string{"method": "该渠道不支持所选支付方式"})
	}
	return requested, nil
}

// PaymentMethods 列出门户可选的支付方式。
// amount 是这张单的应付（可为 nil）；给了且为正时每种方式带上这次的手续费与合计。
func (s *Service) PaymentMethods(ctx context.Context, amount *decimal.Decimal) ([]PaymentMethod, error) {
	rows, err := s.repo.ListAcceptingProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := []PaymentMethod{}
	for _, m := range rows {
		if m.Adapter == AdapterOffline {
			continue
		}
		// 列方式用不到凭据，不解封
		methods := providerMethods(providerRecord{Adapter: m.Adapter, Methods: m.Methods, DefaultMethod: m.DefaultMethod})
		if len(methods) == 0 {
			if m.Adapter == AdapterEpay {
				continue
			}
			methods = []string{""}
		}
		currencies := m.Currencies
		if currencies == nil {
			currencies = []string{}
		}
		for _, method := range methods {
			pm := PaymentMethod{
				Provider:    m.Code,
				DisplayName: m.DisplayName,
				Method:      method,
				Currencies:  currencies,
				FeeFixed:    m.FeeFixed,
				FeePercent:  m.FeePercent,
			}
			if amount != nil && amount.IsPositive() {
				fee := Surcharge(*amount, m.FeeFixed, m.FeePercent)
				total := amount.Add(fee)
				pm.Fee = &fee
				pm.Total = &total
			}
			out = append(out, pm)
		}
	}
	return out, nil
}

// MinPayment 返回站点在该币种上的在线支付最低额（元）；非 CNY 为 0。
func (s *Service) MinPayment(ctx context.Context, cur money.Currency) (decimal.Decimal, error) {
	if cur != money.CNY {
		return decimal.Zero, nil
	}
	amt, ok, err := s.repo.MinAcceptingAmount(ctx, "CNY")
	if err != nil {
		return decimal.Zero, err
	}
	if !ok {
		return decimal.Zero, nil
	}
	return amt, nil
}
