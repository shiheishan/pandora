//go:build !linux && !windows

package main

// The production node agent targets Linux. Unsupported developer platforms
// report an unavailable metric instead of fabricating capacity.
func diskUsage(string) (totalGB, usedGB int) { return 0, 0 }
