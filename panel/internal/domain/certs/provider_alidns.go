package certs

import (
	"context"
	"errors"
	"strings"
	"time"

	openapiutil "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	"github.com/alibabacloud-go/tea/dara"
	alidns "github.com/go-acme/alidns-20150109/v4/client"
	"github.com/go-acme/lego/v4/challenge"
	legoalidns "github.com/go-acme/lego/v4/providers/dns/alidns"
)

// aliDNSRegion 是云解析 DNS API 的地域；DNS 是全局服务，地域只决定 endpoint，与 lego 的缺省一致。
const aliDNSRegion = "cn-hangzhou"

// aliDNSProvider 用 RAM 子账号的 AccessKey（授权 AliyunDNSFullAccess，或自定义策略只给
// alidns:DescribeDomains / DescribeDomainRecords / AddDomainRecord / DeleteDomainRecord，资源限定该域名）。
//
// 预检调 DescribeDomains：鉴权失败即拒绝；按 zone 名找得到才算看得到；总数是看得到的域名数。
// 「能写 TXT」只能真写一条再删掉（阿里云没有只读的权限自检接口）。
type aliDNSProvider struct {
	keyID, keySecret string
}

func (p *aliDNSProvider) client() (*alidns.Client, error) {
	cfg := &openapiutil.Config{
		AccessKeyId:     dara.String(p.keyID),
		AccessKeySecret: dara.String(p.keySecret),
		RegionId:        dara.String(aliDNSRegion),
		ReadTimeout:     dara.Int(30_000),
		ConnectTimeout:  dara.Int(10_000),
	}
	return alidns.NewClient(cfg)
}

// aliAuthRejected 判断阿里云的错误是不是凭据问题：HTTP 401/403/404 带 AccessKey、签名、RAM 类错误码。
func aliAuthRejected(err error) (string, bool) {
	var se *dara.SDKError
	if !errors.As(err, &se) {
		return "", false
	}
	code := dara.StringValue(se.Code)
	detail := code
	if msg := dara.StringValue(se.Message); msg != "" {
		detail = code + " " + truncate(strings.Join(strings.Fields(msg), " "), 200)
	}
	for _, prefix := range []string{"InvalidAccessKeyId", "SignatureDoesNotMatch", "Forbidden", "NoPermission",
		"IncompleteSignature", "InvalidAccessKeySecret", "UserNotExist"} {
		if strings.HasPrefix(code, prefix) {
			return detail, true
		}
	}
	return detail, false
}

func (p *aliDNSProvider) check(ctx context.Context, zone string, write bool) (checkResult, error) {
	var res checkResult
	c, err := p.client()
	if err != nil {
		return res, err
	}
	rt := &dara.RuntimeOptions{}
	found := false
	var page int64 = 1
	for {
		resp, err := alidns.DescribeDomainsWithContext(ctx, c, new(alidns.DescribeDomainsRequest).
			SetPageNumber(page).SetPageSize(100), rt)
		if err != nil {
			if detail, auth := aliAuthRejected(err); auth {
				return res, credentialRejected(err, "阿里云拒绝了这个 AccessKey（%s）", detail)
			}
			return res, err
		}
		body := resp.Body
		if body == nil {
			return res, errors.New("阿里云 DescribeDomains 没有返回内容")
		}
		if body.Domains != nil {
			for _, d := range body.Domains.Domain {
				if zoneMatches(dara.StringValue(d.DomainName), zone) || zoneMatches(dara.StringValue(d.PunyCode), zone) {
					found = true
				}
			}
		}
		res.VisibleZones = int(dara.Int64Value(body.TotalCount))
		if dara.Int64Value(body.PageNumber)*dara.Int64Value(body.PageSize) >= dara.Int64Value(body.TotalCount) || page >= 20 {
			break
		}
		page++
	}
	if !found {
		return res, credentialRejected(errZoneNotFound,
			"这个 AccessKey 看不到域名 %s：确认域名在这个阿里云账号的云解析里，RAM 策略的资源包含它", zone)
	}
	if !write {
		return res, nil
	}
	added, err := alidns.AddDomainRecordWithContext(ctx, c, new(alidns.AddDomainRecordRequest).
		SetDomainName(zone).SetRR(checkRecordLabel).SetType("TXT").SetValue(checkRecordValue()).SetTTL(600), rt)
	if err != nil {
		if detail, auth := aliAuthRejected(err); auth {
			return res, credentialRejected(err, "这个 AccessKey 不能在 %s 里写 TXT 记录（%s）：需要 alidns:AddDomainRecord", zone, detail)
		}
		return res, err
	}
	if added.Body != nil && added.Body.RecordId != nil {
		if _, err := alidns.DeleteDomainRecordWithContext(ctx, c, &alidns.DeleteDomainRecordRequest{RecordId: added.Body.RecordId}, rt); err != nil {
			res.Warnings = append(res.Warnings, "校验用的 TXT 记录 "+checkRecordLabel+"."+zone+" 没删掉，请手动删除（需要 alidns:DeleteDomainRecord）")
		}
	}
	return res, nil
}

func (p *aliDNSProvider) lego(propagation, polling time.Duration) (challenge.Provider, error) {
	return legoalidns.NewDNSProviderConfig(&legoalidns.Config{
		APIKey:             p.keyID,
		SecretKey:          p.keySecret,
		RegionID:           aliDNSRegion,
		TTL:                600,
		PropagationTimeout: propagation,
		PollingInterval:    polling,
		HTTPTimeout:        30 * time.Second,
	})
}
