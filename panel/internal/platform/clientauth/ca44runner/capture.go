package ca44runner

import (
	"bytes"
	"errors"
	"sync"
)

type BoundedCapture struct {
	mu       sync.Mutex
	limit    int
	data     []byte
	total    uint64
	overflow bool
}

func NewBoundedCapture(limit int) *BoundedCapture {
	if limit < 0 {
		limit = 0
	}
	return &BoundedCapture{limit: limit, data: make([]byte, 0, limit)}
}

func (c *BoundedCapture) Write(value []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ^uint64(0)-c.total < uint64(len(value)) {
		c.total = ^uint64(0)
		c.overflow = true
	} else {
		c.total += uint64(len(value))
	}
	remaining := c.limit - len(c.data)
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		c.data = append(c.data, value[:remaining]...)
	}
	if len(value) > remaining {
		c.overflow = true
	}
	return len(value), nil
}

func (c *BoundedCapture) Snapshot() (data []byte, total uint64, overflow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data...), c.total, c.overflow
}

func (c *BoundedCapture) MatchExact(expected []byte) error {
	data, total, overflow := c.Snapshot()
	if overflow || total != uint64(len(expected)) || !bytes.Equal(data, expected) {
		return errors.New("captured output mismatch")
	}
	return nil
}
