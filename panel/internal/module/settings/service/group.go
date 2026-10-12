package service

// diff 比较同一组键的旧值与新值（值只会是 string / int / bool），返回要写的键，以及只含变化键的 before / after（给审计用）。
func diff(before, after map[string]any) (changes []change, b, a map[string]any) {
	b = make(map[string]any)
	a = make(map[string]any)
	for k, newVal := range after {
		oldVal := before[k]
		if oldVal != newVal {
			changes = append(changes, change{key: k, value: newVal})
			b[k] = oldVal
			a[k] = newVal
		}
	}
	return changes, b, a
}
