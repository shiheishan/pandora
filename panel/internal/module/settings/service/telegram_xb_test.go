package service

import (
	"strings"
	"testing"
)

func TestValidateTelegramExtras(t *testing.T) {
	chat := int64(-1001234567890)
	cases := []struct {
		name   string
		in     TelegramExtras
		field  string
		wantOK bool
	}{
		{
			name:   "空讨论链接通过",
			in:     TelegramExtras{DiscussLink: ""},
			wantOK: true,
		},
		{
			name: "https 链接通过",
			in: TelegramExtras{
				DiscussLink: "https://t.me/example_group",
			},
			wantOK: true,
		},
		{
			name: "http 链接拒",
			in: TelegramExtras{
				DiscussLink: "http://example.com/group",
			},
			field: "discuss_link",
		},
		{
			name: "入群审核无群拒",
			in: TelegramExtras{
				JoinReview: true,
			},
			field: "user_group_chat_id",
		},
		{
			name: "群 id 为 0 拒",
			in: TelegramExtras{
				UserGroupChatID: ptrInt64(0),
			},
			field: "user_group_chat_id",
		},
		{
			name: "群 id 为正数拒",
			in: TelegramExtras{
				UserGroupChatID: ptrInt64(42),
			},
			field: "user_group_chat_id",
		},
		{
			name: "超长文案拒",
			in: TelegramExtras{
				StartMessage: strings.Repeat("字", TelegramStartMessageMax+1),
			},
			field: "start_message",
		},
		{
			name: "去空白生效",
			in: TelegramExtras{
				StartMessage:    "  你好  ",
				DiscussLink:     "  https://t.me/x  ",
				JoinReview:      true,
				UserGroupChatID: &chat,
			},
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.in
			err := ValidateTelegramExtras(&v)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("err: %v", err)
				}
				if tc.name == "去空白生效" {
					if v.StartMessage != "你好" || v.DiscussLink != "https://t.me/x" {
						t.Fatalf("trimmed: start=%q discuss=%q", v.StartMessage, v.DiscussLink)
					}
				}
				return
			}
			assertFieldValidation(t, err, tc.field)
		})
	}
}

func ptrInt64(n int64) *int64 { return &n }
