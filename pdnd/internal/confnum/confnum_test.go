package confnum

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInt64AcceptsEveryWireForm(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int64
	}{
		{int(7), 7}, {int64(-3), -3}, {uint16(9), 9}, {float64(443), 443},
		{json.Number("443"), 443}, {json.Number("1e3"), 1000}, {json.Number("8.0"), 8},
		{" 15 ", 15}, {"-2", -2},
	} {
		got, ok := Int64(tc.in)
		if !ok || got != tc.want {
			t.Errorf("Int64(%#v) = %d, %v; want %d", tc.in, got, ok, tc.want)
		}
	}
	for _, bad := range []any{nil, true, 1.5, json.Number("1.5"), json.Number("x"), "15s", "", []any{1}, uint64(1 << 63)} {
		if got, ok := Int64(bad); ok {
			t.Errorf("Int64(%#v) = %d, true; want rejected", bad, got)
		}
	}
}

func TestDurationNumbersAreSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want time.Duration
	}{
		{json.Number("10"), 10 * time.Second}, {float64(15), 15 * time.Second}, {int(3), 3 * time.Second},
		{"15s", 15 * time.Second}, {"1m30s", 90 * time.Second}, {"20", 20 * time.Second}, {json.Number("0"), 0},
	} {
		got, ok := Duration(tc.in)
		if !ok || got != tc.want {
			t.Errorf("Duration(%#v) = %v, %v; want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, bad := range []any{nil, json.Number("-1"), "-5s", "abc", 1.5, json.Number("2.5"), false} {
		if got, ok := Duration(bad); ok {
			t.Errorf("Duration(%#v) = %v, true; want rejected", bad, got)
		}
	}
}

func TestTruthy(t *testing.T) {
	for _, tc := range []struct {
		in       any
		want, ok bool
	}{
		{true, true, true}, {false, false, true}, {json.Number("1"), true, true}, {json.Number("0"), false, true},
		{float64(2), true, true}, {"true", true, true}, {"1", true, true}, {"false", false, true}, {"yes", false, false}, {nil, false, false},
	} {
		got, ok := Truthy(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Truthy(%#v) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
