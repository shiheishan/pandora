package service

import "sync"

// slotMemo 是门户读插槽时「再净化」的进程内备忘：键是插槽 key，记着上次见到的原文与净化结果。
// 原文没变就直接用上次的结果，门户每次加载都调的 GET v1/appearance 不必每次重跑净化器；
// 保存插槽提交后整份清空（SaveSlot 里 tx.AfterCommit），原文比较只是兜底（库里被手工改过的行也会重算）。
// 零值可用；并发安全。
type slotMemo struct {
	mu      sync.Mutex
	entries map[string]slotMemoEntry
}

type slotMemoEntry struct {
	raw   string // 库里的原文
	clean string // SanitizeHTML(raw) 的结果
}

// clean 返回 raw 再净化后的 HTML：同一 key 的原文与上次相同就用备忘，否则净化并记下（每个 key 只留最新一条）。
func (m *slotMemo) clean(key, raw string) string {
	m.mu.Lock()
	if e, ok := m.entries[key]; ok && e.raw == raw {
		clean := e.clean
		m.mu.Unlock()
		return clean
	}
	m.mu.Unlock()

	clean, _ := SanitizeHTML(raw)

	m.mu.Lock()
	if m.entries == nil {
		m.entries = make(map[string]slotMemoEntry)
	}
	m.entries[key] = slotMemoEntry{raw: raw, clean: clean}
	m.mu.Unlock()
	return clean
}

// reset 清空备忘（保存插槽提交后调）。
func (m *slotMemo) reset() {
	m.mu.Lock()
	m.entries = nil
	m.mu.Unlock()
}
