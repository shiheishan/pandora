package certs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	legotencent "github.com/go-acme/lego/v4/providers/dns/tencentcloud"
	dnspod "github.com/go-acme/tencentclouddnspod/v20210323"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	sdkerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// tencentProvider 用 CAM 子用户的 API 密钥（授权 QcloudDNSPodFullAccess，或自定义策略只给
// dnspod:DescribeDomainList / DescribeRecordList / CreateRecord / DeleteRecord，资源限定该域名）。
//
// 预检调 DescribeDomainList：鉴权失败即拒绝；按 zone 名找得到才算看得到；AllTotal 是看得到的域名数。
// 「能写 TXT」只能真写一条再删掉（DNSPod 没有只读的权限自检接口）。
type tencentProvider struct {
	secretID, secretKey string
}

func (p *tencentProvider) client() (*dnspod.Client, error) {
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "dnspod.tencentcloudapi.com"
	cpf.HttpProfile.ReqTimeout = 30
	return dnspod.NewClient(common.NewCredential(p.secretID, p.secretKey), "", cpf)
}

// tencentAuthRejected 判断腾讯云的错误是不是凭据问题（AuthFailure.*、UnauthorizedOperation.*）。
func tencentAuthRejected(err error) (string, bool) {
	var se *sdkerrors.TencentCloudSDKError
	if !errors.As(err, &se) {
		return "", false
	}
	detail := se.Code
	if se.Message != "" {
		detail = se.Code + " " + truncate(strings.Join(strings.Fields(se.Message), " "), 200)
	}
	if strings.HasPrefix(se.Code, "AuthFailure") || strings.HasPrefix(se.Code, "UnauthorizedOperation") ||
		strings.HasPrefix(se.Code, "OperationDenied") {
		return detail, true
	}
	return detail, false
}

func (p *tencentProvider) check(ctx context.Context, zone string, write bool) (checkResult, error) {
	var res checkResult
	c, err := p.client()
	if err != nil {
		return res, err
	}
	var found *dnspod.DomainListItem
	var offset int64
	for range 20 {
		req := dnspod.NewDescribeDomainListRequest()
		req.Offset = common.Int64Ptr(offset)
		req.Limit = common.Int64Ptr(100)
		resp, err := dnspod.DescribeDomainListWithContext(ctx, c, req)
		if err != nil {
			if detail, auth := tencentAuthRejected(err); auth {
				return res, credentialRejected(err, "腾讯云拒绝了这个 API 密钥（%s）", detail)
			}
			return res, err
		}
		if resp.Response == nil {
			return res, errors.New("腾讯云 DescribeDomainList 没有返回内容")
		}
		for _, d := range resp.Response.DomainList {
			if d.Name != nil && zoneMatches(*d.Name, zone) || d.Punycode != nil && zoneMatches(*d.Punycode, zone) {
				found = d
			}
		}
		var total uint64
		if resp.Response.DomainCountInfo != nil && resp.Response.DomainCountInfo.AllTotal != nil {
			total = *resp.Response.DomainCountInfo.AllTotal
		}
		res.VisibleZones = int(total)
		offset += int64(len(resp.Response.DomainList))
		if len(resp.Response.DomainList) == 0 || uint64(offset) >= total {
			break
		}
	}
	if found == nil {
		return res, credentialRejected(errZoneNotFound,
			"这个 API 密钥看不到域名 %s：确认域名在这个腾讯云账号的 DNSPod 里，CAM 策略的资源包含它", zone)
	}
	if !write {
		return res, nil
	}
	req := dnspod.NewCreateRecordRequest()
	req.Domain = common.StringPtr(zone)
	req.DomainId = found.DomainId
	req.SubDomain = common.StringPtr(checkRecordLabel)
	req.RecordType = common.StringPtr("TXT")
	req.RecordLine = common.StringPtr("默认")
	req.Value = common.StringPtr(checkRecordValue())
	req.TTL = common.Uint64Ptr(600)
	created, err := dnspod.CreateRecordWithContext(ctx, c, req)
	if err != nil {
		if detail, auth := tencentAuthRejected(err); auth {
			return res, credentialRejected(err, "这个 API 密钥不能在 %s 里写 TXT 记录（%s）：需要 dnspod:CreateRecord", zone, detail)
		}
		return res, err
	}
	if created.Response != nil && created.Response.RecordId != nil {
		del := dnspod.NewDeleteRecordRequest()
		del.Domain = common.StringPtr(zone)
		del.DomainId = found.DomainId
		del.RecordId = created.Response.RecordId
		if _, err := dnspod.DeleteRecordWithContext(ctx, c, del); err != nil {
			res.Warnings = append(res.Warnings, "校验用的 TXT 记录 "+checkRecordLabel+"."+zone+" 没删掉，请手动删除（需要 dnspod:DeleteRecord）")
		}
	}
	return res, nil
}

func (p *tencentProvider) lego(propagation, polling time.Duration) (challenge.Provider, error) {
	return legotencent.NewDNSProviderConfig(&legotencent.Config{
		SecretID:           p.secretID,
		SecretKey:          p.secretKey,
		TTL:                600,
		PropagationTimeout: propagation,
		PollingInterval:    polling,
		HTTPTimeout:        30 * time.Second,
	})
}
