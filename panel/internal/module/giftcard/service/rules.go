package service

// 纯函数：模板校验、卡密生成 / 规范化 / 掩码 / 哈希、抽奖、封存载荷、给用户看的回执文案。

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 模板与生码的上下限（与迁移 00015 的 CHECK 一致）。
const (
	maxNameRunes        = 120
	maxDescriptionRunes = 2000
	maxThemeColorRunes  = 32
	maxPrizeLabelRunes  = 100
	maxPrizes           = 50
	minPrizes           = 2
	maxPrizeWeight      = 1000000
	maxExpireDays       = 3650
	maxTrafficBytes     = int64(1) << 50
	maxUsePerUser       = 10000
	maxCooldownHours    = 87600
	maxAllowedPlans     = 100
	maxGenerateCount    = 5000
	maxPrefixLen        = 8
	// generateSampleSize 是生码响应里给出的明文样例张数，够核对格式与前缀
	generateSampleSize = 4
	// codeRandomLen 是卡密随机段的长度；码 = 前缀 + 随机段
	codeRandomLen = 12
	// maskedTail 是掩码遮住的末尾字符数：露出「前缀 + 随机段前 4 位」
	maskedTail = 8
	// 用户输入的卡密长度范围（前缀 0–8 位 + 随机段 12 位，留点余量）
	minCodeLen = 8
	maxCodeLen = 32
	// codeHashPrefix 是卡密 HMAC 的域分隔前缀（子密钥与 identity 的标识哈希同一把，靠前缀隔开用途）
	codeHashPrefix = "giftcard-code:"
)

// codeAlphabet 去掉了 I、L、O、0、1：卡密常要人工抄写或电话报读，这几个字符在多数字体里分不清。
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// validateText 校验来自请求的自由文本：去首尾空白、按字数限长、拒绝 NUL（PG 的 text 不收）。
func validateText(s string, minRunes, maxRunes int) (string, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n < minRunes || n > maxRunes || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
		return "", false
	}
	return s, true
}

// normalizeTemplate 校验并规范化模板输入（不查库：套餐与价格是否存在由 SaveTemplate 经 catalog 再查）。
// 错误是 422，fields 写中文提示。
func normalizeTemplate(in TemplateInput) (TemplateInput, error) {
	fields := map[string]string{}
	name, ok := validateText(in.Name, 1, maxNameRunes)
	if !ok {
		fields["name"] = "名称必填，不超过 120 字"
	}
	in.Name = name
	desc, ok := validateText(in.Description, 0, maxDescriptionRunes)
	if !ok {
		fields["description"] = "说明不超过 2000 字"
	}
	in.Description = desc
	color, ok := validateText(in.ThemeColor, 0, maxThemeColorRunes)
	if !ok {
		fields["theme_color"] = "主题色不超过 32 个字符"
	}
	in.ThemeColor = color
	if in.Status == "" {
		in.Status = TemplateActive
	}
	switch in.Status {
	case TemplateActive, TemplatePaused, TemplateArchived:
	default:
		fields["status"] = "状态只能是 active / paused / archived"
	}
	if msg := validateRewards(in.Type, in.Rewards); msg != "" {
		if in.Type != TypeGeneral && in.Type != TypePlan && in.Type != TypeMystery {
			fields["type"] = msg
		} else {
			fields["rewards"] = msg
		}
	}
	if in.Limits.MaxUsePerUser < 0 || in.Limits.MaxUsePerUser > maxUsePerUser ||
		in.Limits.CooldownHours < 0 || in.Limits.CooldownHours > maxCooldownHours {
		fields["limits"] = "每人次数在 0 到 10000 之间，冷却在 0 到 87600 小时之间"
	}
	if in.Conditions.NewUserOnly && in.Conditions.PaidUserOnly {
		fields["conditions"] = "「仅新用户」和「仅付费用户」互斥，同时勾选等于谁都不能兑"
	}
	in.Conditions.AllowedPlanIDs = dedupeIDs(in.Conditions.AllowedPlanIDs)
	if len(in.Conditions.AllowedPlanIDs) > maxAllowedPlans {
		fields["conditions"] = "限定套餐最多 100 个"
	}
	if len(fields) > 0 {
		return in, errs.Invalid(fields)
	}
	// 只留本卡型用得到的奖励字段，免得存进去一份兑换时不会发的东西
	r := in.Rewards
	switch in.Type {
	case TypeGeneral:
		in.Rewards = Rewards{Balance: r.Balance, TrafficBytes: r.TrafficBytes, ExpireDays: r.ExpireDays, ResetQuota: r.ResetQuota}
	case TypePlan:
		in.Rewards = Rewards{PlanID: r.PlanID, PriceID: r.PriceID}
	case TypeMystery:
		pool := make([]Prize, len(r.Pool))
		for i, p := range r.Pool {
			p.Label = strings.TrimSpace(p.Label)
			pool[i] = p
		}
		in.Rewards = Rewards{Pool: pool}
	}
	return in, nil
}

