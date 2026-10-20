package service

import (
	"strings"
	"testing"
)

func TestSlotMemo_读出再净化(t *testing.T) {
	var m slotMemo
	got := m.clean("portal.footer", `<p onclick="x()">hi</p><script>alert(1)</script>`)
	if strings.Contains(got, "onclick") || strings.Contains(got, "script") {
		t.Fatalf("仍含危险内容: %q", got)
	}
	if !strings.Contains(got, "hi") {
		t.Fatalf("应保留文字 hi: %q", got)
	}
}

func TestSlotMemo_原文不变用备忘_原文变了重算(t *testing.T) {
	var m slotMemo
	key := "portal.footer"
	rawA := `<p>alpha</p>`
	cleanA := m.clean(key, rawA)

	m.mu.Lock()
	if m.entries == nil {
		m.entries = make(map[string]slotMemoEntry)
	}
	m.entries[key] = slotMemoEntry{raw: rawA, clean: "SENTINEL"}
	m.mu.Unlock()

	if m.clean(key, rawA) != "SENTINEL" {
		t.Fatal("同一原文应命中备忘，不重算")
	}

	rawB := `<p>beta</p>`
	cleanB := m.clean(key, rawB)
	if cleanB == "SENTINEL" {
		t.Fatal("原文变了应重算，不能返回哨兵")
	}
	if !strings.Contains(cleanB, "beta") {
		t.Fatalf("B 的净化结果: %q", cleanB)
	}
	if cleanB == cleanA {
		t.Fatalf("A 与 B 净化结果不应相同: %q", cleanB)
	}
}

func TestSlotMemo_reset后重算(t *testing.T) {
	var m slotMemo
	key := "portal.footer"
	rawA := `<p>alpha</p>`
	m.clean(key, rawA)

	m.mu.Lock()
	m.entries[key] = slotMemoEntry{raw: rawA, clean: "SENTINEL"}
	m.mu.Unlock()

	m.reset()
	got := m.clean(key, rawA)
	if got == "SENTINEL" {
		t.Fatal("reset 后应重算，不能返回哨兵")
	}
	if !strings.Contains(got, "alpha") {
		t.Fatalf("净化结果: %q", got)
	}
}

func TestSlotMemo_净化后为空(t *testing.T) {
	var m slotMemo
	if m.clean("portal.footer", `<script>alert(1)</script>`) != "" {
		t.Fatal("纯 script 净化后应为空串")
	}
}
