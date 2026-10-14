package service

import (
	"fmt"
	"testing"
)

func TestValidateSupportRules(t *testing.T) {
	cases := []struct {
		hours  int
		field  string
		wantOK bool
	}{
		{hours: 0, field: "auto_close_hours"},
		{hours: 1, wantOK: true},
		{hours: 720, wantOK: true},
		{hours: 721, field: "auto_close_hours"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("hours=%d", tc.hours), func(t *testing.T) {
			err := ValidateSupportRules(SupportRules{AutoCloseHours: tc.hours})
			if tc.wantOK {
				if err != nil {
					t.Fatalf("err: %v", err)
				}
				return
			}
			assertFieldValidation(t, err, tc.field)
		})
	}
}