// validateRewards 把「这张卡到底送什么」在保存时问清楚；返回空串表示合法。
// 一张什么都不送的卡能建出来的话，用户兑完什么都没收到，生产上极难查。
func validateRewards(cardType string, r Rewards) string {
	switch cardType {
	case TypeGeneral:
		if r.Balance.IsNegative() || r.TrafficBytes < 0 || r.ExpireDays < 0 {
			return "奖励数值不能为负"
		}
		if !r.Balance.IsPositive() && r.TrafficBytes == 0 && r.ExpireDays == 0 && !r.ResetQuota {
			return "通用卡至少要送一样东西：余额、流量、延长到期或重置流量"
		}
		if money.Check(r.Balance) != nil {
			return "余额最多两位小数"
		}
		if r.TrafficBytes > maxTrafficBytes {
			return "流量不能超过 1 PiB"
		}
		if r.ExpireDays > maxExpireDays {
			return "延长天数不能超过 3650 天"
		}
	case TypePlan:
		if r.PlanID == nil || *r.PlanID == uuid.Nil {
			return "套餐卡必须指定套餐"
		}
	case TypeMystery:
		if len(r.Pool) < minPrizes {
			return "盲盒至少要有 2 个奖品，否则它就是一张普通卡"
		}
		if len(r.Pool) > maxPrizes {
			return "盲盒奖品不能超过 50 个"
		}
		for i, p := range r.Pool {
			if _, ok := validateText(p.Label, 1, maxPrizeLabelRunes); !ok {
				return fmt.Sprintf("第 %d 个奖品缺少名称或名称超过 100 字 —— 用户中奖后要看到它", i+1)
			}
			if p.Weight <= 0 || p.Weight > maxPrizeWeight {
				return fmt.Sprintf("第 %d 个奖品的权重必须在 1 到 1000000 之间（权重为 0 等于永远抽不到，不如直接删掉）", i+1)
			}
			if p.Balance.IsNegative() || p.TrafficBytes < 0 || p.ExpireDays < 0 {
				return fmt.Sprintf("第 %d 个奖品的数值不能为负", i+1)
			}
			if !p.Balance.IsPositive() && p.TrafficBytes == 0 && p.ExpireDays == 0 {
				return fmt.Sprintf("第 %d 个奖品什么都不送", i+1)
			}
			if money.Check(p.Balance) != nil {
				return fmt.Sprintf("第 %d 个奖品的余额最多两位小数", i+1)
			}
			if p.TrafficBytes > maxTrafficBytes || p.ExpireDays > maxExpireDays {
				return fmt.Sprintf("第 %d 个奖品的流量不能超过 1 PiB、天数不能超过 3650", i+1)
			}
		}
	default:
		return "不支持的卡型"
	}
	return ""
}

