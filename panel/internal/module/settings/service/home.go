// 门户首页（home 组）设置：开关、介绍与常见问题。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 首页这一组的键与审计动作。
const (
	keyHomeEnabled   = "home.enabled" // bool，缺省 DefaultHomeEnabled；关了门户根 / 就是登录页
	keyHomeIntro     = "home.intro"   // string，纯文本，最多 HomeIntroMax 字，可空，允许换行
	keyHomeFAQ       = "home.faq"     // []HomeFAQ，最多 HomeFAQMax 条
	auditHomeChanged = "home.settings_changed"
)

// DefaultHomeEnabled 是「门户根显示首页」的缺省值：开。
const DefaultHomeEnabled = true

// 首页文字的上限（按字符数）。
const (
	HomeIntroMax       = 500
	HomeFAQMax         = 20
	HomeFAQQuestionMax = 100
	HomeFAQAnswerMax   = 500
)

var homeKeys = []string{keyHomeEnabled, keyHomeIntro, keyHomeFAQ}

// HomeFAQ 是常见问题的一条；库里按 JSON 数组存（[{"q":…,"a":…}]）。
type HomeFAQ struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// Home 是门户首页的设置。FAQ 永不为 nil。
type Home struct {
	Enabled bool
	Intro   string
	FAQ     []HomeFAQ
}

// HomeUpdate 是首页设置的部分更新：nil 字段保持原值。FAQ 非 nil 时整组替换。
type HomeUpdate struct {
	Enabled *bool
	Intro   *string
	FAQ     *[]HomeFAQ
}

// decodeHome 按缺省值解出首页设置。
func decodeHome(vals map[string]json.RawMessage) (Home, error) {
	enabled, err := decodeOr(vals, keyHomeEnabled, DefaultHomeEnabled)
	if err != nil {
		return Home{}, err
	}
	intro, err := decodeOr(vals, keyHomeIntro, "")
	if err != nil {
		return Home{}, err
	}
	faq, err := decodeOr(vals, keyHomeFAQ, []HomeFAQ{})
	if err != nil {
		return Home{}, err
	}
	if faq == nil {
		faq = []HomeFAQ{}
	}
	return Home{
		Enabled: enabled,
		Intro:   intro,
		FAQ:     faq,
	}, nil
}

// Home 读取门户首页设置。
func (s *Service) Home(ctx context.Context) (Home, error) {
	vals, err := s.store.Values(ctx, homeKeys)
	if err != nil {
		return Home{}, err
	}
	return decodeHome(vals)
}

// SetHome 在锁住本组各行的事务里合并、校验并写入首页设置。
func (s *Service) SetHome(ctx context.Context, u HomeUpdate) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, homeKeys)
		if err != nil {
			return err
		}
		old, err := decodeHome(vals)
		if err != nil {
			return err
		}
		v := old
		v.FAQ = slices.Clone(old.FAQ)
		if u.Enabled != nil {
			v.Enabled = *u.Enabled
		}
		if u.Intro != nil {
			v.Intro = normalizeMultiline(*u.Intro)
		}
		if u.FAQ != nil {
			out := make([]HomeFAQ, len(*u.FAQ))
			for i, item := range *u.FAQ {
				out[i] = HomeFAQ{
					Q: strings.TrimSpace(item.Q),
					A: normalizeMultiline(item.A),
				}
			}
			v.FAQ = out
		}
		if err := validateHome(v); err != nil {
			return err
		}

		var changes []change
		before := map[string]any{}
		after := map[string]any{}
		if old.Enabled != v.Enabled {
			before[keyHomeEnabled] = old.Enabled
			after[keyHomeEnabled] = v.Enabled
			changes = append(changes, change{key: keyHomeEnabled, value: v.Enabled})
		}
		if old.Intro != v.Intro {
			before[keyHomeIntro] = old.Intro
			after[keyHomeIntro] = v.Intro
			changes = append(changes, change{key: keyHomeIntro, value: v.Intro})
		}
		if !slices.Equal(old.FAQ, v.FAQ) {
			// 整份 FAQ 最长约 36KB（20 条 × 600 字），超过审计 data 的 16KB 上限，只记条数与摘要
			before[keyHomeFAQ+"_count"], before[keyHomeFAQ+"_sha256"] = len(old.FAQ), faqDigest(old.FAQ)
			after[keyHomeFAQ+"_count"], after[keyHomeFAQ+"_sha256"] = len(v.FAQ), faqDigest(v.FAQ)
			changes = append(changes, change{key: keyHomeFAQ, value: v.FAQ})
		}
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, auditHomeChanged, auditResourceSettings, map[string]any{
			"group":  "home",
			"before": before,
			"after":  after,
		}); err != nil {
			return err
		}
		s.homeChanged(ctx)
		return nil
	})
}

// faqDigest 是整份 FAQ 的 JSON 的 SHA-256（小写 hex），审计里用来看出改没改、改回去没有。
func faqDigest(faq []HomeFAQ) string {
	b, _ := json.Marshal(faq) // 只有字符串字段，不会失败
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func normalizeMultiline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func hasControlExceptNewline(s string) bool {
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validateHome(v Home) error {
	fields := map[string]string{}
	if utf8.RuneCountInString(v.Intro) > HomeIntroMax {
		fields["intro"] = "介绍最多 500 个字"
	} else if hasControlExceptNewline(v.Intro) {
		fields["intro"] = "介绍不能包含控制字符"
	}
	if len(v.FAQ) > HomeFAQMax {
		fields["faq"] = "常见问题最多 20 条"
	} else {
		for i, item := range v.FAQ {
			qk := "faq." + strconv.Itoa(i) + ".q"
			ak := "faq." + strconv.Itoa(i) + ".a"
			if item.Q == "" {
				fields[qk] = "问题必填"
			} else if utf8.RuneCountInString(item.Q) > HomeFAQQuestionMax {
				fields[qk] = "问题最多 100 个字"
			} else if hasControl(item.Q) {
				fields[qk] = "问题不能包含换行或控制字符"
			}
			if item.A == "" {
				fields[ak] = "回答必填"
			} else if utf8.RuneCountInString(item.A) > HomeFAQAnswerMax {
				fields[ak] = "回答最多 500 个字"
			} else if hasControlExceptNewline(item.A) {
				fields[ak] = "回答不能包含控制字符"
			}
		}
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}
