// [INPUT]: 依赖本包 announce_admin.go 的 validateAnnouncementTransition
// [OUTPUT]: 对外提供 TestAnnouncementLifecycleCannotBypassWithdrawal
// [POS]: domain/notify 公告后台写路径的状态机单测（随实现从 api/admin/announcement_contract_test.go 迁来）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package notify

import "testing"

func TestAnnouncementLifecycleCannotBypassWithdrawal(t *testing.T) {
	for name, tc := range map[string]struct {
		current string
		next    string
		ok      bool
	}{
		"draft-to-published":     {"draft", "published", true},
		"scheduled-to-draft":     {"scheduled", "draft", true},
		"published-edit":         {"published", "published", true},
		"published-to-draft":     {"published", "draft", false},
		"published-to-scheduled": {"published", "scheduled", false},
		"withdrawn-to-published": {"withdrawn", "published", false},
		"withdrawn-to-draft":     {"withdrawn", "draft", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateAnnouncementTransition(tc.current, tc.next)
			if (err == nil) != tc.ok {
				t.Fatalf("transition %s -> %s err=%v, ok=%v", tc.current, tc.next, err, tc.ok)
			}
		})
	}
}
