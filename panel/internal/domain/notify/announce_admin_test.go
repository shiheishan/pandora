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