// dedupeIDs 去重并去掉零值，保持原有次序。
func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// normalizePrefix 把生码前缀转大写并校验：只用大写字母和数字，最多 8 位。
func normalizePrefix(p string) (string, bool) {
	p = strings.ToUpper(strings.TrimSpace(p))
	if len(p) > maxPrefixLen {
		return "", false
	}
	for _, c := range p {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", false
		}
	}
	return p, true
}

// newCode 生成一张卡密：前缀 + 12 位随机段（crypto/rand，拒绝采样去掉模偏差）。
func newCode(prefix string) (string, error) {
	const n = len(codeAlphabet)
	// 256 以内 n 的最大整数倍；超出的字节丢掉重抽，每个字符等概率
	limit := byte(256 / n * n)
	out := make([]byte, 0, codeRandomLen)
	buf := make([]byte, codeRandomLen*2)
	for len(out) < codeRandomLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out = append(out, codeAlphabet[int(b)%n])
			if len(out) == codeRandomLen {
				break
			}
		}
	}
	return prefix + string(out), nil
}

// normalizeCode 规范化用户输入的卡密：去首尾空白、转大写；长度不在 8–32 或含非 ASCII 字母数字时 ok=false。
func normalizeCode(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < minCodeLen || len(code) > maxCodeLen {
		return "", false
	}
	for _, c := range code {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", false
		}
	}
	return code, true
}

// MaskCode 把明文卡密变成可展示的掩码，例如 GCH2K9Q7MNPRST → GCH2K9••••••••；唯一的 Go 掩码实现。
func MaskCode(code string) string {
	n := utf8.RuneCountInString(code)
	if n <= maskedTail {
		return strings.Repeat("•", n)
	}
	runes := []rune(code)
	return string(runes[:n-maskedTail]) + strings.Repeat("•", maskedTail)
}

// hashCode 是卡密落库与查找用的 HMAC-SHA256（主密钥派生的子密钥，带用途前缀）。输入必须已规范化。
func (s *Service) hashCode(code string) []byte {
	return s.hasher.HMAC(crypto.InfoHMACIdentity, []byte(codeHashPrefix+code))
}

// batchAAD 是批次密文的附加数据：绑批次 id，密文换到别的批次上打不开。
func batchAAD(batchID uuid.UUID) []byte {
	return []byte("gift_card_batch:" + batchID.String())
}

// encodeCodes / decodeCodes 是封存载荷：卡密按生成顺序以换行分隔（字符集里没有换行）。
func encodeCodes(codes []string) []byte { return []byte(strings.Join(codes, "\n")) }

func decodeCodes(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(string(b), "\n")
}

// drawPrize 按权重抽奖。用 crypto/rand：奖池有实际价值，可预测的随机数意味着有人能算出什么时候兑能中大奖。
func drawPrize(pool []Prize) (Prize, error) {
	total := int64(0)
	for _, p := range pool {
		total += int64(p.Weight)
	}
	if total <= 0 {
		return Prize{}, errors.New("giftcard: 奖池权重合计必须大于 0")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(total))
	if err != nil {
		return Prize{}, err
	}
	pick := n.Int64()
	acc := int64(0)
	for _, p := range pool {
		acc += int64(p.Weight)
		if pick < acc {
			return p, nil
		}
	}
	return pool[len(pool)-1], nil
}

// formatAmount 按币种格式化金额：人民币用 ¥，其余写币种代码。
func formatAmount(d decimal.Decimal, cur money.Currency) string {
	s := d.StringFixed(money.Scale)
	if cur == "" || cur == money.CNY {
		return "¥" + s
	}
	return string(cur) + " " + s
}

// formatBytes 把字节数写成 GB / MB（向下取整）。
func formatBytes(b int64) string {
	const gb = int64(1) << 30
	const mb = int64(1) << 20
	if b >= gb {
		return strconv.FormatInt(b/gb, 10) + " GB"
	}
	return strconv.FormatInt(b/mb, 10) + " MB"
}
